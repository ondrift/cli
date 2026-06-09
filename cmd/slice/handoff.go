// Package lifecycle — browser handoff helpers shared by `drift slice create`
// and `drift slice resize`.
//
// The configurable-slices feature replaced the old "pick a tier" CLI prompt
// with a browser form: the CLI mints a session against the configurator
// service, opens the user's browser at a single-use URL, and then polls a
// redeem endpoint until the session is finalized. The CLI never sees the
// SliceConfig the user typed in — it only sees the final Slice document the
// configurator forwarded to api/ops/slice/{create,resize}.
//
// Both commands also support a --headless mode that bypasses the browser
// entirely. Headless mode posts directly to the api gateway and is the only
// way to drive slice provisioning from a non-interactive shell (CI, tests,
// scripts that pipe through ssh). It is intentionally less ergonomic than
// the browser flow — the user has to know the exact tier name or pass the
// full SliceConfig as a JSON file.
package slice

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/ondrift/cli/common"
)

// handoffMode mirrors configurator.routes.SessionMode. We do not import
// the configurator package because the CLI module is not allowed to depend
// on a server-side service module.
type handoffMode string

const (
	modeCreate handoffMode = "create"
	modeResize handoffMode = "resize"
)

// handoffResponse is the configurator's reply to /ops/session/handoff.
type handoffResponse struct {
	Token string `json:"token"`
	URL   string `json:"url"`
}

// redeemResponse is the configurator's reply to /ops/session/redeem. We
// only need the fields the CLI actually reads — Status and the embedded
// Result/Error. Result is left as raw JSON because it is the api service's
// Slice document and the CLI does not need to introspect every field.
type redeemResponse struct {
	Status string          `json:"status"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// runBrowserHandoff drives the full browser flow:
//
//  1. POST /ops/session/handoff to mint a session
//  2. open the user's browser at the returned URL
//  3. poll /ops/session/redeem until the session is non-pending
//  4. return the final Slice document or an error
//
// On a non-pending session, redeem deletes the session server-side, so
// the poll loop is exactly one round-trip past the user clicking "submit".
//
// The op string is used for the lead-in on humane error messages
// ("create slice", "resize slice").
func runBrowserHandoff(op, sliceName string, mode handoffMode, existing any) (json.RawMessage, error) {
	body, _ := json.Marshal(map[string]any{
		"slice_name": sliceName,
		"mode":       mode,
		"existing":   existing,
	})

	resp, err := common.DoJSONRequest(
		http.MethodPost,
		common.ConfiguratorBaseURL+"/ops/session/handoff",
		bytes.NewBuffer(body),
	)
	if err != nil {
		return nil, common.TransportError(op, err)
	}
	defer resp.Body.Close()

	respBody, err := common.CheckResponse(resp, op)
	if err != nil {
		return nil, err
	}

	var hr handoffResponse
	if err := json.Unmarshal(respBody, &hr); err != nil {
		return nil, fmt.Errorf("Couldn't %s: handoff response wasn't valid JSON (%w)", op, err)
	}

	fmt.Println("Opening configurator in your browser...")
	fmt.Println(hr.URL)
	if err := common.OpenBrowser(hr.URL); err != nil {
		// Browser launch failures are routine in containers, SSH
		// sessions, and headless dev environments. The URL is already
		// printed above; just remind the user to open it themselves.
		fmt.Println("(couldn't launch a browser automatically — open the URL above to continue)")
	}

	return pollRedeem(op, hr.Token)
}

// pollRedeem hits /ops/session/redeem on a 2-second cadence until the
// session moves out of "pending". The configurator's TTL is 10 minutes,
// so we cap our own loop at 15 minutes (a comfortable margin past the
// server-side expiry) and bail with a friendly timeout error if the user
// walks away from the form.
//
// We poll every 2 seconds because the user has to read a form, fill it
// in, and click submit — sub-second polling adds zero latency from the
// user's perspective and only burns CPU cycles.
func pollRedeem(op, token string) (json.RawMessage, error) {
	deadline := time.Now().Add(15 * time.Minute)
	for time.Now().Before(deadline) {
		time.Sleep(2 * time.Second)

		resp, err := common.DoRequest(
			http.MethodGet,
			common.ConfiguratorBaseURL+"/ops/session/redeem?s="+token,
			nil,
		)
		if err != nil {
			// A transient network blip while polling is not fatal.
			// The configurator session is still alive on the server,
			// so swallow the error and try again next tick.
			continue
		}

		body, checkErr := common.CheckResponse(resp, op)
		resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.
		if checkErr != nil {
			return nil, checkErr
		}

		var rr redeemResponse
		if err := json.Unmarshal(body, &rr); err != nil {
			return nil, fmt.Errorf("Couldn't %s: redeem response wasn't valid JSON (%w)", op, err)
		}

		switch rr.Status {
		case "pending":
			continue
		case "completed":
			return rr.Result, nil
		case "failed":
			return nil, fmt.Errorf("Couldn't %s: %s", op, rr.Error)
		case "cancelled":
			return nil, fmt.Errorf("Couldn't %s: the configurator session was cancelled", op)
		default:
			return nil, fmt.Errorf("Couldn't %s: unexpected session status %q", op, rr.Status)
		}
	}
	return nil, fmt.Errorf("Couldn't %s: timed out waiting for the configurator (15 minutes). The browser session has been discarded.", op)
}

// printSliceSummary prints a one-line confirmation for the user.
func printSliceSummary(verb string, raw json.RawMessage) {
	var s struct {
		Name string `json:"name"`
		Tier string `json:"tier"`
	}
	if err := json.Unmarshal(raw, &s); err != nil || s.Name == "" {
		fmt.Printf("Slice %s.\n", verb)
		return
	}
	label := "configured"
	if s.Tier == "hacker" {
		label = "free"
	}
	fmt.Printf("Slice '%s' %s (%s).\n", s.Name, verb, label)
}
