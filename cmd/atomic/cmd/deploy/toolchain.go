package atomic_cmd

// toolchain.go — where a build toolchain actually runs.
//
// EVERY Atomic build runs inside a throwaway container of the matching language
// image, targeting the SLICE's architecture. There is no host-toolchain path.
//
// Why there is no host path anymore: the host's architecture is not the target's.
// A build that shells out to the developer's own `go`/`cargo`/`pip3` produces an
// artifact for whatever the developer happens to be sitting at, and the slice runs
// on something else. That failure is silent at build time — a clean compile, a
// clean upload, then `exec format error` at first invocation, pointing at nothing
// the user wrote. Every language leaked it differently (Go never set GOARCH; Rust
// and npm branched on runtime.GOARCH; pip and bundler resolved native wheels and
// gem extensions for the host), so the fix is not six cross-compilation flags —
// it is to build ON the target architecture and let each toolchain do the plainly
// native thing. Docker is the only build dependency the CLI has.
//
// The target is a property of the platform, NOT of this machine: see
// targetPlatform(). `runtime.GOARCH` must never appear in a build path again.
//
// Three things make the container path correct rather than merely plausible:
//   - Ownership: containers run as the host uid:gid (`--user`), so vendor dirs
//     and binaries they write are owned by the user, and the CLI can tar/copy/
//     remove them afterwards (a root-owned node_modules would break cleanup).
//   - Bind-mount reachability: on macOS the Docker VM shares /Users but NOT the
//     default $TMPDIR (/var/folders/…), so a `-v /var/folders/…:/w` mounts empty.
//     Every staging dir therefore comes from stageTempDir(), which allocates under
//     UserCacheDir (shared) rather than os.TempDir().
//   - Cache: a per-language host dir under UserCacheDir is mounted at /cache and
//     the toolchain's caches (GOMODCACHE, npm, pip, cargo, …) point into it, so
//     14 functions don't each cold-download the SDK, and re-runs stay warm.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// defaultTargetPlatform is the architecture Drift slices run on. It is a property
// of the fleet, not of the developer's laptop — an x86 laptop and an ARM laptop
// must produce byte-identical-in-kind artifacts for the same slice.
//
// TODO(arch): the operator should report the target slice's architecture and the
// CLI should thread it down here, so a mixed fleet is expressible. Until then this
// constant is the single place the assumption lives, overridable for anyone
// running a slice elsewhere.
const defaultTargetPlatform = "linux/arm64"

// targetPlatform is the docker --platform value every build runs under.
// DRIFT_TARGET_PLATFORM overrides it (e.g. "linux/amd64" for an x86 slice).
func targetPlatform() string {
	if v := strings.TrimSpace(os.Getenv("DRIFT_TARGET_PLATFORM")); v != "" {
		return v
	}
	return defaultTargetPlatform
}

// targetArch is the bare architecture of targetPlatform ("arm64", "amd64").
func targetArch() string {
	p := targetPlatform()
	if i := strings.LastIndex(p, "/"); i >= 0 {
		return p[i+1:]
	}
	return p
}

// rustTarget maps the target platform to its musl triple. Rust is the one
// toolchain that still needs the triple named explicitly, because the binary is
// statically linked against musl rather than the image's glibc.
func rustTarget() string {
	switch targetArch() {
	case "amd64", "x86_64":
		return "x86_64-unknown-linux-musl"
	default:
		return "aarch64-unknown-linux-musl"
	}
}

// npmCPU maps the target platform to npm's --cpu vocabulary, which controls
// which optionalDependencies (sharp, argon2, …) resolve. The container is already
// the right architecture; passing it explicitly keeps the resolution honest even
// if someone overrides the image.
func npmCPU() string {
	switch targetArch() {
	case "amd64", "x86_64":
		return "x64"
	default:
		return "arm64"
	}
}

// toolchainCmd is one toolchain invocation, run inside the language image. All
// args MUST be relative to dir — absolute host paths wouldn't resolve at /w.
type toolchainCmd struct {
	lang string            // "go","rust","python","node","php","ruby" — selects image + cache env
	dir  string            // working directory; bind-mounted at /w
	name string            // tool binary, run inside the image
	args []string          // arguments, all relative to dir
	env  map[string]string // extra environment
}

// runToolchain runs c inside its language container and returns the combined
// output, like exec.Cmd.CombinedOutput.
func runToolchain(c toolchainCmd) ([]byte, error) {
	if err := ensureDocker(); err != nil {
		return nil, err
	}
	cacheDir, err := toolchainCacheDir(c.lang)
	if err != nil {
		return nil, err
	}
	args := []string{
		"run", "--rm",
		"--platform", targetPlatform(),
		"-v", c.dir + ":/w",
		"-w", "/w",
		"-v", cacheDir + ":/cache",
		"-e", "HOME=/cache",
	}
	args = append(args, dockerUserArgs()...)
	for k, v := range toolchainCacheEnv(c.lang) {
		args = append(args, "-e", k+"="+v)
	}
	for k, v := range c.env {
		args = append(args, "-e", k+"="+v)
	}
	args = append(args, toolchainImage(c.lang))
	// The composer image's entrypoint IS composer, so passing the tool name would
	// double it ("composer composer install"); every other image execs the argv.
	if c.lang != "php" {
		args = append(args, c.name)
	}
	args = append(args, c.args...)
	cmd := exec.Command("docker", args...) // #nosec G204 -- args are CLI-derived; the workload is the user's own build
	return cmd.CombinedOutput()
}

// runRustContainer compiles the staged Rust crate to a static musl binary inside
// the rust image. The musl target's std is added on demand (cached in the mounted
// RUSTUP_HOME); the SDK is pure-Rust by default, so rustc's self-contained musl
// linking needs no external C toolchain.
func runRustContainer(stageDir, target string) ([]byte, error) {
	if err := ensureDocker(); err != nil {
		return nil, err
	}
	cacheDir, err := toolchainCacheDir("rust")
	if err != nil {
		return nil, err
	}
	// One container, one shell: add the target (idempotent, cached) then build.
	script := fmt.Sprintf("set -e; rustup target add %s; cargo build --release --target %s", target, target)
	args := []string{
		"run", "--rm",
		"--platform", targetPlatform(),
		"-v", stageDir + ":/w",
		"-w", "/w",
		"-v", cacheDir + ":/cache",
		"-e", "HOME=/cache",
		"-e", "CARGO_HOME=/cache/cargo",
		"-e", "RUSTUP_HOME=/cache/rustup",
	}
	args = append(args, dockerUserArgs()...)
	args = append(args, toolchainImage("rust"), "sh", "-c", script)
	cmd := exec.Command("docker", args...) // #nosec G204 -- args are CLI-derived; the workload is the user's own crate
	return cmd.CombinedOutput()
}

// ensureDocker fails early and legibly when Docker isn't usable, rather than
// letting six different toolchains each report their own version of "not found".
func ensureDocker() error {
	if _, err := exec.LookPath("docker"); err != nil {
		return fmt.Errorf("docker is required to build Atomic functions but was not found on PATH.\n" +
			"Drift builds every function inside a language container so the artifact matches the\n" +
			"slice's architecture rather than your machine's. Install Docker (or Rancher Desktop,\n" +
			"Colima, Podman with a docker shim) and try again.")
	}
	if out, err := exec.Command("docker", "info", "--format", "{{.ServerVersion}}").CombinedOutput(); err != nil {
		return fmt.Errorf("docker is installed but the daemon isn't reachable — start it and try again.\n%s", strings.TrimSpace(string(out)))
	}
	return nil
}

// toolchainImage is the language build image, overridable per language via
// DRIFT_BUILD_IMAGE_<LANG> for air-gapped mirrors or version pinning.
//
// Every image here MUST contain git. Every Drift SDK is a git-sourced
// dependency — `drift-sdk @ git+https://…` (pip), `github:ondrift/sdk` (npm),
// `gem "drift-sdk", git:` (bundler), `{"type":"vcs"}` (composer),
// `drift-sdk = { git = … }` (cargo) — so a build image without git cannot
// resolve the SDK, which is the default path rather than an edge case. The
// -slim Python/Node/Ruby variants ship no git and were caught doing exactly
// that on the first real deploy: "Cannot find command 'git'". This never
// surfaced while builds ran on the host, because a developer always has git.
func toolchainImage(lang string) string {
	if v := os.Getenv("DRIFT_BUILD_IMAGE_" + strings.ToUpper(lang)); v != "" {
		return v
	}
	switch lang {
	case "go":
		return "golang:1.26.2" // matches the platform Go toolchain (Go 1.26.2)
	case "rust":
		return "rust:1-bookworm" // same base as the slice's own Dockerfile
	case "python":
		return "python:3.13" // NOT -slim: needs git for the SDK
	case "node":
		return "node:22" // NOT -slim: needs git for the SDK
	case "php":
		return "composer:2" // php + composer; entrypoint is composer (see caller)
	case "ruby":
		return "ruby:3.1" // NOT -slim: needs git; same 3.1.x the runner bundles
	}
	return ""
}

// toolchainCacheEnv points each toolchain's caches at the mounted /cache so deps
// download once and persist across functions and runs. HOME=/cache (set by the
// caller) covers tools that also write under ~.
func toolchainCacheEnv(lang string) map[string]string {
	switch lang {
	case "go":
		return map[string]string{
			"GOMODCACHE": "/cache/go-mod",
			"GOCACHE":    "/cache/go-build",
			"GOPATH":     "/cache/gopath",
		}
	case "python":
		return map[string]string{"PIP_CACHE_DIR": "/cache/pip"}
	case "node":
		return map[string]string{"npm_config_cache": "/cache/npm"}
	case "php":
		return map[string]string{"COMPOSER_HOME": "/cache", "COMPOSER_CACHE_DIR": "/cache/composer"}
	case "ruby":
		return map[string]string{"GEM_SPEC_CACHE": "/cache/gemspec"}
	case "rust":
		return map[string]string{"CARGO_HOME": "/cache/cargo", "RUSTUP_HOME": "/cache/rustup"}
	}
	return nil
}

// toolchainCacheDir is a stable, host-user-owned dir under UserCacheDir (so it is
// writable by --user and shared into the Docker VM on macOS), created on demand.
// Keyed by architecture: an amd64 npm cache is not interchangeable with an arm64
// one, and mixing them is exactly the class of bug this file exists to end.
func toolchainCacheDir(lang string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate cache dir: %w", err)
	}
	dir := filepath.Join(base, "drift", "build-cache", targetArch(), lang)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create build cache dir: %w", err)
	}
	return dir, nil
}

// dockerUserArgs maps the container's uid:gid to the host user so files it writes
// (vendor dirs, the app binary) are host-owned and removable by the CLI. Skipped
// where there is no meaningful uid (Windows: os.Getuid returns -1), where the
// daemon's default user mapping applies instead.
func dockerUserArgs() []string {
	uid := os.Getuid()
	if uid < 0 {
		return nil
	}
	return []string{"--user", fmt.Sprintf("%d:%d", uid, os.Getgid())}
}

// containerBuildBaseDir is a host-user-owned, Docker-VM-shared base for build
// staging tempdirs (so their bind-mounts aren't empty on macOS).
func containerBuildBaseDir() (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate cache dir: %w", err)
	}
	dir := filepath.Join(base, "drift", "build-staging")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", fmt.Errorf("create build staging dir: %w", err)
	}
	return dir, nil
}

// stageTempDir allocates a build staging directory that is guaranteed to be
// bind-mountable into the build container. Every build path MUST use this rather
// than os.MkdirTemp(""), whose default on macOS (/var/folders/…) is not shared
// with the Docker VM and would mount empty — a build that silently sees no source.
func stageTempDir(pattern string) (string, error) {
	base, err := containerBuildBaseDir()
	if err != nil {
		return "", err
	}
	return os.MkdirTemp(base, pattern)
}
