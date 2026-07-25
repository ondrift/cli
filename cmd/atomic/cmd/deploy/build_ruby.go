// build_ruby.go — Ruby build path. Stages .rb files, generates the
// wrapper, and (when a Gemfile is present) runs `bundle install
// --standalone` inside the ruby image, under the SLICE's --platform.
// The image pins the Ruby version (no host Ruby, no Apple 2.6 problem)
// and any gem with a C extension compiles for the architecture the runner
// actually has. The wrapper patches RbConfig to whatever version dir was
// produced. The CLI is SDK-agnostic: it installs whatever the Gemfile
// declares. Returns a tar.gz of the staged directory.
package atomic_cmd

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"
)

func buildRuby(absFolder, method, name string) (string, error) {
	funcName := atomic_common.FuncNameForLanguage(method, name, "ruby")

	_, sourceFile, err := atomic_common.DetectLanguage(absFolder)
	if err != nil {
		return "", err
	}
	sourceModule := strings.TrimSuffix(filepath.Base(sourceFile), ".rb")

	stageDir, err := stageTempDir("drift-ruby-")
	if err != nil {
		return "", fmt.Errorf("create staging dir: %w", err)
	}
	defer os.RemoveAll(stageDir)

	// Copy all .rb files.
	entries, _ := os.ReadDir(absFolder)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".rb") {
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

	if err := generateRubyWrapper(stageDir, sourceModule, funcName, method); err != nil {
		return "", fmt.Errorf("generate wrapper: %w", err)
	}

	// Install the user's declared gems (the Drift SDK among them) if a
	// Gemfile is present. The CLI is SDK-agnostic: it installs whatever the
	// manifest declares.
	if err := installRubyDeps(absFolder, stageDir); err != nil {
		return "", err
	}

	archiveFile, err := os.CreateTemp("", fmt.Sprintf("drift-ruby-%s-*.tar.gz", safeTmpName(name)))
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
