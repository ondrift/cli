// Package upgrade implements `drift upgrade [version]` — the CLI self-update.
//
// The drift CLI is installed with `go install`, so upgrading is just running it
// again at a newer version. With no argument it installs the latest published
// release; with a version it pins an exact one (selective upgrade — or a
// rollback, e.g. `drift upgrade v1.8.1`).
package upgrade

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

// GetCmd builds the `drift upgrade` command.
func GetCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "upgrade [version]",
		Short: "Update the drift CLI to the latest release (or pin a version)",
		Long: "Reinstalls the drift CLI with `go install`, replacing the binary in place.\n\n" +
			"With no argument it installs the latest published release. Pass a version to\n" +
			"pin an exact one — handy for trying a specific build or rolling back:\n" +
			"  drift upgrade            # latest\n" +
			"  drift upgrade v1.8.1     # that exact version (rollback / pin)",
		Example: "  drift upgrade\n  drift upgrade v1.8.1",
		Args:    cobra.MaximumNArgs(1),
		GroupID: "account",
		RunE: func(cmd *cobra.Command, args []string) error {
			requested := ""
			if len(args) > 0 {
				requested = args[0]
			}
			return runUpgrade(cmd.Root().Version, requested)
		},
	}
}

func runUpgrade(current, requested string) error {
	if _, err := exec.LookPath("go"); err != nil {
		return fmt.Errorf("`go` isn't on your PATH — the drift CLI installs with `go install`, " +
			"so you need Go to upgrade it. Get it from https://go.dev/dl and try again")
	}

	// Resolve the version label that goes after the "@" in `go install`.
	label, err := resolveLabel(current, strings.TrimSpace(requested))
	if err != nil {
		return err
	}
	if label == "" {
		return nil // already up to date (resolveLabel printed the note)
	}

	spec := common.CLIModulePath + "@" + label
	fmt.Printf("Upgrading the drift CLI → %s\n", label)
	spinner := common.StartSpinner("  ", "go install "+spec)

	// #nosec G204 -- the module path is a constant; `label` is a version tag,
	// and exec.Command passes args without a shell, so there's no injection
	// surface (a bad label just makes `go install` fail with a clear error).
	c := exec.Command("go", "install", spec)
	c.Env = os.Environ()
	out, runErr := c.CombinedOutput()
	spinner.Stop()
	if runErr != nil {
		detail := strings.TrimSpace(string(out))
		if detail == "" {
			detail = runErr.Error()
		}
		return fmt.Errorf("upgrade failed:\n%s", detail)
	}

	fmt.Printf("%s drift CLI installed at %s\n", common.Check(), label)
	if current != "" && current != label {
		fmt.Printf("  %s → %s\n", current, label)
	}
	if target := goInstallTarget(); target != "" {
		fmt.Printf("  binary: %s\n", target)
		if onPath, perr := exec.LookPath("drift"); perr == nil && onPath != target {
			fmt.Printf("  %s\n", common.Hint("the `drift` first on your PATH is "+onPath+
				" — ensure "+filepath.Dir(target)+" comes first to pick this up"))
		}
	}
	fmt.Printf("  Run `drift --version` in a new shell to confirm.\n")
	return nil
}

// resolveLabel turns the user's request into the "@<label>" go install uses.
// Returns ("", nil) — with a printed note — when "latest" is requested and we're
// already on it. Returns the literal label otherwise.
func resolveLabel(current, requested string) (string, error) {
	if requested != "" && !strings.EqualFold(requested, "latest") {
		return normalizeVersion(requested), nil
	}

	// "latest" (explicit or default): resolve the tag so we can show it and skip
	// a needless reinstall. If GitHub is unreachable, fall back to letting the go
	// toolchain resolve @latest itself.
	rel, err := common.FetchLatestCLIRelease()
	if err != nil {
		return "latest", nil
	}
	if current != "" && common.CompareVersions(current, rel.Tag) >= 0 {
		fmt.Printf("%s You're already on the latest release (%s).\n", common.Check(), current)
		fmt.Printf("  To reinstall anyway: `drift upgrade %s`.\n", rel.Tag)
		return "", nil
	}
	return rel.Tag, nil
}

// normalizeVersion accepts "1.8.1" or "v1.8.1" (→ "v1.8.1") and passes any other
// ref (a branch or commit) through untouched.
func normalizeVersion(v string) string {
	if v == "" {
		return "latest"
	}
	if v[0] >= '0' && v[0] <= '9' {
		return "v" + v
	}
	return v
}

// goInstallTarget reports where `go install` puts binaries (GOBIN, else
// GOPATH/bin), so we can tell the user which `drift` was updated.
func goInstallTarget() string {
	if out, err := exec.Command("go", "env", "GOBIN").Output(); err == nil { // #nosec G204 -- constant args
		if p := strings.TrimSpace(string(out)); p != "" {
			return filepath.Join(p, "drift")
		}
	}
	if out, err := exec.Command("go", "env", "GOPATH").Output(); err == nil { // #nosec G204 -- constant args
		if p := strings.TrimSpace(string(out)); p != "" {
			return filepath.Join(p, "bin", "drift")
		}
	}
	return ""
}
