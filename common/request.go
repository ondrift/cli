package common

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"
)

var httpClient = &http.Client{Timeout: 30 * time.Second}

func NewAuthenticatedRequest(method, url string, body io.Reader) (*http.Request, error) {
	return newAuthenticatedRequestCtx(context.Background(), method, url, body)
}

func newAuthenticatedRequestCtx(ctx context.Context, method, url string, body io.Reader) (*http.Request, error) {
	token, _, err := GetTokenFromSession()
	if err != nil {
		return nil, fmt.Errorf("failed to get token from session: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Authorization", "Bearer "+token)

	if slice := GetActiveSlice(); slice != "" {
		req.Header.Set("X-Slice", slice)
	}

	return req, nil
}

// DoRequest executes an authenticated HTTP request. If the server returns 401
// (token expired), it automatically refreshes the JWT using the stored refresh
// token and retries once. The body parameter is read, buffered, and replayed
// on retry so callers don't need to worry about re-seekable readers.
func DoRequest(method, url string, body io.Reader) (*http.Response, error) {
	return DoRequestWithContentType(method, url, "", body)
}

// DoRequestWithContext is like DoRequest but binds the request (and its
// post-refresh retry) to ctx. Use it for best-effort calls that must not
// stall a command on the default 30s client timeout — pass a context with a
// short deadline and treat a deadline error as "skip the optimization".
func DoRequestWithContext(ctx context.Context, method, url string, body io.Reader) (*http.Response, error) {
	return doRequestWithHeaders(ctx, method, url, body, nil)
}

// DoJSONRequest is a convenience wrapper for JSON request bodies.
func DoJSONRequest(method, url string, body io.Reader) (*http.Response, error) {
	return DoRequestWithContentType(method, url, "application/json", body)
}

// DoRequestWithContentType is like DoRequest but sets the Content-Type header
// on both the original request and the post-refresh retry. Pass an empty
// contentType to skip setting it.
func DoRequestWithContentType(method, url, contentType string, body io.Reader) (*http.Response, error) {
	headers := map[string]string{}
	if contentType != "" {
		headers["Content-Type"] = contentType
	}
	return DoRequestWithHeaders(method, url, body, headers)
}

// DoRequestWithHeaders is like DoRequest but applies the given headers to
// both the original request and the post-refresh retry.
func DoRequestWithHeaders(method, url string, body io.Reader, headers map[string]string) (*http.Response, error) {
	return doRequestWithHeaders(context.Background(), method, url, body, headers)
}

// doRequestWithHeaders is the context-aware core shared by all request
// helpers. ctx bounds both the initial send and the post-refresh retry.
func doRequestWithHeaders(ctx context.Context, method, url string, body io.Reader, headers map[string]string) (*http.Response, error) {
	var bodyBytes []byte
	if body != nil {
		var err error
		bodyBytes, err = io.ReadAll(body)
		if err != nil {
			return nil, fmt.Errorf("failed to read request body: %w", err)
		}
	}

	send := func() (*http.Response, error) {
		req, err := newAuthenticatedRequestCtx(ctx, method, url, bytes.NewReader(bodyBytes))
		if err != nil {
			return nil, err
		}
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		return httpClient.Do(req)
	}

	// First contact with the platform in this invocation also refreshes this
	// machine's copy of the Driftfile format. It rides along here rather than
	// living in `project deploy` because the format should be current whenever
	// the CLI has spoken to the platform AT ALL — a user who logs in today and
	// deploys on a plane tomorrow should be deploying against today's format,
	// not the one from whenever they last ran a project command.
	//
	// Bounded to once per process, best-effort, and never able to fail the
	// caller's actual request.
	RefreshDriftfileSchema()

	resp, err := send()
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusUnauthorized {
		return resp, nil
	}
	resp.Body.Close() // #nosec G104 -- discarded return is intentional and audited; the call's failure does not affect downstream correctness in this context.

	// Attempt token refresh. A 401 here is ambiguous on its own — it can mean
	// the access token simply aged out (refresh fixes it, invisibly) or that
	// the platform is unavailable and cannot say. Only the refresh's OWN
	// failure mode tells them apart, so it decides the message.
	if err := RefreshAccessToken(); err != nil {
		if errors.Is(err, ErrPlatformUnavailable) {
			return nil, errors.New(MaintenanceMessage)
		}
		return nil, fmt.Errorf("session expired — run 'drift account login' to re-authenticate")
	}

	return send()
}

// RefreshAccessToken uses the stored refresh token to obtain a new access
// token from the API, persisting both new tokens to the session file.
// Exported so callers can proactively refresh a locally-expired access token
// (e.g. the portal's pre-launch session gate) without waiting for a 401.
func RefreshAccessToken() error {
	_, refreshToken, err := GetTokenFromSession()
	if err != nil {
		return err
	}
	if refreshToken == "" {
		return fmt.Errorf("no refresh token stored")
	}

	body, _ := json.Marshal(map[string]string{
		"refresh_token": refreshToken,
		"device_id":     GetOrCreateDeviceID(),
	})

	resp, err := httpClient.Post(APIBaseURL+"/refresh", "application/json", bytes.NewReader(body))
	if err != nil {
		// Never reached the platform at all — that is unavailability, not a
		// verdict on the token.
		return fmt.Errorf("%w: %w", ErrPlatformUnavailable, err)
	}
	defer resp.Body.Close()

	// A refresh can fail two ways that look identical to the caller and are
	// opposite in meaning. REJECTED (401/403) means the refresh token is no
	// longer good and the user really must log in again. UNAVAILABLE (5xx)
	// means the platform could not answer — the token may be perfectly valid
	// and nobody knows yet.
	//
	// Collapsing them is what produced the worst message this CLI has shipped:
	// during a store outage every command said "run 'drift account login' to
	// re-authenticate", and following that advice logged the user out of a
	// platform that could not log them back in.
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return fmt.Errorf("%w (refresh returned %d)", ErrSessionRejected, resp.StatusCode)
	case resp.StatusCode >= 500:
		return fmt.Errorf("%w (refresh returned %d)", ErrPlatformUnavailable, resp.StatusCode)
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("refresh returned %d", resp.StatusCode)
	}

	var result struct {
		AccessToken  string `json:"access_token"`
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return fmt.Errorf("failed to parse refresh response: %w", err)
	}

	return SaveSession(result.AccessToken, result.RefreshToken)
}
