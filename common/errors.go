// Package common — humane error rendering for API responses and network
// failures.
//
// The guiding philosophy: the CLI owes its users clear, honest, explicit
// messages when something goes wrong. Dumping a raw JSON blob at the user's
// terminal is a cop-out. Every error should read like a human wrote it —
// informal where it helps, informative always, honest when it's our fault.
//
// Typical usage from a command:
//
//	resp, err := common.DoRequest(http.MethodPost, url, body)
//	if err != nil {
//	    fmt.Println(common.TransportError("create slice", err))
//	    return
//	}
//	defer resp.Body.Close()
//
//	respBody, err := common.CheckResponse(resp, "create slice")
//	if err != nil {
//	    fmt.Println(err)
//	    return
//	}
//	// ... use respBody
package common

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
)

// APIError is the humane rendering of a non-2xx response from the Drift API.
// It implements the error interface; callers should just `fmt.Println(err)`.
type APIError struct {
	// Op is a short, lowercase, imperative description of what the CLI
	// was trying to do ("create slice", "deploy atomic function"). Used
	// as the lead-in: "Couldn't {op}: ...".
	Op string

	// Status is the HTTP status code the server returned.
	Status int

	// Detail is the server-supplied reason, extracted from the JSON body
	// ("error" or "message" fields). May be empty when the body is not
	// JSON or when the server didn't include one.
	Detail string

	// Raw is the trimmed response body. Used only as a last-resort
	// fallback when Detail is empty AND the status code has no specific
	// mapping — we'd rather show the raw body than nothing.
	Raw string
}

func (e *APIError) Error() string {
	lead := "Something went wrong"
	if e.Op != "" {
		lead = "Couldn't " + e.Op
	}

	switch {
	case e.Status == http.StatusUnauthorized:
		if e.Op == "log in" {
			if e.Detail != "" {
				return lead + ": " + e.Detail + "."
			}
			return lead + ": invalid username or password."
		}
		return lead + ": your session expired. Run 'drift account login' to re-authenticate."

	case e.Status == http.StatusForbidden:
		if e.Detail != "" {
			return lead + ": " + e.Detail + "."
		}
		return lead + ": you don't have permission to do that."

	case e.Status == http.StatusNotFound:
		if e.Detail != "" {
			return lead + ": " + e.Detail + "."
		}
		return lead + ": that resource wasn't found."

	case e.Status == http.StatusConflict:
		if e.Detail != "" {
			return lead + ": " + e.Detail + "."
		}
		return lead + ": that conflicts with existing state."

	case e.Status == http.StatusPaymentRequired || e.Status == http.StatusTooManyRequests:
		if e.Detail != "" {
			return lead + ": " + e.Detail + ". Run 'drift slice resize' to add capacity."
		}
		return lead + ": you're at your plan limit. Run 'drift slice resize' to add capacity."

	case e.Status >= 400 && e.Status < 500:
		// Generic 4xx — trust the server's detail if it sent one.
		if e.Detail != "" {
			return lead + ": " + e.Detail + "."
		}
		if e.Raw != "" {
			return fmt.Sprintf("%s: %s (HTTP %d).", lead, e.Raw, e.Status)
		}
		return fmt.Sprintf("%s: request was rejected (HTTP %d).", lead, e.Status)

	case e.Status >= 500:
		if e.Detail != "" {
			return lead + ": the platform is having trouble — " + e.Detail + ". That's on us; give it a moment and try again."
		}
		return lead + ": the platform is having trouble. That's on us; give it a moment and try again."
	}

	// Fallback — shouldn't normally reach here for HTTP responses.
	if e.Detail != "" {
		return lead + ": " + e.Detail + "."
	}
	if e.Raw != "" {
		return lead + ": " + e.Raw
	}
	return lead + "."
}

// CheckResponse validates an HTTP response. On 2xx it returns the fully-read
// body and a nil error. On any other status it parses the server's error
// message (if any) and returns a humane *APIError.
//
// Callers remain responsible for `defer resp.Body.Close()`.
func CheckResponse(resp *http.Response, op string) ([]byte, error) {
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("Couldn't %s: failed to read response body (%w)", op, err)
	}
	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return body, nil
	}
	return nil, &APIError{
		Op:     op,
		Status: resp.StatusCode,
		Detail: extractDetail(body),
		Raw:    strings.TrimSpace(string(body)),
	}
}

// extractDetail pulls a human-readable message out of a JSON error body.
// The Drift API uses {"error": "..."} as its primary shape; LogAndRespond
// produces {"status", "message", ...}. We accept both, plus "detail" and
// "reason" as defensive fallbacks.
func extractDetail(body []byte) string {
	if len(body) == 0 {
		return ""
	}
	var parsed map[string]any
	if err := json.Unmarshal(body, &parsed); err != nil {
		return ""
	}
	for _, k := range []string{"error", "message", "detail", "reason"} {
		if v, ok := parsed[k].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

// TransportError wraps a network-level failure (DNS, dial, TLS, timeout) in
// a humane message. Pass the same `op` string you'd give to CheckResponse.
//
// This is the right function to call when DoRequest returns an error —
// the return value is already a formatted, printable error.
func TransportError(op string, err error) error {
	lead := "Couldn't " + op + ": "

	// Session-expired error bubbles up from DoRequest's refresh path.
	// Its message is already polite; just prepend the lead.
	if strings.Contains(err.Error(), "session expired") {
		return fmt.Errorf("%s%s", lead, strings.TrimPrefix(err.Error(), "session expired — "))
	}

	// Timeouts — check both url.Error and the generic net.Error interface.
	var urlErr *url.Error
	if errors.As(err, &urlErr) && urlErr.Timeout() {
		return fmt.Errorf("%scouldn't reach the Drift API (timed out). Is the platform up?", lead)
	}
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%scouldn't reach the Drift API (timed out). Is the platform up?", lead)
	}

	// Connection refused / DNS failure / dial errors.
	msg := err.Error()
	if strings.Contains(msg, "connection refused") ||
		strings.Contains(msg, "no such host") ||
		strings.Contains(msg, "dial tcp") {
		return fmt.Errorf("%scouldn't reach the Drift API. Is the platform up? (%v)", lead, err)
	}

	// Unknown — wrap it unchanged so the user still sees something.
	return fmt.Errorf("%s%v", lead, err)
}
