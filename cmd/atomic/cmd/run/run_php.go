// run_php.go — PHP path for the local dev runner. Generates the
// `app.php` wrapper. Dispatched via `php app.php` by the shared
// runInterpreted helper.
package atomic_cmd

import (
	"os"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"
)

func (r *devRunner) generatePHP() error {
	funcName := atomic_common.FuncNameForLanguage(r.method, r.name, "php")
	sourceModule := strings.TrimSuffix(r.sourceFile, ".php")

	var tmpl string
	switch r.method {
	case "post", "put", "delete", "patch":
		tmpl = wrapperPostPHP
	default:
		tmpl = wrapperGetPHP
	}

	code := strings.NewReplacer(
		"{{SOURCE}}", sourceModule,
		"{{FUNC}}", funcName,
	).Replace(tmpl)

	// SDK comes from `composer install` (vcs repo) in installDeps; the
	// wrapper loads vendor/autoload.php.
	return os.WriteFile(filepath.Join(r.workDir, "app.php"), []byte(code), 0o644) // #nosec G306 -- build-time artefact on the user's machine
}
