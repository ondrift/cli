// build_python.go — Python build path. Stages the user's .py files into
// a temp directory, generates the wrapper, and installs the user's
// declared dependencies (the Drift SDK among them, from requirements.txt)
// into vendor/ — the wrapper prepends vendor/ to sys.path, so `import
// drift` resolves it. The CLI is SDK-agnostic: it installs whatever the
// manifest declares. Returns a tar.gz of the staged directory.
package atomic_cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
)

// buildPython generates the wrapper, installs the SDK + deps into
// vendor/, creates a tar.gz archive, and returns its path.
func buildPython(absFolder, method, name string) (string, error) {
	funcName := atomic_common.FuncNameForLanguage(method, name, "python")

	// Find the user's source file (the .py with @atomic annotation).
	_, sourceFile, err := atomic_common.DetectLanguage(absFolder)
	if err != nil {
		return "", err
	}
	sourceModule := strings.TrimSuffix(filepath.Base(sourceFile), ".py")

	// Create a staging directory for the archive.
	stageDir, err := os.MkdirTemp("", "drift-python-")
	if err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	// Copy all .py files from the function directory.
	entries, _ := os.ReadDir(absFolder)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".py") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(absFolder, e.Name())) // #nosec G304 -- path is built from a CLI-validated argument or a regex-validated name plus a controlled base directory; never untrusted input.
		if err != nil {
			return "", fmt.Errorf("read %s: %w", e.Name(), err)
		}
		if err := os.WriteFile(filepath.Join(stageDir, e.Name()), data, 0o644); err != nil { // #nosec G306 G703 -- the path is the CLI's stageDir on the user's machine; mode 0644 is intentional for a build-time artefact.
			return "", fmt.Errorf("write %s: %w", e.Name(), err)
		}
	}

	// Generate wrapper app.py (prepends vendor/ to sys.path, imports drift).
	if err := generatePythonWrapper(stageDir, sourceModule, funcName, method); err != nil {
		return "", fmt.Errorf("generate wrapper: %w", err)
	}

	// Install the user's declared dependencies (the Drift SDK among them)
	// into vendor/ if a requirements.txt is present. The wrapper prepends
	// vendor/ to sys.path. The CLI is SDK-agnostic — it installs the
	// manifest verbatim. No --platform/--only-binary: a git-source dep
	// (like the SDK) can't be a prebuilt wheel; functions needing
	// linux-native wheels should depend on PyPI-published packages.
	reqPath := filepath.Join(absFolder, "requirements.txt")
	if _, err := os.Stat(reqPath); err == nil {
		vendorDir := filepath.Join(stageDir, "vendor")
		cmd := exec.Command("pip3", "install", "-t", vendorDir, "-r", reqPath, "--quiet") // #nosec G204
		cmd.Dir = absFolder
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("pip install error: %w\n%s", err, string(out))
		}
	}

	// Create tar.gz archive in a unique temp file to avoid races when
	// multiple CLI instances deploy functions with the same name concurrently.
	archiveFile, err := os.CreateTemp("", fmt.Sprintf("drift-python-%s-*.tar.gz", safeTmpName(name)))
	if err != nil {
		return "", fmt.Errorf("create archive temp: %w", err)
	}
	archivePath := archiveFile.Name()
	archiveFile.Close() // #nosec G104 -- best-effort close; createTarGz re-creates the file
	if err := createTarGz(stageDir, archivePath); err != nil {
		return "", fmt.Errorf("create archive: %w", err)
	}

	return archivePath, nil
}
