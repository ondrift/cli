package project

// test.go — `drift project test`: start the project locally (the same
// mechanism `drift project run` uses), run the Driftfile's declared
// `tests.e2e` commands against it, and always tear the local instance down
// afterward. The "test before you ship" half of the local-first promise —
// no account, no cloud, e2e against the real running app before `drift
// project deploy` ever touches Drift.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

func getTestCmd() *cobra.Command {
	var (
		envName   string
		hostPort  int
		noEnvFile bool
	)
	cmd := &cobra.Command{
		Use:   "test [environment]",
		Short: "Run the project locally and run its declared e2e tests against it",
		Long: `Build and run the project locally (same as 'drift project run'), wait for
it to be healthy, then run every command declared under the Driftfile's
tests.e2e — same shape and semantics as hooks.pre_deploy/post_deploy. The
instance's local URL rides in as DRIFT_TEST_URL, so a test command knows
where to point (the port is picked at runtime, never fixed).

The local instance is always torn down afterward, whether the tests pass,
fail, or error out.`,
		Example: "  drift project test\n  drift project test staging",
		Args:    cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			manifestPath, err := filepath.Abs(filepath.Join(".", driftfileName))
			if err != nil {
				return err
			}
			if _, err := os.Stat(manifestPath); err != nil {
				return fmt.Errorf("no Driftfile in the current directory (looked for %s)", manifestPath)
			}
			projectDir := filepath.Dir(manifestPath)

			tests, err := ParseTests(manifestPath)
			if err != nil {
				return err
			}
			if len(tests.E2E) == 0 {
				return fmt.Errorf("no tests declared — add a `tests:` block to your Driftfile:\n\n  tests:\n    e2e:\n      - npx playwright test\n")
			}

			selectedEnv := envName
			if len(args) == 1 {
				selectedEnv = args[0]
			}
			// persist=false: a test run's data shouldn't tether to a volume
			// that outlives the container this command tears down below.
			app, container, url, err := startLocal(selectedEnv, len(args) == 1, hostPort, false, noEnvFile)
			if err != nil {
				return err
			}
			defer func() {
				fmt.Printf("\n  %s tearing down %s…\n", common.Hint("·"), common.Highlight(app))
				if out, rmErr := exec.Command("docker", "rm", "-f", container).CombinedOutput(); rmErr != nil {
					fmt.Fprintf(os.Stderr, "  %s could not remove container %s: %s\n", common.Cross(), container, string(out))
				}
			}()

			fmt.Printf("\n  %s %s running (container %s)\n", common.Check(), common.Highlight(app), container)
			fmt.Printf("     → %s\n", common.Highlight(url))

			// DRIFT_TEST_URL rides to every test command via the process
			// environment — runHooks's exec.Command inherits it the same way
			// it inherits the rest of os.Environ(), no plumbing needed.
			if err := os.Setenv("DRIFT_TEST_URL", url); err != nil {
				return err
			}
			if err := runHooks("tests.e2e", tests.E2E, projectDir); err != nil {
				return err
			}
			fmt.Printf("\n  %s all tests passed\n\n", common.Check())
			return nil
		},
	}
	cmd.Flags().StringVar(&envName, "env", "", "Environment to run (same as the positional argument)")
	cmd.Flags().IntVar(&hostPort, "port", 0, "Host port to map Canvas (:8002) to (default: 8002, or the next free port)")
	cmd.Flags().BoolVar(&noEnvFile, "no-env-file", false, "Do not read the .env / .env.<env> file")
	return cmd
}
