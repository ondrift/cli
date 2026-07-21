// run_node.go — Node.js path for the local dev runner. Generates
// the `app.js` wrapper. The shared `runInterpreted` helper in
// run.go dispatches `node app.js`.
package atomic_cmd

import (
	"os"
	"path/filepath"
	"strings"

	atomic_common "github.com/ondrift/cli/v2/cmd/atomic/common"
)

func (r *devRunner) generateNode() error {
	funcName := atomic_common.FuncNameForLanguage(r.method, r.name, "node")
	sourceModule := strings.TrimSuffix(r.sourceFile, ".js")

	var tmpl string
	switch r.method {
	case "post", "put", "delete", "patch":
		tmpl = wrapperPostNode
	default:
		tmpl = wrapperGetNode
	}

	code := strings.NewReplacer(
		"{{SOURCE}}", sourceModule,
		"{{FUNC}}", funcName,
	).Replace(tmpl)

	// SDK comes from `npm install github:ondrift/sdk` in installDeps.
	return os.WriteFile(filepath.Join(r.workDir, "app.js"), []byte(code), 0o644) // #nosec G306 -- build-time artefact on the user's machine
}
