package project

// diff_cmd.go exposes `drift project diff` — the same divergence
// summary `drift project deploy --plan` produces, surfaced as its own
// verb. The motivation (per the DevEx roadmap): "diff" is a word every
// developer reaches for *while debugging*, not as a flag on apply, and
// it's a one-word answer to "what's actually deployed?"
//
// Behaviour mirrors --plan exactly: never applies, exits non-zero when
// the manifest would abort the deploy (slice oversized vs declared
// shape).

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"
)

func getDiffCmd() *cobra.Command {
	var envName string
	cmd := &cobra.Command{
		Use:   "diff",
		Short: "Show the divergence between the local Driftfile and the live slice",
		Long: `Print the same diff that 'drift project deploy --plan' produces:
resource counts, envelope shape, and the cost change a deploy would apply.

Never applies. Exits non-zero if the manifest would abort the deploy
(slice oversized vs declared shape).`,
		Example: "  drift project diff\n  drift project diff --env=staging",
		Args:    cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if envName != "" {
				if err := os.Setenv("ENV", envName); err != nil {
					return fmt.Errorf("set ENV=%s: %w", envName, err)
				}
			}
			manifestPath, err := filepath.Abs(filepath.Join(".", driftfileName))
			if err != nil {
				return fmt.Errorf("resolve manifest path: %w", err)
			}
			if _, err := os.Stat(manifestPath); err != nil {
				return fmt.Errorf("no Driftfile in the current directory (looked for %s)", manifestPath)
			}
			m, err := ParseDriftfile(manifestPath)
			if err != nil {
				return err
			}
			return runPlan(m)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Sets ENV before parsing the Driftfile, so ${ENV} substitutes to this value")
	return cmd
}
