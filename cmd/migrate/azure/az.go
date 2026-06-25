package azure

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// azClient runs Azure CLI commands and decodes their JSON output. The real
// implementation (azRunner) shells out to `az`; tests inject a fake that
// returns fixture JSON, so the whole pipeline runs offline.
type azClient interface {
	// runJSON runs `az <args...> -o json` and unmarshals stdout into out.
	runJSON(args []string, out any) error
	// runRaw runs `az <args...>` WITHOUT forcing -o json and returns raw stdout.
	// For binary/file outputs the JSON path can't carry — e.g. downloading a
	// deployment package blob. Same read-only guard as runJSON.
	runRaw(args []string) ([]byte, error)
}

// azRunner is the real azClient — it shells out to the Azure CLI, loudly and
// read-only:
//
//   - Read-only by construction: the action verb of every command must be in
//     readOnlyVerbs. A mutating verb (create/delete/update/…) is refused
//     before exec, so the tool can never change anything in the tenant.
//   - Loud: every command is printed before it runs, so there is nothing the
//     tool does to Azure that the operator can't see on screen.
type azRunner struct {
	dryRun bool // print the command, skip exec, return no data
	logAll bool // print every command before running it
}

// readOnlyVerbs is the allowlist of az action verbs this tool may run. An
// allowlist (not a blocklist) is the safe default: a verb we didn't think of
// is refused, never silently allowed.
var readOnlyVerbs = map[string]bool{
	"list":    true,
	"show":    true,
	"version": true,
	// get-access-token mints a short-lived bearer for reading the function
	// source over Kudu/SCM. It reads a credential but mutates nothing in the
	// tenant, so it belongs on the read-only allowlist.
	"get-access-token": true,
	// download reads a blob (the deployment package, blob-container contents) to
	// local disk. It reads, never writes Azure — read-only, on the allowlist.
	"download": true,
	// peek reads queue messages WITHOUT dequeuing them (never `receive`), so the
	// source queue is left intact. Read-only by construction.
	"peek": true,
}

// actionVerb returns the az action verb — the last bare token before any flag.
//
//	["resource", "list", "-g", "rg"]              -> "list"
//	["consumption", "usage", "list", "-o", "json"] -> "list"
//	["account", "show"]                            -> "show"
func actionVerb(args []string) string {
	verb := ""
	for _, a := range args {
		if strings.HasPrefix(a, "-") {
			break
		}
		verb = a
	}
	return verb
}

// redactArgs masks credentials before a command is printed (print-before-exec /
// --az-log), so a storage key or SAS token never reaches the terminal or the
// audit file. The trust posture is "your security team greps this log" — it must
// never carry a secret.
func redactArgs(args []string) []string {
	out := make([]string, len(args))
	redactNext := false
	for i, a := range args {
		switch {
		case redactNext:
			out[i] = "<redacted>"
			redactNext = false
		case a == "--connection-string" || a == "--sas-token" || a == "--account-key" || a == "--uri":
			out[i] = a
			redactNext = true
		case strings.Contains(a, "AccountKey=") || strings.Contains(strings.ToLower(a), "sig="):
			out[i] = "<redacted>"
		default:
			out[i] = a
		}
	}
	return out
}

// guard refuses any command whose action verb is not read-only.
func guard(args []string) error {
	v := actionVerb(args)
	if !readOnlyVerbs[v] {
		return fmt.Errorf("refusing non-read-only command 'az %s' (verb %q): this tool never mutates Azure",
			strings.Join(args, " "), v)
	}
	return nil
}

func (r azRunner) runJSON(args []string, out any) error {
	if err := guard(args); err != nil {
		return err
	}
	full := append(append([]string{}, args...), "-o", "json")
	if r.logAll || r.dryRun {
		fmt.Fprintf(os.Stderr, "  $ az %s\n", strings.Join(redactArgs(full), " "))
	}
	if r.dryRun {
		return nil
	}
	cmd := exec.Command("az", full...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return fmt.Errorf("az %s: %s", strings.Join(args, " "), msg)
	}
	if out == nil || stdout.Len() == 0 {
		return nil
	}
	if err := json.Unmarshal(stdout.Bytes(), out); err != nil {
		return fmt.Errorf("az %s: parse json: %w", strings.Join(args, " "), err)
	}
	return nil
}

// runRaw runs a guarded, read-only `az` command without forcing -o json and
// returns its stdout. Used where the output isn't JSON (e.g. a blob download
// that writes the package to a file via -f, whose stdout we discard).
func (r azRunner) runRaw(args []string) ([]byte, error) {
	if err := guard(args); err != nil {
		return nil, err
	}
	if r.logAll || r.dryRun {
		fmt.Fprintf(os.Stderr, "  $ az %s\n", strings.Join(redactArgs(args), " "))
	}
	if r.dryRun {
		return nil, nil
	}
	cmd := exec.Command("az", args...)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		return nil, fmt.Errorf("az %s: %s", strings.Join(args, " "), msg)
	}
	return stdout.Bytes(), nil
}

// azInstalled reports whether the `az` binary is on PATH.
func azInstalled() bool {
	_, err := exec.LookPath("az")
	return err == nil
}

// azAccount is the trimmed shape of `az account show`.
type azAccount struct {
	Name string `json:"name"` // subscription display name
	ID   string `json:"id"`   // subscription id
	User struct {
		Name string `json:"name"`
	} `json:"user"`
	TenantID  string `json:"tenantId"`
	IsDefault bool   `json:"isDefault"`
}

// activeAccount returns the operator's currently-selected subscription, the
// trust banner the tool prints before touching anything.
func activeAccount(c azClient) (azAccount, error) {
	var acct azAccount
	if err := c.runJSON([]string{"account", "show"}, &acct); err != nil {
		return azAccount{}, fmt.Errorf("not logged in to Azure (run `az login`): %w", err)
	}
	return acct, nil
}
