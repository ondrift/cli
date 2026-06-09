// run_rust.go — Rust path for the local dev runner. Sets up a
// Cargo project structure (Cargo.toml + src/) in the workspace,
// generates the `main.rs` wrapper, then runs `cargo build` and
// spawns the resulting binary. Re-builds on save.
package atomic_cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
)

func (r *devRunner) generateRust() error {
	funcName := atomic_common.FuncNameForLanguage(r.method, r.name, "rust")
	sourceModule := strings.TrimSuffix(r.sourceFile, ".rs")

	var tmpl string
	switch r.method {
	case "post", "put", "delete", "patch":
		tmpl = wrapperPostRust
	default:
		tmpl = wrapperGetRust
	}

	code := strings.NewReplacer(
		"{{SOURCE}}", sourceModule,
		"{{FUNC}}", funcName,
	).Replace(tmpl)

	srcDir := filepath.Join(r.workDir, "src")
	if err := os.MkdirAll(srcDir, 0o750); err != nil {
		return err
		// #nosec G306 -- snapshot artefacts are intentionally readable by the user (mode 0644/0755) so the portable archive can be unpacked elsewhere; this is the platform's 'your data is yours' contract.
	}
	if err := os.WriteFile(filepath.Join(srcDir, "main.rs"), []byte(code), 0o644); err != nil { // #nosec G306 -- build-time artefact on the user's machine
		return err
	}
	// Move user source into src/ if not already there.
	userSrc := filepath.Join(r.workDir, r.sourceFile)
	userSrcDest := filepath.Join(srcDir, r.sourceFile)
	if _, err := os.Stat(userSrc); err == nil { // #nosec G304 -- controlled workspace
		data, _ := os.ReadFile(userSrc)        // #nosec G304 -- controlled workspace
		os.WriteFile(userSrcDest, data, 0o644) // #nosec G104 G306 -- build-time artefact
	}
	// Ensure Cargo.toml exists: the user's (already synced into the
	// workspace) or the default skeleton. The CLI is SDK-agnostic — the
	// drift-sdk dependency is whatever the Cargo.toml declares.
	cargoPath := filepath.Join(r.workDir, "Cargo.toml")
	if _, err := os.Stat(cargoPath); err != nil {
		return os.WriteFile(cargoPath, []byte(cargoTemplate), 0o644) // #nosec G306 -- build-time artefact
	}
	return nil
}

// ---------- installDeps (run once at startup) ----------

func (r *devRunner) buildAndRunRust() error {
	// Build for local OS/arch (not cross-compiling for local dev).
	buildArgs := []string{"build", "--release"}
	if err := runCmd(r.workDir, "cargo", buildArgs...); err != nil {
		return fmt.Errorf("cargo build: %w", err)
	}

	r.stopProcess()

	envs, _ := readDotEnv(filepath.Join(r.srcDir, ".env"))
	runEnv := os.Environ()
	runEnv = append(runEnv, envs...)
	runEnv = append(runEnv, fmt.Sprintf("PORT=%d", r.port))

	binaryPath := filepath.Join(r.workDir, "target", "release", "atomic-function")
	cmd := exec.Command(binaryPath) // #nosec G204 — controlled temp workspace
	cmd.Dir = r.workDir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = runEnv

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("start app: %w", err)
	}

	r.procLock.Lock()
	r.proc = cmd
	r.procLock.Unlock()

	go func() {
		_ = cmd.Wait()
	}()

	fmt.Printf("✅ Server started (PID %d)\n", cmd.Process.Pid)
	return nil
}
