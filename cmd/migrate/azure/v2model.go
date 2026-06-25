package azure

// v2model.go — the Azure Functions v2 Python programming model. v2 apps put
// every function in one `function_app.py` with `@app.route` / `@app.schedule` /
// `@app.queue_trigger` decorators and NO per-function `function.json`. The v1
// path (one folder + function.json each) finds nothing in a v2 app, so without
// this they'd be silently invisible — the worst failure for a tool whose whole
// promise is "your code is yours". This parser reads the decorators into the same
// ManifestTrigger shape v1 produces, so the rest of the pipeline (manifest,
// scaffold, Driftfile) is identical. It is deliberately a lexical parser, not a
// Python AST: it recognises the decorator forms Azure documents and refuses the
// rest, never guesses.

import (
	"regexp"
	"strings"
)

var (
	reV2Route     = regexp.MustCompile(`route\s*=\s*["']([^"']*)["']`)
	reV2Methods   = regexp.MustCompile(`methods\s*=\s*\[([^\]]*)\]`)
	reV2Method1   = regexp.MustCompile(`["']([A-Za-z]+)["']`)
	reV2Schedule  = regexp.MustCompile(`schedule\s*=\s*["']([^"']+)["']`)
	reV2QueueName = regexp.MustCompile(`(?:queue_name|queueName)\s*=\s*["']([^"']+)["']`)
	reV2AuthLevel = regexp.MustCompile(`auth_level\s*=\s*[\w.]*AuthLevel\.(\w+)`)
	reV2AppAuth   = regexp.MustCompile(`http_auth_level\s*=\s*[\w.]*AuthLevel\.(\w+)`)
	reV2DefName   = regexp.MustCompile(`^(?:async\s+)?def\s+(\w+)\s*\(`)
)

// isV2Model reports whether a source tree is the v2 in-code model: a
// function_app.py at the root and no v1 function.json folders.
func isV2Model(tree map[string][]byte) bool {
	if _, ok := tree["function_app.py"]; !ok {
		return false
	}
	for p := range tree {
		if strings.HasSuffix(p, "/function.json") {
			return false
		}
	}
	return true
}

// parseV2Functions parses function_app.py into Drift functions. Each is a `def`
// preceded by one or more `@app.*` decorators; the trigger decorator sets the
// ManifestTrigger, input/output-binding decorators become REPORT bindings, and a
// function with no supported trigger is refused. sourcePath is the export-relative
// path the captured function_app.py was written to (every function shares it).
func parseV2Functions(faName string, src []byte, sourcePath string) ([]ManifestFunction, []Refusal) {
	text := string(src)
	appAuth := "none"
	if m := reV2AppAuth.FindStringSubmatch(text); m != nil && !strings.EqualFold(m[1], "anonymous") {
		appAuth = "apikey"
	}

	var fns []ManifestFunction
	var refs []Refusal
	var decos []string
	var buf strings.Builder
	depth, collecting := 0, false

	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !collecting && strings.HasPrefix(trimmed, "@app.") {
			collecting, depth = true, 0
			buf.Reset()
		}
		if collecting {
			buf.WriteString(" " + trimmed)
			depth += strings.Count(trimmed, "(") - strings.Count(trimmed, ")")
			if depth <= 0 {
				decos = append(decos, strings.TrimSpace(buf.String()))
				collecting = false
			}
			continue
		}
		if m := reV2DefName.FindStringSubmatch(trimmed); m != nil {
			name := m[1]
			if len(decos) > 0 {
				t, bindings, ok := v2Trigger(decos, appAuth)
				if !ok {
					refs = append(refs, Refusal{
						RefuseUnknownBinding, faName + "/" + name,
						"v2 function has no supported trigger",
						"Trigger is not http/queue/timer (blob, event, cosmos, service-bus, …) — keep on Azure or adapt (Studio). function_app.py is in the snapshot.",
					})
				} else {
					if t.Type == "http" && t.Route == "" {
						t.Route = slugify(name)
					}
					fns = append(fns, ManifestFunction{
						Name: name, Handler: driftHandlerName(t, name), Runtime: "python",
						Trigger: t, SourcePath: sourcePath, Bindings: bindings,
					})
				}
			}
			decos = nil
			continue
		}
		// A real statement between decorators and the def is malformed; reset.
		if trimmed != "" && !strings.HasPrefix(trimmed, "#") {
			decos = nil
		}
	}
	return fns, refs
}

// v2Trigger maps a function's decorator block to a Drift trigger. The first
// recognised trigger decorator wins; the rest are recorded as bindings.
func v2Trigger(decos []string, appAuth string) (t ManifestTrigger, bindings []string, ok bool) {
	for _, d := range decos {
		dl := strings.ToLower(d)
		switch {
		case strings.Contains(dl, "@app.route"):
			t.Type, t.Method, t.Auth = "http", "post", appAuth
			if m := reV2Methods.FindStringSubmatch(d); m != nil {
				if first := reV2Method1.FindStringSubmatch(m[1]); first != nil {
					t.Method = strings.ToLower(first[1])
				}
			}
			if r := reV2Route.FindStringSubmatch(d); r != nil {
				t.Route = azureRouteToDrift(r[1])
			}
			if a := reV2AuthLevel.FindStringSubmatch(d); a != nil {
				if strings.EqualFold(a[1], "anonymous") {
					t.Auth = "none"
				} else {
					t.Auth = "apikey"
				}
			}
			ok = true
		case strings.Contains(dl, "@app.schedule"), strings.Contains(dl, "@app.timer_trigger"):
			if s := reV2Schedule.FindStringSubmatch(d); s != nil {
				cron, _ := ncrontabToCron(s[1])
				t.Type, t.Schedule, t.Auth = "cron", cron, "none"
				ok = true
			}
		case strings.Contains(dl, "@app.queue_trigger"):
			if q := reV2QueueName.FindStringSubmatch(d); q != nil {
				t.Type, t.Queue, t.Auth = "queue", q[1], "none"
				ok = true
			}
		case strings.Contains(dl, "_trigger"):
			bindings = append(bindings, "unsupported v2 trigger: "+strings.TrimSpace(d))
		case strings.HasPrefix(strings.TrimSpace(dl), "@app.") && !strings.Contains(dl, "function_name"):
			bindings = append(bindings, "v2 binding: "+strings.TrimSpace(d))
		}
	}
	return t, bindings, ok
}
