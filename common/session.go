package common

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const SessionFile = "~/.drift/session.json"

// APIBaseURL is the base URL for the Drift API gateway. It defaults to the
// public production gateway so a plain `go install` works out of the box; a
// local/dev build points it elsewhere via:
//
//	go build -ldflags "-X github.com/ondrift/cli/common.APIBaseURL=http://api.localhost:30036"
//
// At runtime, the DRIFT_API_URL environment variable takes precedence over
// the compiled-in default (useful for self-hosted instances or staging).
var APIBaseURL = "https://api.ondrift.eu"

// ConfiguratorBaseURL is the base URL for the configurator service. The CLI
// hits this directly (rather than via the api gateway) for the slice
// create/resize browser handoff: handoff mints a session, redeem polls for
// the result. Like APIBaseURL it defaults to production and a local/dev build
// overrides it via -ldflags.
//
// At runtime, the DRIFT_CONFIGURATOR_URL environment variable takes
// precedence over the compiled-in default.
var ConfiguratorBaseURL = "https://configurator.ondrift.eu"

func init() {
	if u := os.Getenv("DRIFT_API_URL"); u != "" {
		APIBaseURL = u
	}
	if u := os.Getenv("DRIFT_CONFIGURATOR_URL"); u != "" {
		ConfiguratorBaseURL = u
	}
}

type Session struct {
	Username    string `json:"username"`
	ActiveSlice string `json:"active_slice,omitempty"`
}

func expandPath(path string) (string, error) {
	// `len(path) >= 2` is load-bearing: the previous `len > 0` check
	// panicked with index-out-of-range when path was exactly "~"
	// (path[:2] over a 1-char string). The constant `SessionFile`
	// never trips this in practice, but a public-source CLI should
	// handle every input shape without panicking.
	if len(path) >= 2 && path[:2] == "~/" {
		// Use $HOME directly so that tests (and tools that override HOME) work
		// correctly. CGo's user.Current() ignores the HOME env var on macOS.
		home := os.Getenv("HOME")
		if home == "" {
			return "", fmt.Errorf("HOME environment variable is not set")
		}
		return filepath.Join(home, path[2:]), nil
	}
	return path, nil
}

func SaveSession(token, refresh_token string) error {
	// Read existing session to preserve active_slice across logins.
	data, _ := readSessionMap()
	if data == nil {
		data = make(map[string]string)
	}
	data["token"] = token
	data["refresh_token"] = refresh_token
	return writeSessionMap(data)
}

func GetTokenFromSession() (token string, refreshToken string, err error) {
	// Get full path to session file
	path, err := expandPath(SessionFile)
	if err != nil {
		return "", "", err
	}

	// Open the file for reading
	f, err := os.Open(path) // #nosec G304 — CLI tool reads user's own session file by design
	if err != nil {
		return "", "", err
	}
	defer f.Close()

	// Decode JSON into a map or struct
	data := make(map[string]string)
	dec := json.NewDecoder(f)
	if err := dec.Decode(&data); err != nil {
		return "", "", err
	}

	// Extract tokens from the map
	token, ok1 := data["token"]
	refreshToken, ok2 := data["refresh_token"]
	if !ok1 || !ok2 {
		return "", "", fmt.Errorf("token or refresh_token not found in session file")
	}

	return token, refreshToken, nil
}

// ClearSession removes the stored session file entirely. Used after the
// account it belonged to is deleted, so the CLI stops holding credentials for
// an account that no longer exists. A missing file is not an error.
func ClearSession() error {
	path, err := expandPath(SessionFile)
	if err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return err
	}
	return nil
}

// readSessionMap loads the raw session JSON as a string map.
func readSessionMap() (map[string]string, error) {
	path, err := expandPath(SessionFile)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path) // #nosec G304
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data := make(map[string]string)
	if err := json.NewDecoder(f).Decode(&data); err != nil {
		return nil, err
	}
	return data, nil
}

// writeSessionMap persists the raw session JSON.
func writeSessionMap(data map[string]string) error {
	path, err := expandPath(SessionFile)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600) // #nosec G304
	if err != nil {
		return err
	}
	defer f.Close()
	return json.NewEncoder(f).Encode(data)
}

// SaveActiveSlice persists the active slice name into the session file.
func SaveActiveSlice(name string) error {
	data, err := readSessionMap()
	if err != nil {
		return fmt.Errorf("no active session — log in first")
	}
	data["active_slice"] = name
	return writeSessionMap(data)
}

// GetActiveSlice returns the active slice name, or empty string if none set.
func GetActiveSlice() string {
	data, err := readSessionMap()
	if err != nil {
		return ""
	}
	return data["active_slice"]
}

// decodeTokenClaims decodes a JWT's payload segment (base64url, unverified —
// the CLI has no way to check the signature; it only reads its own claims
// for display/expiry purposes, the server is the real authority) into v.
func decodeTokenClaims(token string, v any) error {
	// JWT is three base64url-encoded segments separated by dots.
	parts := strings.SplitN(token, ".", 3)
	if len(parts) < 2 {
		return fmt.Errorf("malformed token")
	}
	// base64url → base64 (add padding).
	payload := parts[1]
	if m := len(payload) % 4; m != 0 {
		payload += strings.Repeat("=", 4-m)
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.NewReplacer("-", "+", "_", "/").Replace(payload))
	if err != nil {
		return err
	}
	return json.Unmarshal(decoded, v)
}

// GetUsername extracts the username from the stored JWT access token.
// Returns an empty string if the session or token is missing/unparseable.
func GetUsername() string {
	token, _, err := GetTokenFromSession()
	if err != nil || token == "" {
		return ""
	}
	var claims struct {
		Username string `json:"username"`
	}
	if decodeTokenClaims(token, &claims) != nil {
		return ""
	}
	return claims.Username
}

// TokenExpired reports whether the stored access token's `exp` claim has
// already passed, checked locally against the system clock — no network
// round trip. Treated as expired (true) if there's no session, no token, or
// the token can't be parsed, since none of those give the caller anything
// usable either.
func TokenExpired() bool {
	token, _, err := GetTokenFromSession()
	if err != nil || token == "" {
		return true
	}
	var claims struct {
		Exp int64 `json:"exp"`
	}
	if decodeTokenClaims(token, &claims) != nil || claims.Exp == 0 {
		return true
	}
	return time.Now().Unix() >= claims.Exp
}

// GetOrCreateDeviceID returns a stable per-workstation random ID. Used to
// bind refresh tokens: the server stores the ID at login, and a presented
// refresh token whose device_id doesn't match is treated as theft (revokes
// every live token for that user). Read-once cached on first call.
//
// Stored alongside the session file at ~/.drift/device_id with mode 0600.
// A stealer that copies session.json without device_id is locked out at
// the next refresh.
func GetOrCreateDeviceID() string {
	path, err := expandPath("~/.drift/device_id")
	if err != nil {
		return ""
	}
	if data, err := os.ReadFile(path); err == nil { // #nosec G304
		id := strings.TrimSpace(string(data))
		if id != "" {
			return id
		}
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return ""
	}
	id := hex.EncodeToString(b)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return id
	}
	_ = os.WriteFile(path, []byte(id), 0o600)
	return id
}

// RequireActiveSlice returns the active slice or an error instructing the user
// to select one with "drift slice use <name>".
func RequireActiveSlice() (string, error) {
	s := GetActiveSlice()
	if s == "" {
		return "", fmt.Errorf("no active slice — run 'drift slice use <name>' or 'drift slice create <name>' first")
	}
	return s, nil
}
