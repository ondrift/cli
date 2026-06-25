package atomic_cmd

// toolchain.go — where a build toolchain actually runs.
//
// `drift atomic deploy` (the cloud path) builds on the platform's build host, so
// it execs `go`/`cargo`/`pip3`/… directly — the host toolchain. `drift project
// run` instead targets a user's laptop where the whole promise is "install only
// Docker": no Go, no Rust, no pip on the host. So for `run` every toolchain
// invocation is redirected into a throwaway container of the matching language
// image, with the work dir bind-mounted and a persistent cache reused across
// functions and runs.
//
// The redirect is a single chokepoint, runToolchain, swapped on by
// StageElementsLocally (the `run` entry) exactly like the SlotSink swap — so the
// build LOGIC is shared verbatim between cloud and local; only where the compiler
// runs differs. Host mode reproduces the previous exec.Command behaviour byte for
// byte, so the cloud deploy path is unchanged.
//
// Three things make the container path correct rather than merely plausible:
//   - Ownership: containers run as the host uid:gid (`--user`), so vendor dirs
//     and binaries they write are owned by the user, and the CLI can tar/copy/
//     remove them afterwards (a root-owned node_modules would break cleanup).
//   - Bind-mount reachability: on macOS the Docker VM shares /Users but NOT the
//     default $TMPDIR (/var/folders/…), so a `-v /var/folders/…:/w` mounts empty.
//     StageElementsLocally points TMPDIR at a dir under UserCacheDir (~/Library/
//     Caches → shared) for the build, so every staging tempdir is mountable.
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

// toolchainContainerMode is flipped on by StageElementsLocally for the duration
// of a `drift project run` build. Set once before the (possibly parallel) build
// fan-out and restored after, so concurrent reads are safe. When false, every
// call below is the host toolchain, identical to the pre-existing behaviour.
var toolchainContainerMode bool

// toolchainCmd is one toolchain invocation, expressed so it runs the same whether
// execed on the host or inside a container. All args MUST be relative to dir (no
// absolute host paths — they wouldn't resolve at the container's /w mount).
type toolchainCmd struct {
	lang string            // "go","rust","python","node","php","ruby" — selects image + cache env
	dir  string            // working directory; bind-mounted at /w in container mode
	name string            // tool binary, e.g. "go" (host execs it; container runs it in the image)
	args []string          // arguments, all relative to dir
	env  map[string]string // extra environment, applied in both modes

	// Host-only knobs, ignored in container mode (the image owns the toolchain):
	hostName string // absolute tool path on the host (e.g. a discovered `bundle`); defaults to name
	hostPath string // prepended to PATH on the host (e.g. the matching Ruby's bin dir)
}

// runToolchain runs c and returns its combined output, like exec.Cmd.CombinedOutput.
// Container vs host is chosen by toolchainContainerMode so call sites stay identical.
func runToolchain(c toolchainCmd) ([]byte, error) {
	if toolchainContainerMode {
		return runToolchainContainer(c)
	}
	return runToolchainHost(c)
}

// runToolchainHost reproduces the original exec.Command(...).CombinedOutput()
// behaviour: same binary, same working dir, inherited env plus c.env (and the
// optional host PATH prefix). This is the cloud deploy path.
func runToolchainHost(c toolchainCmd) ([]byte, error) {
	name := c.hostName
	if name == "" {
		name = c.name
	}
	cmd := exec.Command(name, c.args...) // #nosec G204 -- name/args are CLI-derived toolchain commands, not user input
	cmd.Dir = c.dir
	env := os.Environ()
	if c.hostPath != "" {
		env = append(env, "PATH="+c.hostPath+string(os.PathListSeparator)+os.Getenv("PATH"))
	}
	for k, v := range c.env {
		env = append(env, k+"="+v)
	}
	cmd.Env = env
	return cmd.CombinedOutput()
}

// runToolchainContainer runs c inside a throwaway language container: the work
// dir at /w, a persistent per-language cache at /cache, as the host user so
// outputs are owned by the user. Returns docker's combined output (which carries
// both docker's own errors and the tool's, so build failures still surface).
func runToolchainContainer(c toolchainCmd) ([]byte, error) {
	cacheDir, err := toolchainCacheDir(c.lang)
	if err != nil {
		return nil, err
	}
	args := []string{
		"run", "--rm",
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

// toolchainImage is the language build image, overridable per language via
// DRIFT_BUILD_IMAGE_<LANG> for air-gapped mirrors or version pinning.
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
		return "python:3.13-slim"
	case "node":
		return "node:22-slim"
	case "php":
		return "composer:2" // php + composer; entrypoint is composer (see caller)
	case "ruby":
		return "ruby:3.1-slim" // matches the runner's bundled Ruby dir (3.1.x)
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
func toolchainCacheDir(lang string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return "", fmt.Errorf("locate cache dir: %w", err)
	}
	dir := filepath.Join(base, "drift", "build-cache", lang)
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

// runRustContainer compiles the staged Rust crate to a static musl binary inside
// the rust image, skipping the host's rustup/rust-lld resolution entirely (the
// image owns the toolchain). The musl target's std is added on demand (cached in
// the mounted RUSTUP_HOME); the SDK is pure-Rust by default, so rustc's
// self-contained musl linking needs no external C toolchain — matching the host
// path's "rustup alone" guarantee. UNVERIFIED locally (no Rust app on hand).
func runRustContainer(stageDir, target string) ([]byte, error) {
	cacheDir, err := toolchainCacheDir("rust")
	if err != nil {
		return nil, err
	}
	// One container, one shell: add the target (idempotent, cached) then build.
	script := fmt.Sprintf("set -e; rustup target add %s; cargo build --release --target %s", target, target)
	args := []string{
		"run", "--rm",
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

// containerBuildBaseDir is a host-user-owned, Docker-VM-shared base for build
// staging tempdirs (so their bind-mounts aren't empty on macOS). StageElements
// Locally points TMPDIR here for the build.
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

// hostBuildRequested lets a user with the toolchains installed opt back into a
// host build (faster, no image pulls) via DRIFT_RUN_HOST_BUILD=1.
func hostBuildRequested() bool {
	v := os.Getenv("DRIFT_RUN_HOST_BUILD")
	return v == "1" || strings.EqualFold(v, "true")
}
