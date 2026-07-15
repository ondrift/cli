package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

// getCreateCmd builds `drift slice create [name]`.
//
// Default flow: launches the drift dashboard (TUI) straight into
// create-slice mode. The user configures resources there, reviews
// the live price, and submits — the dashboard posts the create call
// to api directly. Passing a positional name pre-fills the form's
// name field. (This used to hand off to a browser-based configurator
// service; that service has been retired in favor of the dashboard,
// which already had a full equivalent create-slice form — see
// cmd/portal/configform.go.)
//
// Headless flow (--free / --headless): skips the dashboard entirely
// and creates a free Hacker slice directly. The name is required
// in headless mode because there is no form to collect it from.
// This path is the only one that works in CI, scripts, and SSH
// sessions.
func getCreateCmd() *cobra.Command {
	var headless bool
	var free bool

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new slice (opens the dashboard's create-slice view)",
		Example: "  drift slice create my-slice            # opens the dashboard\n" +
			"  drift slice create my-slice --free      # free Hacker slice, no dashboard\n" +
			"  drift slice create my-slice --headless  # alias for --free (CI/scripts)",
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			var name string
			if len(args) == 1 {
				name = args[0]
			}

			// --headless is an alias for --free (backward compat).
			if headless {
				free = true
			}

			// Free tier: create directly, no configurator.
			if free {
				if name == "" {
					return fmt.Errorf("--free requires a slice name argument")
				}
				return createHeadless(name)
			}

			// Default: launch the dashboard straight into create-slice mode.
			// It sets the new slice active itself on a successful submit
			// (see cmd/portal/configform.go's submitForm), so there's
			// nothing left for this command to do afterward.
			return openPortalCreate(name)
		},
	}

	cmd.Flags().BoolVar(&free, "free", false, "Create a free Hacker slice without opening the dashboard")
	cmd.Flags().BoolVar(&headless, "headless", false, "Alias for --free (CI/scripts)")
	_ = cmd.Flags().MarkHidden("headless")
	return cmd
}

// openPortalCreate launches the drift dashboard (our own binary, so it's
// always the build actually installed) straight into create-slice mode,
// pre-filled with name. A re-exec rather than a direct call because
// cmd/portal already imports cmd/slice (for SliceEntry/FetchSlices/
// TierLabel) — importing cmd/portal back from here would be a cycle.
// Mirrors the portal package's own re-exec pattern for suspend/resume
// (see cmd/portal/newslice.go's driftExe + suspendAndRun).
func openPortalCreate(name string) error {
	exe, err := os.Executable()
	if err != nil {
		exe = "drift"
	}
	cmd := exec.Command(exe, "portal", "--create", name) // #nosec G204 -- our own binary, fixed args
	cmd.Stdin, cmd.Stdout, cmd.Stderr = os.Stdin, os.Stdout, os.Stderr
	return cmd.Run()
}

// createHeadless posts directly to api/ops/slice/create with the free
// tier. Kept for non-interactive use (CI, scripts, SSH sessions). For
// configured (paid) slices, use the dashboard flow (the default, no
// --free/--headless).
func createHeadless(name string) error {
	body, _ := json.Marshal(map[string]string{
		"name": name,
		"tier": "hacker",
	})

	resp, err := common.DoJSONRequest(
		http.MethodPost,
		common.APIBaseURL+"/ops/slice/create",
		bytes.NewBuffer(body),
	)
	if err != nil {
		return common.TransportError("create slice", err)
	}
	defer resp.Body.Close()

	if _, err := common.CheckResponse(resp, "create slice"); err != nil {
		return err
	}

	if err := common.SaveActiveSlice(name); err != nil {
		fmt.Println("Warning: couldn't mark the new slice as active —", err)
	}
	fmt.Printf("Slice '%s' created and set as active.\n", name)
	return nil
}
