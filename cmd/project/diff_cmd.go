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
	var (
		envName         string
		secretOverrides []string
		noEnvFile       bool
	)
	cmd := &cobra.Command{
		Use:   "diff [environment]",
		Short: "Show the divergence between the local Driftfile and the live slice",
		Long: `Print the same diff that 'drift project deploy --plan' produces:
resource counts, envelope shape, and the cost change a deploy would apply.

If the Driftfile declares environments, pass one to diff that environment's
merged shape against its slice. Never applies. Exits non-zero if the manifest
would abort the deploy (slice oversized vs declared shape).`,
		Example: "  drift project diff\n  drift project diff staging",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manifestPath, err := filepath.Abs(filepath.Join(".", driftfileName))
			if err != nil {
				return fmt.Errorf("resolve manifest path: %w", err)
			}
			if _, err := os.Stat(manifestPath); err != nil {
				return fmt.Errorf("no Driftfile in the current directory (looked for %s)", manifestPath)
			}

			positionalEnv := ""
			if len(args) == 1 {
				positionalEnv = args[0]
			}
			selectedEnv := positionalEnv
			if selectedEnv == "" {
				selectedEnv = envName
			}

			// Same variable origin hierarchy as deploy (see env.go): terminal
			// env > --secret/--env overrides > .env.<env> > .env.
			overrides := secretOverrides
			if selectedEnv != "" {
				overrides = append([]string{"ENV=" + selectedEnv}, overrides...)
			}
			vars, err := applyVariableSources(filepath.Dir(manifestPath), overrides, !noEnvFile, selectedEnv)
			if err != nil {
				return err
			}
			vars.report()

			m, err := ParseDriftfile(manifestPath)
			if err != nil {
				return err
			}
			if _, err := m.SelectEnvironment(selectedEnv, positionalEnv != ""); err != nil {
				return err
			}
			return runPlan(m)
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Environment to diff (same as the positional argument); also sets ${ENV}")
	cmd.Flags().StringArrayVar(&secretOverrides, "secret", nil, "Override a variable for ${VAR}/$ENVREF resolution: KEY=value (repeatable). Yields to a variable already set in the environment; beats the .env file.")
	cmd.Flags().BoolVar(&noEnvFile, "no-env-file", false, "Do not read the .env / .env.<env> file sitting next to the Driftfile")
	return cmd
}
