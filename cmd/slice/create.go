package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/ondrift/cli/common"

	"github.com/spf13/cobra"
)

// getCreateCmd builds `drift slice create [name]`.
//
// Default flow: opens the configurator in the browser. The user
// types the slice name into the form, configures resources, reviews
// the price, and submits — the configurator forwards the create call
// to api on the user's behalf and the CLI polls for the result.
// Passing a positional name pre-fills the form's name field.
//
// The configurator is the single source of truth for slice envelope
// configuration. There is no terminal-based configurator; resource
// configuration always happens in the browser.
//
// Headless flow (--free / --headless): skips the browser entirely
// and creates a free Hacker slice directly. The name is required
// in headless mode because there is no form to collect it from.
// This path is the only one that works in CI, scripts, and SSH
// sessions.
func getCreateCmd() *cobra.Command {
	var headless bool
	var free bool

	cmd := &cobra.Command{
		Use:   "create [name]",
		Short: "Create a new slice (opens the configurator in your browser)",
		Example: "  drift slice create my-slice            # browser configurator\n" +
			"  drift slice create my-slice --free      # free Hacker slice, no browser\n" +
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

			// Default: browser configurator.
			result, err := runBrowserHandoff("create slice", name, modeCreate, nil)
			if err != nil {
				return err
			}
			activeName := sliceNameFromResult(result, name)
			if activeName != "" {
				if err := common.SaveActiveSlice(activeName); err != nil {
					fmt.Println("Warning: couldn't mark the new slice as active —", err)
				}
			}
			printSliceSummary("created and set as active", result)
			return nil
		},
	}

	cmd.Flags().BoolVar(&free, "free", false, "Create a free Hacker slice without opening the configurator")
	cmd.Flags().BoolVar(&headless, "headless", false, "Alias for --free (CI/scripts)")
	_ = cmd.Flags().MarkHidden("headless")
	return cmd
}

// sliceNameFromResult extracts the "name" field out of the Slice
// document returned by the configurator. The fallback is the name
// the user passed on the command line (if any), so the active-slice
// marker is set even when the result body is missing or unparseable.
func sliceNameFromResult(raw json.RawMessage, fallback string) string {
	var s struct {
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &s); err == nil && s.Name != "" {
		return s.Name
	}
	return fallback
}

// createHeadless posts directly to api/ops/slice/create with the free
// tier. Kept for non-interactive use (CI, scripts, SSH sessions). For
// configured (paid) slices, use the browser flow.
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
