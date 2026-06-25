package azure

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ondrift/cli/common"
)

func getApplyCmd() *cobra.Command {
	var in string
	var acceptRefusals bool
	cmd := &cobra.Command{
		Use:   "apply",
		Short: "Deploy a drift_workspace/ to your active Drift slice",
		Long: "The one command in this tool that touches Drift. It runs `drift project deploy`\n" +
			"from the workspace `transform` produced — deploying to your ACTIVE slice over the\n" +
			"authenticated API. Review REPORT.md and acknowledge the REFUSED list first; the\n" +
			"refused workloads stay on Azure, by design.",
		Example: "  drift migrate azure apply -i ./drift_workspace --accept-refusals",
		RunE: func(_ *cobra.Command, _ []string) error {
			if err := applyGate(in, acceptRefusals); err != nil {
				return err
			}
			fmt.Printf("%s  deploying %s to your active slice via `drift project deploy`…\n\n",
				common.Check(), common.Highlight(in))
			return runProjectDeploy(in)
		},
	}
	cmd.Flags().StringVarP(&in, "in", "i", "./drift_workspace", "Workspace produced by `transform`")
	cmd.Flags().BoolVar(&acceptRefusals, "accept-refusals", false, "Acknowledge that REFUSED items stay on Azure and deploy the rest")
	return cmd
}

// applyGate is the pure, testable precondition check: the workspace must exist
// and, if anything was refused, the operator must explicitly accept that those
// workloads stay on Azure. Refusing to deploy silently around a refusal is the
// integrity contract — the operator decides, on the record.
func applyGate(dir string, acceptRefusals bool) error {
	if _, err := os.Stat(filepath.Join(dir, "Driftfile")); err != nil {
		return fmt.Errorf("no Driftfile in %s — run `drift migrate azure transform` first", dir)
	}
	refused := migrationRefusedCount(dir)
	if refused > 0 && !acceptRefusals {
		return fmt.Errorf("%d resource(s) were refused and will stay on Azure (see %s).\n"+
			"Re-run with --accept-refusals to deploy what fits and leave the rest behind",
			refused, filepath.Join(dir, "REFUSED.md"))
	}
	return nil
}

// migrationRefusedCount reads the refusal count transform recorded. Missing or
// unreadable → 0 (the gate then only enforces the Driftfile's presence).
func migrationRefusedCount(dir string) int {
	b, err := os.ReadFile(filepath.Join(dir, "_migration.json"))
	if err != nil {
		return 0
	}
	var s struct {
		Refused int `json:"refused"`
	}
	_ = json.Unmarshal(b, &s)
	return s.Refused
}

// runProjectDeploy shells out to `drift project deploy` from the workspace,
// having loaded .env.migrate so the Driftfile's $ENV secret refs resolve. This
// is the deliberate MVP shape (shell-out, not in-process) — clean isolation,
// and the migration pipeline borrows no privileges the deploy doesn't already
// have.
func runProjectDeploy(dir string) error {
	loadDotEnv(filepath.Join(dir, ".env.migrate"))
	bin, err := exec.LookPath("drift")
	if err != nil {
		return fmt.Errorf("`drift` is not on PATH — install the CLI, then run `drift project deploy` from %s", dir)
	}
	cmd := exec.Command(bin, "project", "deploy")
	cmd.Dir = dir
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// loadDotEnv loads KEY=VALUE lines into the environment (never clobbering a var
// the operator already set).
func loadDotEnv(path string) {
	b, err := os.ReadFile(path)
	if err != nil {
		return
	}
	for _, line := range strings.Split(string(b), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			continue
		}
		k = strings.TrimSpace(k)
		if _, exists := os.LookupEnv(k); !exists {
			_ = os.Setenv(k, v)
		}
	}
}
