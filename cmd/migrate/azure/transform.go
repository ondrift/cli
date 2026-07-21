package azure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ondrift/cli/cmd/project"
	"github.com/ondrift/cli/common"
)

func getTransformCmd() *cobra.Command {
	var in, out string
	cmd := &cobra.Command{
		Use:   "transform",
		Short: "Turn an azure_export/ into a deployable drift_workspace/ (offline)",
		Long: "Reads a snapshot and emits a Drift project: a Driftfile, a Tier-A scaffold per\n" +
			"movable function (correct @atomic annotation + Drift return; original body preserved\n" +
			"verbatim with TODOs), Cosmos data remapped id→_id, secrets as $ENV refs, and\n" +
			"REFUSED.md / REPORT.md / PLAN.md. The generated Driftfile is validated before it's\n" +
			"written. Entirely offline — no Azure, no Drift contact.",
		Example: "  drift migrate azure transform -i ./azure_export -o ./drift_workspace",
		RunE: func(_ *cobra.Command, _ []string) error {
			m, err := readManifest(in)
			if err != nil {
				return fmt.Errorf("reading %s: %w (run `snapshot` first)", in, err)
			}
			res, err := runTransform(in, out, m)
			if err != nil {
				return err
			}
			fmt.Printf("\n%s  %d scaffold(s) · %d collection(s) · %d refused → %s\n",
				common.Check(), res.scaffolds, len(m.Collections), len(res.refusals), common.Highlight(out))
			fmt.Printf("  review %s, then: %s\n", common.BoldText("REPORT.md"), common.BoldText("cd "+out+" && drift project deploy"))
			return nil
		},
	}
	cmd.Flags().StringVarP(&in, "in", "i", "./azure_export", "Input snapshot directory")
	cmd.Flags().StringVarP(&out, "out", "o", "./drift_workspace", "Output workspace directory")
	return cmd
}

type transformResult struct {
	scaffolds int
	refusals  []Refusal
}

type driftFn struct{ slug, cron string }
type driftColl struct {
	name, seed string
	sizeMB     int // declared backbone.nosql[].size, computed from the migrated data (see collSizeMB)
}

// collSizeMB turns a migrated collection's actual seed byte count into a
// declared `size` (now mandatory on every backbone.nosql[] entry) with real
// headroom for post-migration growth: round up to the next whole MiB, then
// double it, with a 5MB floor so even a near-empty collection gets enough
// room for its first few real writes. This is a starting point, not a
// considered capacity plan — buildDriftfile's generated Driftfile already
// carries a "review before deploy" banner, and the caller adds a per-
// collection TODO pointing at exactly that.
func collSizeMB(dataBytes int) int {
	mb := max((dataBytes+1024*1024-1)/(1024*1024), 1)
	return max(mb*2, 5)
}

// runTransform is the offline Stage-2 engine: azure_export/ → drift_workspace/.
func runTransform(inDir, outDir string, m Manifest) (transformResult, error) {
	res := transformResult{refusals: append([]Refusal(nil), m.Refusals...)}
	var todos []string

	var fns []driftFn
	for _, fn := range m.Functions {
		// Timer triggers: Drift scheduling is wired from a `# drift:schedule`
		// source comment on an http handler, NOT an `@atomic cron=` trigger (the
		// interpreted deploy path rejects cron=). Until transform emits that
		// pattern and it's verified to fire end-to-end, a timer is refused rather
		// than mapped — a Driftfile `cron:` alone would bill for a job that never
		// runs. The handler source is still captured in the snapshot.
		if fn.Trigger.Type == "cron" {
			res.refusals = append(res.refusals, Refusal{
				RefuseTimerTrigger, fn.Name,
				"timer trigger (schedule \"" + fn.Trigger.Schedule + "\")",
				"Hand-port: add `# drift:schedule " + fn.Trigger.Schedule + "` above an http=post handler (the source is in the snapshot), or keep it on Azure.",
			})
			continue
		}
		if fn.Runtime != "python" {
			res.refusals = append(res.refusals, Refusal{
				RefuseUnknownBinding, fn.Name,
				fn.Runtime + " scaffold is not in v1 (Python only)",
				"Source is captured in the snapshot; port it by hand, or wait for the " + fn.Runtime + " scaffolder.",
			})
			continue
		}
		orig, err := os.ReadFile(filepath.Join(inDir, filepath.FromSlash(fn.SourcePath)))
		if err != nil {
			res.refusals = append(res.refusals, Refusal{RefuseSourceBlocked, fn.Name, "source missing from snapshot", "Re-run snapshot."})
			continue
		}
		slug := slugify(fn.Name)
		if err := writeWS(outDir, "atomic/"+slug+"/"+slug+".py", pythonScaffold(fn, orig), 0o644); err != nil {
			return res, err
		}
		fns = append(fns, driftFn{slug, fn.Trigger.Schedule})
		res.scaffolds++
		todos = append(todos, fmt.Sprintf("%s: scaffolded — replace the commented Azure body with drift.* SDK calls and a real return", slug))
		for _, b := range fn.Bindings {
			todos = append(todos, fmt.Sprintf("%s: wire Azure binding → Drift SDK — %s", slug, b))
		}
	}

	var colls []driftColl
	for _, coll := range m.Collections {
		data, err := os.ReadFile(filepath.Join(inDir, filepath.FromSlash(coll.DataPath)))
		if err != nil {
			res.refusals = append(res.refusals, Refusal{RefuseSourceBlocked, coll.Name, "collection data missing from snapshot", "Re-run snapshot."})
			continue
		}
		transformed, lossy := cosmosIDToMongo(data)
		seed := "backbone/nosql/" + coll.Name + ".jsonl"
		if err := writeWS(outDir, seed, transformed, 0o644); err != nil {
			return res, err
		}
		sizeMB := collSizeMB(len(transformed))
		colls = append(colls, driftColl{coll.Name, seed, sizeMB})
		todos = append(todos, fmt.Sprintf("%s: backbone.nosql size set to %dMB (2x the migrated data, min 5MB) — review and adjust for expected growth", coll.Name, sizeMB))
		if lossy {
			todos = append(todos, fmt.Sprintf("%s: documents had an `id` field remapped to `_id` (Mongo key); original kept as `_azure_id`", coll.Name))
		}
	}

	if len(m.Secrets) > 0 {
		var env strings.Builder
		env.WriteString("# Secret values for `drift project deploy` (referenced as $NAME in the Driftfile).\n")
		for _, s := range m.Secrets {
			env.WriteString(s.Name + "=" + s.Value + "\n")
		}
		if err := writeWS(outDir, ".env.migrate", []byte(env.String()), 0o600); err != nil {
			return res, err
		}
		todos = append(todos, "secrets: fill in real values in .env.migrate (snapshot stored names; values only with --deref-secrets)")
	}

	for _, site := range m.Sites {
		res.refusals = append(res.refusals, Refusal{
			RefuseUnknownBinding, site.Name,
			"Static Web App content is not fetched in v1",
			"Build the SWA locally and add it under canvas/ + the Driftfile.",
		})
	}

	driftfile := buildDriftfile(m.slug(), fns, colls, m.Secrets)
	if err := writeWS(outDir, "Driftfile", []byte(driftfile), 0o644); err != nil {
		return res, err
	}

	if err := writeRefusedMD(outDir, res.refusals); err != nil {
		return res, err
	}
	if err := writeReportMD(outDir, todos); err != nil {
		return res, err
	}
	if err := writePlanMD(outDir, m, driftfile); err != nil {
		return res, err
	}

	// A machine-readable summary so `apply` can gate on the refusal count
	// without re-parsing markdown.
	summary, _ := json.MarshalIndent(map[string]int{
		"functions": res.scaffolds, "collections": len(colls), "refused": len(res.refusals),
	}, "", "  ")
	if err := writeWS(outDir, "_migration.json", append(summary, '\n'), 0o644); err != nil {
		return res, err
	}

	// Validate in the same context `drift project deploy` will run in: the
	// Driftfile's `$NAME` secret refs resolve from the environment, which at
	// deploy time is `source .env.migrate`. Seed any unset secret var from the
	// value we just wrote (never clobbering a real one the operator already has)
	// so the parser can resolve them.
	for _, s := range m.Secrets {
		if _, ok := os.LookupEnv(s.Name); !ok {
			_ = os.Setenv(s.Name, s.Value)
		}
	}

	// The contract: the Driftfile we emit must pass the platform's own parser.
	if _, err := project.ParseDriftfile(filepath.Join(outDir, "Driftfile")); err != nil {
		return res, fmt.Errorf("generated Driftfile failed validation: %w", err)
	}
	return res, nil
}

// handlerArgs is the Python handler signature for a trigger: a body-bearing
// method gets (body, req); everything else gets (req).
func handlerArgs(t ManifestTrigger) string {
	if t.Type == "queue" {
		return "body, req"
	}
	if t.Type == "http" {
		switch t.Method {
		case "post", "put", "patch":
			return "body, req"
		}
	}
	return "req"
}

// pythonScaffold emits a Tier-A Python handler: correct @atomic annotation,
// Drift signature and return, with the original Azure body preserved verbatim
// (commented) and a porting TODO. Valid Python that deploys as a stub.
func pythonScaffold(fn ManifestFunction, orig []byte) []byte {
	var b strings.Builder
	b.WriteString("# " + driftDirective(fn.Trigger, fn.Secrets) + "\n#\n")
	b.WriteString("# Tier-A scaffold from `drift migrate azure transform`. The original Azure handler\n")
	b.WriteString("# is preserved verbatim below (commented). Replace its Azure bindings with drift.*\n")
	b.WriteString("# SDK calls and return (status, message, payload). See REPORT.md.\n")
	b.WriteString("import drift  # noqa: F401\n\n\n")
	b.WriteString("def " + fn.Handler + "(" + handlerArgs(fn.Trigger) + "):\n")
	b.WriteString("    # ----- original Azure source (verbatim) -----\n")
	for _, line := range strings.Split(strings.TrimRight(string(orig), "\n"), "\n") {
		b.WriteString("    # " + line + "\n")
	}
	b.WriteString("    # ----- end original -----\n")
	b.WriteString("    return 200, \"OK\", {\"todo\": \"port this handler — see REPORT.md\"}\n")
	return []byte(b.String())
}

// cosmosIDToMongo remaps each document's `id` to `_id` (Mongo's key), keeping
// the original as `_azure_id`. Deterministic (json.Marshal sorts map keys).
// lossy=true when any remap happened (worth flagging to the user).
func cosmosIDToMongo(jsonl []byte) (out []byte, lossy bool) {
	var buf bytes.Buffer
	for _, line := range bytes.Split(jsonl, []byte("\n")) {
		line = bytes.TrimSpace(line)
		if len(line) == 0 {
			continue
		}
		var doc map[string]json.RawMessage
		if err := json.Unmarshal(line, &doc); err != nil {
			buf.Write(line)
			buf.WriteByte('\n')
			continue
		}
		if id, ok := doc["id"]; ok {
			if _, has := doc["_id"]; !has {
				doc["_id"] = id
				doc["_azure_id"] = id
				delete(doc, "id")
				lossy = true
			}
		}
		b, _ := json.Marshal(doc)
		buf.Write(b)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), lossy
}

func buildDriftfile(name string, fns []driftFn, colls []driftColl, secrets []ManifestSecret) string {
	var b strings.Builder
	b.WriteString("# Generated by `drift migrate azure transform`. Review REPORT.md before deploy.\n")
	b.WriteString("name: " + name + "\n")
	if len(fns) > 0 {
		b.WriteString("atomic:\n  functions:\n")
		for _, fn := range fns {
			if fn.cron != "" {
				b.WriteString("    - name: " + fn.slug + "\n      cron: \"" + fn.cron + "\"\n")
			} else {
				b.WriteString("    - " + fn.slug + "\n")
			}
		}
	}
	if len(colls) > 0 || len(secrets) > 0 {
		b.WriteString("backbone:\n")
		if len(colls) > 0 {
			b.WriteString("  nosql:\n")
			for _, c := range colls {
				b.WriteString("    - name: " + c.name + "\n      size: " + strconv.Itoa(c.sizeMB) + "MB\n      seed: " + c.seed + "\n")
			}
		}
		if len(secrets) > 0 {
			b.WriteString("  secrets:\n")
			names := make([]string, 0, len(secrets))
			for _, s := range secrets {
				names = append(names, s.Name)
			}
			sort.Strings(names)
			for _, n := range names {
				b.WriteString("    " + n + ": $" + n + "\n")
			}
		}
	}
	return b.String()
}

// writeWS writes content to outDir/rel, creating parents, with the given mode.
func writeWS(outDir, rel string, content []byte, mode os.FileMode) error {
	full := filepath.Join(outDir, filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
		return err
	}
	return os.WriteFile(full, content, mode)
}
