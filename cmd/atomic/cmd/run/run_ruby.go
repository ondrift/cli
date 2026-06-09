// run_ruby.go — Ruby path for the local dev runner. Generates the
// `app.rb` wrapper. Dispatched via `ruby app.rb` by the shared
// runInterpreted helper.
package atomic_cmd

import (
	"os"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/cmd/atomic/common"
)

func (r *devRunner) generateRuby() error {
	funcName := atomic_common.FuncNameForLanguage(r.method, r.name, "ruby")
	sourceModule := strings.TrimSuffix(r.sourceFile, ".rb")

	var tmpl string
	switch r.method {
	case "post", "put", "delete", "patch":
		tmpl = wrapperPostRuby
	default:
		tmpl = wrapperGetRuby
	}

	code := strings.NewReplacer(
		"{{SOURCE}}", sourceModule,
		"{{FUNC}}", funcName,
	).Replace(tmpl)

	// SDK comes from `bundle install` (git gem) in installDeps.
	return os.WriteFile(filepath.Join(r.workDir, "app.rb"), []byte(code), 0o644) // #nosec G306 -- build-time artefact on the user's machine
}
