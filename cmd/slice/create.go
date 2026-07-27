package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"

	project "github.com/ondrift/cli/v2/cmd/project"
	"github.com/ondrift/cli/v2/common"

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
//
// Driftfile flow (--from <path>): the slice is born at the shape the
// manifest declares, which is what `drift project deploy` would have
// provisioned had the slice not existed. Without this, the two-command
// path (`slice create` then `project deploy`) starts from a shape nobody
// declared — the fixed free preset, which is larger than any honest first
// Driftfile — while the one-command path starts from the manifest. Same
// project, two different slices, depending only on which command was run
// first. See HQ CLI-STANDARDUSAGE-T17KKN.
func getCreateCmd() *cobra.Command {
	var (
		headless      bool
		free          bool
		fromPath      string
		autoYes       bool
		billingMonths int
	)

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new slice (opens the dashboard's create-slice view, or --from a Driftfile)",
		Example: "  drift slice create my-slice            # opens the dashboard\n" +
			"  drift slice create my-slice --free      # free Hacker slice, no dashboard\n" +
			"  drift slice create --from Driftfile     # born at the manifest's shape\n" +
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

			if fromPath != "" {
				if name != "" {
					return fmt.Errorf("cannot pass <name> when --from is set; the slice name comes from the Driftfile")
				}
				if free {
					return fmt.Errorf("cannot combine --from with --free; the Driftfile's declared shape decides whether the slice is free")
				}
				return createFromDriftfile(fromPath, autoYes, billingMonths)
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
	cmd.Flags().StringVar(&fromPath, "from", "", "Create the slice at the shape a Driftfile declares (name comes from the Driftfile)")
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Auto-confirm the cost prompt (for CI)")
	cmd.Flags().IntVar(&billingMonths, "billing-period-months", 1, "Billing period in months, for a configured (non-free) slice")
	_ = cmd.Flags().MarkHidden("headless")
	return cmd
}

// createFromDriftfile provisions a slice at the shape its Driftfile
// declares. It is the create-time twin of `slice resize --from Driftfile`
// and shares that command's whole pipeline — parse, translate, price,
// classify — so the cost the user confirms here is computed by the same
// server-side pricing call that `project deploy` would have used.
//
// There is deliberately no shape decision of its own: the tier follows the
// price exactly as reconcileSlice's create path does (free when the
// manifest costs nothing, configured otherwise), so `slice create --from X`
// and a first `project deploy` against the same manifest produce the same
// slice.
func createFromDriftfile(path string, autoYes bool, billingMonths int) error {
	abs, err := filepath.Abs(path)
	if err != nil {
		return fmt.Errorf("resolve %s: %w", path, err)
	}

	m, err := project.ParseDriftfile(abs)
	if err != nil {
		return err
	}

	manifestCfg, err := project.ManifestToSliceConfig(m)
	if err != nil {
		return err
	}
	wantedCost, wantedItems, err := project.PriceConfig(manifestCfg)
	if err != nil {
		return fmt.Errorf("price target config: %w", err)
	}

	// Refuse rather than silently no-op or resize: this command creates.
	live, err := project.FetchLiveSlice(m.Slice.Name)
	if err != nil {
		return fmt.Errorf("fetch slice: %w", err)
	}
	if live != nil {
		return fmt.Errorf("slice %q already exists — use `drift project deploy` to deploy into it, or `drift slice resize --from %s` to change its shape", m.Slice.Name, path)
	}

	d := project.Diff(m.Slice.Name, manifestCfg, nil, "", 0, wantedCost)
	d.WantedItems = wantedItems

	fmt.Println()
	fmt.Println(project.RenderDiff(d))
	if !confirmYesNo(autoYes, "Apply?") {
		return fmt.Errorf("aborted by user")
	}

	// Tier follows price — identical rule to cmd/project's create path.
	tier := "custom"
	if wantedCost == 0 {
		tier = project.TierHacker
	}

	fmt.Printf("\n  Creating slice %q...\n", m.Slice.Name)
	if err := project.CreateSlice(m.Slice.Name, tier, manifestCfg, billingMonths); err != nil {
		return err
	}
	// Wait for provisioning, exactly as `project deploy`'s own create path
	// does. The whole point of this command is that a `project deploy` follows
	// it, and without the wait that deploy races the slice coming up and fails
	// with "runner unreachable" — a platform-fault message for what is really
	// just impatience. Observed on alpha before this was added.
	if err := project.WaitForSliceReady(m.Slice.Name); err != nil {
		return fmt.Errorf("slice %q was created but did not become ready: %w", m.Slice.Name, err)
	}

	if err := common.SaveActiveSlice(m.Slice.Name); err != nil {
		fmt.Println("Warning: couldn't mark the new slice as active —", err)
	}
	fmt.Printf("Slice '%s' created and set as active.\n", m.Slice.Name)
	return nil
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
