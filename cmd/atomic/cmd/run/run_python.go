// run_python.go — Python path for the local dev runner. Generates
// the `app.py` wrapper that imports the user's handler and runs
// it on `PORT`. The shared `runInterpreted` helper in run.go
// dispatches `python3 app.py` after dependencies are installed.
package atomic_cmd

import (
	"os"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"
)

func (r *devRunner) generatePython() error {
	funcName := atomic_common.FuncNameForLanguage(r.method, r.name, "python")
	sourceModule := strings.TrimSuffix(r.sourceFile, ".py")

	var tmpl string
	switch r.method {
	case "post", "put", "delete", "patch":
		tmpl = wrapperPostPython
	default:
		tmpl = wrapperGetPython
	}

	code := strings.NewReplacer(
		"{{SOURCE}}", sourceModule,
		"{{FUNC}}", funcName,
	).Replace(tmpl)

	// SDK comes from `pip install drift-sdk @ git+…` (into vendor/) in installDeps.
	return os.WriteFile(filepath.Join(r.workDir, "app.py"), []byte(code), 0o644) // #nosec G306 -- build-time artefact on the user's machine
}
