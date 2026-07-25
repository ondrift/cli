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
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"
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
	stageDir, err := stageTempDir("drift-python-")
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
	// manifest verbatim.
	//
	// No --platform/--only-binary is needed, and that is the point: the build
	// runs in a python image under the SLICE's --platform, so pip resolves the
	// native wheels for the target architecture by simply being there. On the
	// host this silently produced manylinux_x86_64 wheels for an arm64 slice
	// (and the reverse), which crashed at first invocation rather than at build.
	if err := installPythonDeps(absFolder, stageDir); err != nil {
		return "", err
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
