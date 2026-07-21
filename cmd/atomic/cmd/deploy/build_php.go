// build_php.go — PHP build path. Stages .php files, generates the
// wrapper, and (when a composer.json is present) runs `composer install
// --no-dev` so the user's declared packages — the Drift SDK among them —
// resolve into vendor/. The CLI is SDK-agnostic: it installs the
// manifest. The wrapper loads vendor/autoload.php, which autoloads the
// SDK's \Drift\ namespace. Returns a tar.gz of the staged directory.
package atomic_cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"
)

func buildPHP(absFolder, method, name string) (string, error) {
	funcName := atomic_common.FuncNameForLanguage(method, name, "php")

	_, sourceFile, err := atomic_common.DetectLanguage(absFolder)
	if err != nil {
		return "", err
	}
	sourceModule := strings.TrimSuffix(filepath.Base(sourceFile), ".php")

	stageDir, err := os.MkdirTemp("", "drift-php-")
	if err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	// Copy all .php files.
	entries, _ := os.ReadDir(absFolder)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".php") {
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

	if err := generatePHPWrapper(stageDir, sourceModule, funcName, method); err != nil {
		return "", fmt.Errorf("generate wrapper: %w", err)
	}

	// Install the user's declared packages (the Drift SDK among them) if a
	// composer.json is present. The CLI is SDK-agnostic — it installs the
	// manifest verbatim.
	composerPath := filepath.Join(absFolder, "composer.json")
	if _, serr := os.Stat(composerPath); serr == nil {
		data, rerr := os.ReadFile(composerPath) // #nosec G304 -- controlled base dir
		if rerr != nil {
			return "", fmt.Errorf("read composer.json: %w", rerr)
		}
		if werr := os.WriteFile(filepath.Join(stageDir, "composer.json"), data, 0o644); werr != nil { // #nosec G306 -- build-time artefact
			return "", fmt.Errorf("write staged composer.json: %w", werr)
		}
		if lockData, lerr := os.ReadFile(filepath.Join(absFolder, "composer.lock")); lerr == nil { // #nosec G304 -- controlled base dir
			_ = os.WriteFile(filepath.Join(stageDir, "composer.lock"), lockData, 0o644) // #nosec G306 -- build-time artefact
		}
		cmd := exec.Command("composer", "install", "--no-dev", "--ignore-platform-reqs", "--quiet", "--no-interaction") // #nosec G204
		cmd.Dir = stageDir
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("composer install error: %w\n%s", err, string(out))
		}
	}

	archiveFile, err := os.CreateTemp("", fmt.Sprintf("drift-php-%s-*.tar.gz", safeTmpName(name)))
	if err != nil {
		return "", fmt.Errorf("create archive temp: %w", err)
	}
	archivePath := archiveFile.Name()
	archiveFile.Close()                                        // #nosec G104 -- best-effort close; createTarGz re-creates the file
	if err := createTarGz(stageDir, archivePath); err != nil { // #nosec G104
		return "", fmt.Errorf("create archive: %w", err)
	}

	return archivePath, nil
}
