// run_go.go — Go language path for the local dev runner.
// Compiles `go build -o app ./...` from the user's workspace
// (with the embedded SDK vendored into a side directory and a
// `replace` directive injected into a copy of go.mod), then
// spawns the binary as the dev process. Re-runs on every save.
package atomic_cmd

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
)

func (r *devRunner) generateGo() error {
	// Use the shared FuncNameForLanguage helper so route templates
	// with path-parameter (`:id`) and multi-segment (`reviewer/blob`)
	// names produce a valid Go identifier — same path
	// `drift atomic deploy`'s build_go.go takes.
	funcName := atomic_common.FuncNameForLanguage(r.method, r.name, "native")

	var code string
	replacer := strings.NewReplacer(
		"{{FUNC}}", funcName,
		"{{ROUTE}}", "/"+r.name,
		"{{PORT}}", fmt.Sprintf("%d", r.port),
	)
	switch r.method {
	case "post", "put", "delete", "patch":
		code = replacer.Replace(defaultGolangServerPost)
	default:
		code = replacer.Replace(defaultGolangServerGet)
	}

	return os.WriteFile(filepath.Join(r.workDir, "main.go"), []byte(code), 0o600)
}

func (r *devRunner) buildAndRunGo() error {
	// Pull the published SDK at its latest tag — root module named
	// explicitly to dodge the legacy nested-module pseudo-versions (see
	// atomic_common.DriftGoModule). No version pinned.
	if err := runCmd(r.workDir, "go", "get", atomic_common.DriftGoModule+"@latest"); err != nil {
		log.Printf("go get drift sdk warning: %v", err)
	}
	if err := runCmd(r.workDir, "go", "mod", "tidy"); err != nil {
		// Not fatal; the project may already be tidy, but log it.
		log.Printf("go mod tidy warning: %v", err)
	}

	// Build for the local OS/arch (we're running locally)
	buildArgs := []string{"build", "-o", "app"}
	if err := runCmd(r.workDir, "go", buildArgs...); err != nil {
		return fmt.Errorf("go build: %w", err)
	}

	// Stop any existing process
	r.stopProcess()

	// Load .env (from source) into environment for the run if present in annotation
	envs, _ := readDotEnv(filepath.Join(r.srcDir, ".env"))
	runEnv := os.Environ()
	runEnv = append(runEnv, envs...)
	runEnv = append(runEnv, fmt.Sprintf("PORT=%d", r.port))

	// Start the process
	cmd := exec.Command(filepath.Join(r.workDir, "app")) // #nosec G204 — path is a controlled temp workspace
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
