// login_session_test.go — a failed login never destroys a working session.
//
// This invariant is the reason the outage message mattered so much. When the
// platform's store was down, every command told the user to run
// `drift account login`; the fear was that following that advice while the
// platform could not answer would leave them logged out and unable to get back
// in — a wait turned into a lockout.
//
// It does not, and these tests are why we can keep saying so. DoLoginErr
// returns on the first error and only reaches SaveSession on a fully
// successful response, and ClearSession is called from exactly one place
// (`drift account delete`). Nothing here is new behaviour — it was true and
// simply had nothing pinning it, so a future refactor could have quietly taken
// it away.
package account

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/ondrift/cli/v2/common"
)

// seedSession points HOME at a scratch dir, writes a session, and returns a
// reader for the raw file so a test can compare bytes before and after.
func seedSession(t *testing.T) func() []byte {
	t.Helper()

	home := t.TempDir()
	t.Setenv("HOME", home)
	path := filepath.Join(home, ".drift", "session.json")

	if err := common.SaveSession("access-token-still-good", "refresh-token-still-good"); err != nil {
		t.Fatalf("seeding the session: %v", err)
	}
	if err := common.SaveActiveSlice("my-slice"); err != nil {
		t.Fatalf("seeding the active slice: %v", err)
	}

	return func() []byte {
		b, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("reading the session file back: %v", err)
		}
		return b
	}
}

// stubAPI points the CLI at a server that answers /login with the given status
// and body, for the duration of the test.
func stubAPI(t *testing.T, status int, body string) {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)

	previous := common.APIBaseURL
	common.APIBaseURL = srv.URL
	t.Cleanup(func() { common.APIBaseURL = previous })
}

func TestFailedLoginLeavesTheSessionIntact(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
	}{
		{"rejected credentials", http.StatusUnauthorized, `{"error":"invalid username or password"}`},
		{"platform unavailable", http.StatusServiceUnavailable, `{"error":"service temporarily unavailable"}`},
		{"platform broken", http.StatusInternalServerError, `{"error":"boom"}`},
		{"a 200 carrying no tokens", http.StatusOK, `{}`},
		{"a 200 carrying junk", http.StatusOK, `not json at all`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			read := seedSession(t)
			before := read()
			stubAPI(t, tc.status, tc.body)

			if err := DoLoginErr("isrand", "whatever"); err == nil {
				t.Fatalf("the login was supposed to fail, and didn't")
			}

			if after := read(); string(after) != string(before) {
				t.Errorf("a failed login rewrote the session file.\nbefore: %s\nafter:  %s", before, after)
			}
		})
	}
}

func TestUnreachablePlatformLeavesTheSessionIntact(t *testing.T) {
	read := seedSession(t)
	before := read()

	// A server that is closed before the request is ever made — the transport
	// failure a real outage produces, rather than an HTTP status.
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	previous := common.APIBaseURL
	common.APIBaseURL = srv.URL
	srv.Close()
	t.Cleanup(func() { common.APIBaseURL = previous })

	if err := DoLoginErr("isrand", "whatever"); err == nil {
		t.Fatalf("the login was supposed to fail, and didn't")
	}

	if after := read(); string(after) != string(before) {
		t.Errorf("a login against an unreachable platform rewrote the session file.\nbefore: %s\nafter:  %s", before, after)
	}
}

// The control. If the assertions above passed because the test simply cannot
// observe a write, they prove nothing — so a SUCCESSFUL login must be seen to
// change the file, and to preserve the active slice while doing it.
func TestSuccessfulLoginIsObservedToRewriteTheSession(t *testing.T) {
	read := seedSession(t)
	before := read()
	stubAPI(t, http.StatusOK, `{"access_token":"fresh-access","refresh_token":"fresh-refresh"}`)

	if err := DoLoginErr("isrand", "correct-password"); err != nil {
		t.Fatalf("the login was supposed to succeed: %v", err)
	}

	after := read()
	if string(after) == string(before) {
		t.Fatalf("a successful login did not change the session file — the other tests are vacuous")
	}

	token, refresh, err := common.GetTokenFromSession()
	if err != nil {
		t.Fatalf("reading the new session: %v", err)
	}
	if token != "fresh-access" || refresh != "fresh-refresh" {
		t.Errorf("stored tokens are %q/%q, want fresh-access/fresh-refresh", token, refresh)
	}
	if slice := common.GetActiveSlice(); slice != "my-slice" {
		t.Errorf("logging in dropped the active slice: got %q, want my-slice", slice)
	}
}
