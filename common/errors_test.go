package common

import (
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestAPIError_StatusMessages(t *testing.T) {
	tests := []struct {
		name   string
		err    APIError
		substr string
	}{
		{"401 session expired", APIError{Op: "list slices", Status: 401}, "session expired"},
		{"401 login with detail", APIError{Op: "log in", Status: 401, Detail: "bad password"}, "bad password"},
		{"401 login no detail", APIError{Op: "log in", Status: 401}, "invalid username or password"},
		{"403 with detail", APIError{Op: "delete slice", Status: 403, Detail: "not owner"}, "not owner"},
		{"403 no detail", APIError{Op: "delete", Status: 403}, "don't have permission"},
		{"404 with detail", APIError{Op: "get slice", Status: 404, Detail: "slice not found"}, "slice not found"},
		{"404 no detail", APIError{Op: "get", Status: 404}, "wasn't found"},
		{"409 with detail", APIError{Op: "create", Status: 409, Detail: "already exists"}, "already exists"},
		{"409 no detail", APIError{Op: "create", Status: 409}, "conflicts"},
		{"429 with detail", APIError{Op: "deploy", Status: 429, Detail: "function limit"}, "function limit"},
		{"429 no detail", APIError{Op: "deploy", Status: 429}, "plan limit"},
		{"402 same as 429", APIError{Op: "deploy", Status: 402}, "plan limit"},
		{"400 with detail", APIError{Op: "deploy", Status: 400, Detail: "bad input"}, "bad input"},
		{"400 with raw", APIError{Op: "deploy", Status: 400, Raw: "raw body"}, "raw body"},
		{"400 no detail no raw", APIError{Op: "deploy", Status: 400}, "rejected"},
		{"500 with detail", APIError{Op: "deploy", Status: 500, Detail: "db down"}, "db down"},
		{"500 no detail", APIError{Op: "deploy", Status: 500}, "having trouble"},
		// 503 deliberately DISCARDS the server's detail rather than echoing it.
		// This case used to assert the opposite. A 5xx detail is written for an
		// operator ("overloaded", "db down") and reaches a user who can do
		// nothing with it except learn our architecture; unavailability is the
		// one status where the honest message is the same every time.
		{"503 ignores the server detail", APIError{Op: "deploy", Status: 503, Detail: "db down"}, "temporarily unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			msg := tt.err.Error()
			if !strings.Contains(msg, tt.substr) {
				t.Errorf("error message %q should contain %q", msg, tt.substr)
			}
		})
	}
}

func TestAPIError_LeadIn(t *testing.T) {
	e := &APIError{Op: "create slice", Status: 500}
	if !strings.HasPrefix(e.Error(), "Couldn't create slice") {
		t.Fatalf("should start with op: %q", e.Error())
	}

	e2 := &APIError{Status: 500}
	if !strings.HasPrefix(e2.Error(), "Something went wrong") {
		t.Fatalf("empty op: %q", e2.Error())
	}
}

func TestAPIError_Fallback(t *testing.T) {
	// Status 0 (shouldn't happen but defensive).
	e := &APIError{Op: "test", Detail: "info"}
	if !strings.Contains(e.Error(), "info") {
		t.Fatalf("fallback: %q", e.Error())
	}

	e2 := &APIError{Op: "test", Raw: "raw"}
	if !strings.Contains(e2.Error(), "raw") {
		t.Fatalf("fallback raw: %q", e2.Error())
	}

	e3 := &APIError{Op: "test"}
	if !strings.HasSuffix(e3.Error(), ".") {
		t.Fatalf("fallback empty: %q", e3.Error())
	}
}

func TestCheckResponse_Success(t *testing.T) {
	resp := &http.Response{
		StatusCode: 200,
		Body:       io.NopCloser(strings.NewReader(`{"ok":true}`)),
	}
	body, err := CheckResponse(resp, "test op")
	if err != nil {
		t.Fatalf("expected nil error: %v", err)
	}
	if string(body) != `{"ok":true}` {
		t.Fatalf("body: %q", body)
	}
}

func TestCheckResponse_201(t *testing.T) {
	resp := &http.Response{
		StatusCode: 201,
		Body:       io.NopCloser(strings.NewReader(`created`)),
	}
	body, err := CheckResponse(resp, "create")
	if err != nil {
		t.Fatalf("201 should succeed: %v", err)
	}
	if string(body) != "created" {
		t.Fatalf("body: %q", body)
	}
}

func TestCheckResponse_Error(t *testing.T) {
	resp := &http.Response{
		StatusCode: 400,
		Body:       io.NopCloser(strings.NewReader(`{"error":"bad input"}`)),
	}
	_, err := CheckResponse(resp, "deploy")
	if err == nil {
		t.Fatal("expected error")
	}
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *APIError, got %T", err)
	}
	if apiErr.Status != 400 {
		t.Fatalf("status: %d", apiErr.Status)
	}
	if apiErr.Detail != "bad input" {
		t.Fatalf("detail: %q", apiErr.Detail)
	}
}

func TestCheckResponse_NonJSONBody(t *testing.T) {
	resp := &http.Response{
		StatusCode: 500,
		Body:       io.NopCloser(strings.NewReader("Internal Server Error")),
	}
	_, err := CheckResponse(resp, "deploy")
	var apiErr *APIError
	errors.As(err, &apiErr)
	if apiErr.Detail != "" {
		t.Fatalf("non-JSON body should have empty detail: %q", apiErr.Detail)
	}
	if apiErr.Raw != "Internal Server Error" {
		t.Fatalf("raw: %q", apiErr.Raw)
	}
}

func TestExtractDetail(t *testing.T) {
	tests := []struct {
		name string
		body string
		want string
	}{
		{"error field", `{"error":"not found"}`, "not found"},
		{"message field", `{"message":"created"}`, "created"},
		{"detail field", `{"detail":"info"}`, "info"},
		{"reason field", `{"reason":"quota"}`, "quota"},
		{"error takes precedence", `{"error":"err","message":"msg"}`, "err"},
		{"empty body", "", ""},
		{"invalid json", "not json", ""},
		{"empty error field", `{"error":""}`, ""},
		{"numeric value", `{"error":123}`, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := extractDetail([]byte(tt.body))
			if got != tt.want {
				t.Errorf("extractDetail(%q) = %q, want %q", tt.body, got, tt.want)
			}
		})
	}
}

func TestTransportError_ConnectionRefused(t *testing.T) {
	err := TransportError("deploy", errors.New("dial tcp 127.0.0.1:8000: connection refused"))
	if !strings.Contains(err.Error(), "couldn't reach the Drift API") {
		t.Fatalf("msg: %q", err.Error())
	}
}

func TestTransportError_Generic(t *testing.T) {
	err := TransportError("deploy", errors.New("some unknown error"))
	if !strings.Contains(err.Error(), "some unknown error") {
		t.Fatalf("should pass through: %q", err.Error())
	}
}

func TestTransportError_SessionExpired(t *testing.T) {
	err := TransportError("list slices", errors.New("session expired — run drift account login"))
	if !strings.Contains(err.Error(), "run drift account login") {
		t.Fatalf("msg: %q", err.Error())
	}
}

// TestAPIError_UnavailableIsNotABrokenPlatform — 502/503/504 mean Drift cannot
// answer right now, which is a different user action (wait) from a 500 (report
// it). The message must also not name the component: a database, a rollout and
// a chart apply are the same event from outside.
func TestAPIError_UnavailableIsNotABrokenPlatform(t *testing.T) {
	for _, code := range []int{502, 503, 504} {
		got := (&APIError{Status: code, Op: "list slices"}).Error()
		if !strings.Contains(got, "temporarily unavailable") {
			t.Errorf("HTTP %d: %q should say the platform is temporarily unavailable", code, got)
		}
		if !strings.Contains(got, "login is still valid") {
			t.Errorf("HTTP %d: %q must reassure the user their session is fine", code, got)
		}
		for _, leak := range []string{"database", "mongo", "Mongo", "kubernetes", "pod"} {
			if strings.Contains(got, leak) {
				t.Errorf("HTTP %d: %q leaks infrastructure detail (%q)", code, got, leak)
			}
		}
	}
}

// TestAPIError_RealUnauthorizedStillSaysReauthenticate is the control. The fix
// must not soften a genuine credential rejection into "try again later", or a
// user with a truly dead session waits forever for nothing to change.
func TestAPIError_RealUnauthorizedStillSaysReauthenticate(t *testing.T) {
	got := (&APIError{Status: 401, Op: "list slices"}).Error()
	if !strings.Contains(got, "drift account login") {
		t.Errorf("a real 401 must still tell the user to log in, got %q", got)
	}
}

// TestMaintenanceMessage_NeverAdvisesLoggingOut is the whole point. The message
// this replaced told users to re-authenticate during an outage — and auth needs
// the same platform, so following it turned a wait into a lockout.
func TestMaintenanceMessage_NeverAdvisesLoggingOut(t *testing.T) {
	if strings.Contains(MaintenanceMessage, "account login") ||
		strings.Contains(MaintenanceMessage, "re-authenticate") {
		t.Fatalf("the maintenance message must never advise logging out: %q", MaintenanceMessage)
	}
}
