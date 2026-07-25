// build_rust.go — Rust build path. Stages .rs files, generates the
// wrapper, writes the function's Cargo.toml (the user's, or a default
// skeleton), and compiles a static musl binary for the SLICE's
// architecture inside the rust image. The CLI is SDK-agnostic: the Drift
// SDK is whatever the Cargo.toml declares; cargo fetches it.
//
// The build always runs in a container (see toolchain.go). That removes the
// two host-toolchain hazards this file used to carry — a Homebrew cargo
// shadowing rustup's and lacking the musl target, and the need to locate a
// bundled rust-lld to avoid an external musl cross-linker — because the image
// owns the toolchain and the container already IS the target architecture.
// A function that opts into the SDK's `tls` feature still pulls ring (C/asm);
// that compiles natively in the image rather than cross-compiling.
package atomic_cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"
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

	stageDir, err := stageTempDir("drift-rust-")
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

	// The musl triple for the SLICE's architecture — never the host's. See
	// toolchain.go: the container runs under --platform, so this compiles
	// natively rather than cross-compiling.
	target := rustTarget()

	if out, err := runRustContainer(stageDir, target); err != nil {
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

// rustBuildHint tailors the failure message. A `ring` error means the function
// enabled outbound HTTPS (the SDK's `tls` feature), which drags in C/assembly.
// That now compiles natively in the image, so the usual cause is a missing musl
// C toolchain in the image rather than a cross-compilation problem.
func rustBuildHint(buildOutput, target string) string {
	if strings.Contains(strings.ToLower(buildOutput), "ring") {
		return "Hint: outbound HTTPS (the SDK's \"tls\" feature) pulls `ring`, which has C/assembly and " +
			"needs musl-gcc in the build image. Point DRIFT_BUILD_IMAGE_RUST at an image with musl-tools " +
			"installed, or use only http:// (drop the \"tls\" feature) to keep the build pure-Rust."
	}
	return fmt.Sprintf("Hint: the build runs in %s under --platform %s, targeting %s. "+
		"Override the image with DRIFT_BUILD_IMAGE_RUST if it lacks that target.",
		toolchainImage("rust"), targetPlatform(), target)
}
