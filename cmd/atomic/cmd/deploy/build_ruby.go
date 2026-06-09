// build_ruby.go — Ruby build path. Stages .rb files, generates the
// wrapper, and (when a Gemfile is present) runs `bundle install
// --standalone` with the host's own Ruby — no Docker. "Write Ruby, have
// Ruby, go." The standalone gems are pure-Ruby (the SDK has no C
// extensions), so they're portable to the runner regardless of the host
// Ruby version; the wrapper patches RbConfig to whatever version dir was
// produced. We deliberately use a host Ruby >= 3.0 (see
// atomic_common.FindRuby) because Apple's system Ruby 2.6 is too old. The
// CLI is SDK-agnostic: it installs whatever the Gemfile declares. Returns
// a tar.gz of the staged directory.
package atomic_cmd

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
)

func buildRuby(absFolder, method, name string) (string, error) {
	funcName := atomic_common.FuncNameForLanguage(method, name, "ruby")

	_, sourceFile, err := atomic_common.DetectLanguage(absFolder)
	if err != nil {
		return "", err
	}
	sourceModule := strings.TrimSuffix(filepath.Base(sourceFile), ".rb")

	stageDir, err := os.MkdirTemp("", "drift-ruby-")
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
	// Gemfile is present, using the host's own Ruby — no Docker. The CLI is
	// SDK-agnostic: it installs whatever the manifest declares. --standalone
	// emits vendor/bundle/bundler/setup.rb which the wrapper loads without
	// requiring bundler at runtime.
	gemfilePath := filepath.Join(absFolder, "Gemfile")
	if _, err := os.Stat(gemfilePath); err == nil {
		data, rerr := os.ReadFile(gemfilePath) // #nosec G304 -- controlled base dir
		if rerr != nil {
			return "", fmt.Errorf("read Gemfile: %w", rerr)
		}
		if werr := os.WriteFile(filepath.Join(stageDir, "Gemfile"), data, 0o644); werr != nil { // #nosec G306 -- build-time artefact
			return "", fmt.Errorf("write staged Gemfile: %w", werr)
		}
		if lockData, lerr := os.ReadFile(filepath.Join(absFolder, "Gemfile.lock")); lerr == nil { // #nosec G304 -- controlled base dir
			_ = os.WriteFile(filepath.Join(stageDir, "Gemfile.lock"), lockData, 0o644) // #nosec G306 -- build-time artefact
		}
		// Use a host Ruby >= 3.0 (Apple's system 2.6 is too old). BUNDLE_PATH
		// /BUNDLE_WITHOUT go through env so this works across bundler 2.x–4.x
		// (4.x dropped the --path flag), and the Ruby's bin dir leads PATH so
		// `bundle` drives the matching interpreter.
		rb, ferr := atomic_common.FindRuby()
		if ferr != nil {
			return "", ferr
		}
		cmd := exec.Command(rb.Bundle, "install", "--standalone", "--quiet") // #nosec G204 -- bundle path is from toolchain discovery, not user input
		cmd.Dir = stageDir
		cmd.Env = append(os.Environ(),
			"PATH="+rb.BinDir+string(os.PathListSeparator)+os.Getenv("PATH"),
			"BUNDLE_PATH=vendor/bundle",
			"BUNDLE_WITHOUT=development:test",
		)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("bundle install error (ruby %s): %w\n%s", rb.Version, err, string(out))
		}
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
