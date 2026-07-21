// build_node.go — Node.js build path. Stages .js files into a temp
// directory, generates the wrapper, and runs `npm install` (when a
// package.json is present) so the user's declared dependencies — the
// Drift SDK among them — resolve into node_modules. The CLI is
// SDK-agnostic: it installs whatever the manifest declares, never an
// injected copy. `--os=linux --cpu=<arch>` makes platform-specific
// optional deps (sharp, argon2, …) resolve to the linux binaries the
// runner needs. Returns a tar.gz of the staged directory.
package atomic_cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"

	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"
)

func buildNode(absFolder, method, name string) (string, error) {
	funcName := atomic_common.FuncNameForLanguage(method, name, "node")

	// Find the user's source file.
	_, sourceFile, err := atomic_common.DetectLanguage(absFolder)
	if err != nil {
		return "", err
	}
	sourceModule := strings.TrimSuffix(filepath.Base(sourceFile), ".js")

	// Create a staging directory.
	stageDir, err := os.MkdirTemp("", "drift-node-")
	if err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	// Copy all .js files from the function directory.
	entries, _ := os.ReadDir(absFolder)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".js") {
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

	// Generate wrapper app.js (requires('@ondrift/sdk')).
	if err := generateNodeWrapper(stageDir, sourceModule, funcName, method); err != nil {
		return "", fmt.Errorf("generate wrapper: %w", err)
	}

	// Install the user's declared dependencies (the Drift SDK is whatever
	// the function's package.json declares) when a package.json is present.
	// `--os=linux --cpu=<arch>` resolves linux platform binaries for native
	// optional deps. The CLI is SDK-agnostic — it installs the manifest.
	if data, rerr := os.ReadFile(filepath.Join(absFolder, "package.json")); rerr == nil { // #nosec G304 -- controlled base dir
		if werr := os.WriteFile(filepath.Join(stageDir, "package.json"), data, 0o644); werr != nil { // #nosec G306 -- build-time artefact
			return "", fmt.Errorf("write staged package.json: %w", werr)
		}
		if lockData, lerr := os.ReadFile(filepath.Join(absFolder, "package-lock.json")); lerr == nil { // #nosec G304 -- controlled base dir
			_ = os.WriteFile(filepath.Join(stageDir, "package-lock.json"), lockData, 0o644) // #nosec G306 -- build-time artefact
		}
		npmCPU := "x64"
		if runtime.GOARCH == "arm64" {
			npmCPU = "arm64"
		}
		cmd := exec.Command("npm", "install", "--production", "--silent", "--os=linux", "--cpu="+npmCPU) // #nosec G204
		cmd.Dir = stageDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("npm install error: %w\n%s", err, string(out))
		}
	}

	// Create tar.gz archive in a unique temp file to avoid races when
	// multiple CLI instances deploy functions with the same name concurrently.
	archiveFile, err := os.CreateTemp("", fmt.Sprintf("drift-node-%s-*.tar.gz", safeTmpName(name)))
	if err != nil {
		return "", fmt.Errorf("create archive temp: %w", err)
	}
	archivePath := archiveFile.Name()
	archiveFile.Close()                                        // #nosec G104 -- best-effort close of the temp handle; createTarGz re-creates the file
	if err := createTarGz(stageDir, archivePath); err != nil { // #nosec G104
		return "", fmt.Errorf("create archive: %w", err)
	}

	return archivePath, nil
}
