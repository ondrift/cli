package slice

import (
	"encoding/json"
	"fmt"
	"net/http"
	"path/filepath"

	project "github.com/ondrift/cli/v2/cmd/project"
	"github.com/ondrift/cli/v2/common"

	"github.com/spf13/cobra"
)

// getResizeCmd builds `drift slice resize`.
//
// Two modes:
//
//  1. **Browser mode** (default, no args + no flags). Opens the
//     configurator in the browser with the slice's current config
//     pre-loaded. Same UX the platform has had since v0.
//
//  2. **Driftfile mode** (`--from <path>`). Reads a Driftfile,
//     diffs it against the live slice, and applies the divergence
//     directly via /ops/slice/resize. Unlike `drift project deploy`
//     (which aborts on shrink), this command is the named verb for
//     shrinking — it requires `--allow-destructive` to actually
//     apply any field that goes down.
//
// The Driftfile mode is the load-bearing answer to the spec's
// reconcile rule #3: "deploy never shrinks." Shrinks live here, with
// a separate flag, separate prompt, and separate code path.
func getResizeCmd() *cobra.Command {
	var (
		fromPath         string
		allowDestructive bool
		autoYes          bool
		billingMonths    int
	)

	cmd := &cobra.Command{
		Use:   "resize [name]",
		Short: "Resize a slice — defaults to the active slice; browser by default, or --from a Driftfile",
		Example: `  drift slice resize
  drift slice resize my-slice
  drift slice resize --from Driftfile
  drift slice resize --from Driftfile --allow-destructive`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if fromPath != "" {
				if len(args) > 0 {
					return fmt.Errorf("cannot pass <name> when --from is set; the slice name comes from the Driftfile")
				}
				return resizeFromDriftfile(fromPath, allowDestructive, autoYes, billingMonths)
			}

			// No name given → resize the currently active slice (the one
			// `drift slice use` selected), matching every other slice subcommand.
			var name string
			if len(args) == 1 {
				name = args[0]
			} else {
				active, err := common.RequireActiveSlice()
				if err != nil {
					return fmt.Errorf("%w, or pass a slice name / use --from <Driftfile>", err)
				}
				name = active
			}
			existing, err := fetchSliceConfig(name)
			if err != nil {
				return err
			}
			result, err := runBrowserHandoff("resize slice", name, modeResize, existing)
			if err != nil {
				return err
			}
			printSliceSummary("resized", result)
			return nil
		},
	}

	cmd.Flags().StringVar(&fromPath, "from", "", "Read the target shape from a Driftfile (default: open the browser configurator)")
	cmd.Flags().BoolVar(&allowDestructive, "allow-destructive", false, "Authorise shrinks that lower a resource limit. Required for any non-zero shrink.")
	cmd.Flags().BoolVarP(&autoYes, "yes", "y", false, "Auto-confirm the cost prompt (for CI)")
	cmd.Flags().IntVar(&billingMonths, "billing-period-months", 1, "Billing period in months for the resize")
	return cmd
}

// resizeFromDriftfile applies a Driftfile's declared shape to a live
// slice, including shrinks when --allow-destructive is set.
//
// The destructive flag is checked at the CLI level rather than the
// manifest level on purpose: a teammate reading a Driftfile cannot
// see "this run will shrink production." Forcing the flag at the
// command line means destructive intent is unambiguous on the
// terminal that ran it, and CI cannot accidentally shrink without
// it being visible in the workflow file.
func resizeFromDriftfile(path string, allowDestructive, autoYes bool, billingMonths int) error {
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

	live, err := project.FetchLiveSlice(m.Slice.Name)
	if err != nil {
		return fmt.Errorf("fetch slice: %w", err)
	}
	if live == nil {
		return fmt.Errorf("slice %q does not exist; create it first with `drift project deploy`", m.Slice.Name)
	}

	d := project.Diff(m.Slice.Name, manifestCfg, &live.Config, live.MonthlyCostCents, wantedCost)
	d.WantedItems = wantedItems

	switch d.Verdict {
	case project.VerdictMatch:
		fmt.Printf("Slice %q already matches the Driftfile. Nothing to do.\n", m.Slice.Name)
		return nil

	case project.VerdictGrow:
		// Pure grow — same as `project deploy` would do.
		fmt.Println()
		fmt.Println(project.RenderDiff(d))
		if !confirmYesNo(autoYes, "Apply?") {
			return fmt.Errorf("aborted by user")
		}
		return project.ResizeSlice(m.Slice.Name, manifestCfg, billingMonths)

	case project.VerdictAbort:
		// VerdictAbort here means the manifest is *smaller* in some
		// dimension. That's exactly what `slice resize` exists to
		// handle — but ONLY with --allow-destructive.
		if !allowDestructive {
			fmt.Println()
			fmt.Printf("✘ Refusing to shrink slice %q without --allow-destructive.\n\n", m.Slice.Name)
			fmt.Printf("  The Driftfile declares smaller limits than the slice currently has:\n")
			for _, s := range d.Shrinks {
				fmt.Printf("    %s   %s → %s\n", s.Path,
					formatDelta(s.Live, s),
					formatDelta(s.Wanted, s))
			}
			fmt.Println()
			fmt.Println("  Re-run with --allow-destructive if you intend to lower these limits.")
			fmt.Println("  The platform-side resize endpoint will still refuse to shrink below")
			fmt.Println("  current usage — your data isn't at risk, but quotas can drop.")
			return fmt.Errorf("destructive shrink refused")
		}

		fmt.Println()
		fmt.Printf("⚠ Shrinking slice %q (--allow-destructive set):\n\n", m.Slice.Name)
		if len(d.Grows) > 0 {
			fmt.Println("  Grows:")
			for _, g := range d.Grows {
				fmt.Printf("    %s   %s → %s\n", g.Path,
					formatDelta(g.Live, g),
					formatDelta(g.Wanted, g))
			}
			fmt.Println()
		}
		fmt.Println("  Shrinks:")
		for _, s := range d.Shrinks {
			fmt.Printf("    %s   %s → %s\n", s.Path,
				formatDelta(s.Live, s),
				formatDelta(s.Wanted, s))
		}
		fmt.Println()
		fmt.Printf("  Cost: €%s/month (was €%s/month).\n",
			centsToEuros(wantedCost), centsToEuros(live.MonthlyCostCents))

		if !confirmYesNo(autoYes, "Apply destructive resize?") {
			return fmt.Errorf("aborted by user")
		}
		return project.ResizeSlice(m.Slice.Name, manifestCfg, billingMonths)
	}

	return fmt.Errorf("unexpected verdict: %s", d.Verdict)
}

// formatDelta renders a single FieldDelta value with the right unit.
// Mirror of project.formatValue, exposed here for resize's UX.
func formatDelta(n int, f project.FieldDelta) string {
	if n == 0 {
		return "0"
	}
	switch {
	case f.IsBytes:
		return fmt.Sprintf("%d bytes", n)
	case f.IsTime:
		return fmt.Sprintf("%ds", n)
	case f.IsHours:
		return fmt.Sprintf("%dh", n)
	case f.IsDays:
		return fmt.Sprintf("%dd", n)
	}
	return fmt.Sprintf("%d", n)
}

func centsToEuros(cents int) string {
	if cents%100 == 0 {
		return fmt.Sprintf("%d", cents/100)
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100)
}

func confirmYesNo(autoYes bool, prompt string) bool {
	if autoYes {
		fmt.Printf("  %s [y/N] (auto-yes)\n", prompt)
		return true
	}
	fmt.Printf("  %s [y/N] ", prompt)
	var ans string
	_, _ = fmt.Scanln(&ans)
	return ans == "y" || ans == "Y" || ans == "yes" || ans == "YES"
}

// fetchSliceConfig pulls the user's current SliceConfig from api so the
// configurator form can pre-populate. We return the JSON-decoded value as
// a generic any so the handoff helper can re-encode it without the CLI
// having to import drift-common/models. The shape is intentionally
// passthrough — the CLI never inspects the config, it only forwards it.
func fetchSliceConfig(name string) (any, error) {
	resp, err := common.DoRequest(
		http.MethodGet,
		common.APIBaseURL+"/ops/slice/get?name="+name,
		nil,
	)
	if err != nil {
		return nil, common.TransportError("resize slice", err)
	}
	defer resp.Body.Close()

	body, err := common.CheckResponse(resp, "resize slice")
	if err != nil {
		return nil, err
	}

	// /ops/slice/get returns the full Slice document; the configurator
	// only needs the embedded "config" subobject. Pull it out so the
	// handoff payload matches the configurator's handoffRequest.Existing
	// field shape.
	var slice struct {
		Config any `json:"config"`
	}
	if err := json.Unmarshal(body, &slice); err != nil {
		return nil, fmt.Errorf("Couldn't resize slice: get response wasn't valid JSON (%w)", err)
	}
	if slice.Config == nil {
		return nil, fmt.Errorf("Couldn't resize slice: server returned no config for slice %q", name)
	}
	return slice.Config, nil
}
