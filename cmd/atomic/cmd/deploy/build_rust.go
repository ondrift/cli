// build_rust.go — Rust build path. Stages .rs files, generates the
// wrapper, writes the function's Cargo.toml (the user's, or a default
// skeleton), runs `cargo build --release --target
// {x86_64,aarch64}-unknown-linux-musl`, and returns the compiled binary.
// The CLI is SDK-agnostic: the Drift SDK is whatever the Cargo.toml
// declares; cargo fetches it.
//
// Two non-obvious bits worth keeping visible:
//
//   - Toolchain pinning. A common dev setup has both Homebrew's `rust`
//     formula and rustup on PATH; brew's cargo shadows rustup's, but
//     brew's rustc lacks the musl target — the cross-compile silently
//     fails with "can't find crate for core". We resolve rustup's
//     toolchain dir explicitly, prepend it to PATH, and pin RUSTC.
//   - No external linker. We link with Rust's bundled rust-lld
//     (CARGO_TARGET_*_LINKER) instead of a musl cross-gcc, so a Rust
//     deploy needs only rustup — no Homebrew toolchain. This holds
//     because the SDK is pure Rust by default (TLS is opt-in); a function
//     that turns on the SDK's `tls` feature pulls ring (C) and then does
//     need a C cross-compiler (the build error points the way).
package atomic_cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
)

// buildRust compiles the Rust function to a static Linux binary client-side
// and returns the path to the binary.
func buildRust(absFolder, method, name string) (string, error) {
	funcName := atomic_common.FuncNameForLanguage(method, name, "rust")

	_, sourceFile, err := atomic_common.DetectLanguage(absFolder)
	if err != nil {
		return "", err
	}
	sourceModule := strings.TrimSuffix(filepath.Base(sourceFile), ".rs")

	stageDir, err := os.MkdirTemp("", "drift-rust-")
	if err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	srcDir := filepath.Join(stageDir, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		return "", fmt.Errorf("create src dir: %w", err)
	}

	// Cargo.toml: the user's if present, else the default skeleton. The CLI
	// is SDK-agnostic — the drift-sdk dependency is whatever the Cargo.toml
	// declares (the skeleton ships a default).
	var cargoData string
	userCargoPath := filepath.Join(absFolder, "Cargo.toml")
	if data, rerr := os.ReadFile(userCargoPath); rerr == nil { // #nosec G304 -- controlled base dir
		cargoData = string(data)
	} else {
		cargoData = cargoTemplate
	}
	if werr := os.WriteFile(filepath.Join(stageDir, "Cargo.toml"), []byte(cargoData), 0o644); werr != nil { // #nosec G306 -- build-time artefact
		return "", fmt.Errorf("write Cargo.toml: %w", werr)
	}

	// Copy all .rs files into src/.
	entries, _ := os.ReadDir(absFolder)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".rs") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(absFolder, e.Name())) // #nosec G304 -- path is built from a CLI-validated argument or a regex-validated name plus a controlled base directory; never untrusted input.
		if err != nil {
			return "", fmt.Errorf("read %s: %w", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(srcDir, e.Name()), data, 0o644); err != nil { // #nosec G306 G703 -- the path is the CLI's stageDir on the user's machine; mode 0644 is intentional for a build-time artefact.
			return "", fmt.Errorf("write %s: %w", e.Name(), err)
		}
	}

	// Generate main.rs wrapper (uses drift_sdk::run; no injected SDK module).
	if err := generateRustWrapper(stageDir, sourceModule, funcName, method); err != nil {
		return "", fmt.Errorf("generate wrapper: %w", err)
	}

	// Build for Linux (cross-compile). Match the host arch since Rancher Desktop
	// runs containers with the same architecture as the host.
	target := "x86_64-unknown-linux-musl"
	if runtime.GOARCH == "arm64" {
		target = "aarch64-unknown-linux-musl"
	}
	cmd := exec.Command("cargo", "build", "--release", "--target", target) // #nosec G204
	cmd.Dir = stageDir
	cmd.Env = os.Environ()

	rustcBin := "rustc" // fallback if rustup isn't installed
	if rustupPath, err := exec.LookPath("rustup"); err == nil {
		// Make sure the musl target's std is present — idempotent, no-op if
		// it's already installed. "Have Rust, go": the user shouldn't have to
		// remember `rustup target add`.
		_ = exec.Command(rustupPath, "target", "add", target).Run() // #nosec G204

		// Prefer rustup's toolchain (brew's rustc lacks the musl target; see
		// the package comment). Pin cargo + rustc explicitly.
		if toolchainDir, runErr := exec.Command(rustupPath, "which", "--toolchain", "stable", "cargo").Output(); runErr == nil {
			cargoBin := strings.TrimSpace(string(toolchainDir))
			binDir := filepath.Dir(cargoBin)
			rustcBin = filepath.Join(binDir, "rustc")
			cmd.Path = cargoBin
			cmd.Args[0] = cargoBin
			cmd.Env = append(cmd.Env, "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
			cmd.Env = append(cmd.Env, "RUSTC="+rustcBin)
		}
	}

	// Link with Rust's own rust-lld (ships in every toolchain) so NO external
	// musl cross-linker is needed — the whole point: deploy Rust with rustup
	// alone. This works because the SDK is pure Rust (its ureq has TLS off by
	// default). A function that opts into the SDK's `tls` feature pulls ring
	// (C/asm) and will need a C cross-compiler — see rustBuildHint.
	if lld := findRustLLD(rustcBin); lld != "" {
		linkerEnv := fmt.Sprintf("CARGO_TARGET_%s_LINKER", strings.ReplaceAll(strings.ToUpper(target), "-", "_"))
		cmd.Env = append(cmd.Env, linkerEnv+"="+lld)
	}

	if out, err := cmd.CombinedOutput(); err != nil {
		return "", fmt.Errorf("cargo build error (target %s): %w\n%s\n%s", target, err, string(out), rustBuildHint(string(out), target))
	}

	binaryPath := filepath.Join(stageDir, "target", target, "release", "atomic-function")
	outputPath := filepath.Join(os.TempDir(), fmt.Sprintf("drift-rust-%s", safeTmpName(name)))
	data, err := os.ReadFile(binaryPath) // #nosec G304
	if err != nil {
		return "", fmt.Errorf("read compiled binary: %w", err)
	}
	if err := os.WriteFile(outputPath, data, 0o755); err != nil { // #nosec G306 G703 -- the compiled binary must be executable by the runner
		return "", fmt.Errorf("write binary: %w", err)
	}

	return outputPath, nil
}

// findRustLLD locates the toolchain's bundled rust-lld linker under the
// rustc sysroot (…/lib/rustlib/<host>/bin/rust-lld), or returns "" if it
// can't be found. Using it means we never need an external musl cross-linker.
func findRustLLD(rustcBin string) string {
	out, err := exec.Command(rustcBin, "--print", "sysroot").Output() // #nosec G204 -- rustcBin is a discovered toolchain binary, not user input
	if err != nil {
		return ""
	}
	matches, _ := filepath.Glob(filepath.Join(strings.TrimSpace(string(out)), "lib", "rustlib", "*", "bin", "rust-lld"))
	if len(matches) > 0 {
		return matches[0]
	}
	return ""
}

// rustBuildHint tailors the failure message. A `ring` error means the
// function enabled outbound HTTPS (the SDK's `tls` feature), which drags in
// C/assembly — that needs a C cross-toolchain. Anything else is most likely a
// missing target or a non-rustup toolchain.
func rustBuildHint(buildOutput, target string) string {
	if strings.Contains(strings.ToLower(buildOutput), "ring") {
		return "Hint: outbound HTTPS (the SDK's \"tls\" feature) pulls `ring`, which has C/assembly. " +
			"Cross-compiling it to musl needs a C toolchain — install zig and run `cargo install cargo-zigbuild`, " +
			"or use only http:// (drop the \"tls\" feature) to keep the build pure-Rust."
	}
	return fmt.Sprintf("Hint: run `rustup target add %s`, and make sure you're using a rustup toolchain (not Homebrew's rust).", target)
}
