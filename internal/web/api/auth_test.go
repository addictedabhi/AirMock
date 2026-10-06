package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/auth"
	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestAuthRouter(t *testing.T, adminPassword string) (chi.Router, *AuthHandler) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	manager, err := auth.NewManager(adminPassword, auth.NewStore(db))
	if err != nil {
		t.Fatalf("auth.NewManager: %v", err)
	}
	h := NewAuthHandler(manager)
	r := chi.NewRouter()
	r.Route("/api/auth", h.Routes)
	r.With(h.RequireAuth).Get("/api/protected", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
	})
	return r, h
}

func doJSONWithCookie(t *testing.T, r chi.Router, method, path string, body any, cookie *http.Cookie) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatalf("encode body: %v", err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	if cookie != nil {
		req.AddCookie(cookie)
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func sessionCookieFrom(rec *httptest.ResponseRecorder) *http.Cookie {
	for _, c := range rec.Result().Cookies() {
		if c.Name == SessionCookieName {
			return c
		}
	}
	return nil
}

func TestAuthStatusWhenNoPasswordConfigured(t *testing.T) {
	r, _ := newTestAuthRouter(t, "")
	rec := doJSONWithCookie(t, r, http.MethodGet, "/api/auth/status", nil, nil)
	var got map[string]bool
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["authRequired"] || got["authenticated"] {
		t.Fatalf("expected auth disabled and unauthenticated, got %+v", got)
	}
}

func TestAuthStatusRequiredButNotAuthenticated(t *testing.T) {
	r, _ := newTestAuthRouter(t, "hunter2")
	rec := doJSONWithCookie(t, r, http.MethodGet, "/api/auth/status", nil, nil)
	var got map[string]bool
	json.Unmarshal(rec.Body.Bytes(), &got)
	if !got["authRequired"] || got["authenticated"] {
		t.Fatalf("expected auth required and not authenticated, got %+v", got)
	}
}

// TestRepeatedStatusPollsDoNotExtendTheSession is the actual bug report
// this guards against: a frontend that polls /api/auth/status
// periodically to notice an expired session (see ui/src/lib/auth.js's
// installSessionExpiryPoller) must not itself keep the session alive by
// re-extending it on every poll — otherwise even a very short configured
// session timeout would never actually fire while the poll kept running.
func TestRepeatedStatusPollsDoNotExtendTheSession(t *testing.T) {
	r, h := newTestAuthRouter(t, "hunter2")
	// Comfortably longer than bcrypt's own ~100ms+ comparison cost (paid
	// once, at login, before this timeout window even starts counting) —
	// too tight a margin here would make the test flaky on the Login call
	// itself, not on the behavior actually under test.
	h.manager.SetSessionTimeout(400 * time.Millisecond)

	loginRec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", loginRequest{Password: "hunter2"}, nil)
	cookie := sessionCookieFrom(loginRec)
	if cookie == nil {
		t.Fatal("expected a session cookie after login")
	}

	// Poll status repeatedly, faster than the session's own timeout — if
	// status extended the session (the bug), it would never expire.
	for i := 0; i < 3; i++ {
		time.Sleep(100 * time.Millisecond)
		rec := doJSONWithCookie(t, r, http.MethodGet, "/api/auth/status", nil, cookie)
		var got map[string]any
		json.Unmarshal(rec.Body.Bytes(), &got)
		if got["authenticated"] != true {
			t.Fatalf("poll %d: expected still authenticated (well within the 400ms timeout), got %+v", i, got)
		}
	}

	time.Sleep(500 * time.Millisecond)
	rec := doJSONWithCookie(t, r, http.MethodGet, "/api/auth/status", nil, cookie)
	var got map[string]any
	json.Unmarshal(rec.Body.Bytes(), &got)
	if got["authenticated"] != false {
		t.Fatalf("expected the session to have actually expired after repeated polling, got %+v", got)
	}
}

func TestAuthLoginWithCorrectPasswordThenStatusShowsAuthenticated(t *testing.T) {
	r, _ := newTestAuthRouter(t, "hunter2")

	loginRec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", loginRequest{Password: "hunter2"}, nil)
	if loginRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", loginRec.Code, loginRec.Body.String())
	}
	cookie := sessionCookieFrom(loginRec)
	if cookie == nil {
		t.Fatal("expected a session cookie to be set on successful login")
	}
	if cookie.Secure {
		t.Fatal("session cookie must not set Secure — the admin server only ever runs plain HTTP")
	}
	if !cookie.HttpOnly {
		t.Fatal("session cookie must be HttpOnly")
	}

	statusRec := doJSONWithCookie(t, r, http.MethodGet, "/api/auth/status", nil, cookie)
	var got map[string]bool
	json.Unmarshal(statusRec.Body.Bytes(), &got)
	if !got["authenticated"] {
		t.Fatalf("expected authenticated after login, got %+v", got)
	}
}

func TestAuthLoginWithWrongPasswordFails(t *testing.T) {
	r, _ := newTestAuthRouter(t, "hunter2")
	rec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", loginRequest{Password: "wrong"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
	if sessionCookieFrom(rec) != nil {
		t.Fatal("expected no session cookie on a failed login")
	}
}

func TestAuthLogoutInvalidatesTheSession(t *testing.T) {
	r, _ := newTestAuthRouter(t, "hunter2")
	loginRec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", loginRequest{Password: "hunter2"}, nil)
	cookie := sessionCookieFrom(loginRec)

	logoutRec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/logout", nil, cookie)
	if logoutRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", logoutRec.Code)
	}

	statusRec := doJSONWithCookie(t, r, http.MethodGet, "/api/auth/status", nil, cookie)
	var got map[string]bool
	json.Unmarshal(statusRec.Body.Bytes(), &got)
	if got["authenticated"] {
		t.Fatal("expected the session to be invalid after logout")
	}
}

func TestRequireAuthPassesThroughWhenAuthDisabled(t *testing.T) {
	r, _ := newTestAuthRouter(t, "")
	rec := doJSONWithCookie(t, r, http.MethodGet, "/api/protected", nil, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 when auth is disabled, got %d", rec.Code)
	}
}

func TestRequireAuthRejectsRequestsWithoutASession(t *testing.T) {
	r, _ := newTestAuthRouter(t, "hunter2")
	rec := doJSONWithCookie(t, r, http.MethodGet, "/api/protected", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", rec.Code)
	}
}

func TestRequireAuthAllowsRequestsWithAValidSession(t *testing.T) {
	r, _ := newTestAuthRouter(t, "hunter2")
	loginRec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", loginRequest{Password: "hunter2"}, nil)
	cookie := sessionCookieFrom(loginRec)

	rec := doJSONWithCookie(t, r, http.MethodGet, "/api/protected", nil, cookie)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestAuthLoginLockoutReturns429(t *testing.T) {
	r, _ := newTestAuthRouter(t, "hunter2")
	var rec *httptest.ResponseRecorder
	for i := 0; i < 5; i++ {
		rec = doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", loginRequest{Password: "wrong"}, nil)
	}
	rec = doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", loginRequest{Password: "hunter2"}, nil)
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 once locked out, got %d", rec.Code)
	}
}

// TestSetPasswordFirstEnableRequiresNoSessionAndLogsInImmediately covers
// the very first "enable login from Settings" call: RequireAuth is a no-op
// while auth is disabled, so no cookie is needed at all — and the caller
// comes away already logged in (via the returned cookie), not locked out
// of the instance they just configured.
func TestSetPasswordFirstEnableRequiresNoSessionAndLogsInImmediately(t *testing.T) {
	r, _ := newTestAuthRouter(t, "")

	rec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/set-password", setPasswordRequest{NewPassword: "newpass"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookieFrom(rec)
	if cookie == nil {
		t.Fatal("expected set-password to log the caller in immediately on first enable")
	}

	statusRec := doJSONWithCookie(t, r, http.MethodGet, "/api/auth/status", nil, cookie)
	var got map[string]bool
	json.Unmarshal(statusRec.Body.Bytes(), &got)
	if !got["authRequired"] || !got["authenticated"] {
		t.Fatalf("expected auth now required and this session authenticated, got %+v", got)
	}
}

func TestSetPasswordRejectsTooShortAPassword(t *testing.T) {
	r, _ := newTestAuthRouter(t, "")
	rec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/set-password", setPasswordRequest{NewPassword: "abc"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a too-short password, got %d", rec.Code)
	}
}

func TestSetPasswordWithPINTypeAndStatusReportsIt(t *testing.T) {
	r, _ := newTestAuthRouter(t, "")

	rec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/set-password", setPasswordRequest{CredentialType: "pin", NewPassword: "1234"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	cookie := sessionCookieFrom(rec)

	statusRec := doJSONWithCookie(t, r, http.MethodGet, "/api/auth/status", nil, cookie)
	var got map[string]any
	if err := json.Unmarshal(statusRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got["credentialType"] != "pin" {
		t.Fatalf("expected status to report the pin credential type, got %+v", got)
	}
}

func TestSetPasswordRejectsANonNumericPIN(t *testing.T) {
	r, _ := newTestAuthRouter(t, "")
	rec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/set-password", setPasswordRequest{CredentialType: "pin", NewPassword: "abcd"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-numeric PIN, got %d", rec.Code)
	}
}

func TestSetPasswordRejectsAPINOfTheWrongLength(t *testing.T) {
	r, _ := newTestAuthRouter(t, "")
	for _, pin := range []string{"12", "123", "1234567"} {
		rec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/set-password", setPasswordRequest{CredentialType: "pin", NewPassword: pin}, nil)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for PIN %q, got %d", pin, rec.Code)
		}
	}
}

func TestSetPasswordRejectsAnUnknownCredentialType(t *testing.T) {
	r, _ := newTestAuthRouter(t, "")
	rec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/set-password", setPasswordRequest{CredentialType: "fingerprint", NewPassword: "whatever"}, nil)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unrecognized credential type, got %d", rec.Code)
	}
}

// TestSetPasswordChangeRequiresAnExistingSession covers the OTHER half of
// the tradeoff: once a password already exists, changing it is just
// another admin action gated by RequireAuth like any other.
func TestSetPasswordChangeRequiresAnExistingSession(t *testing.T) {
	r, _ := newTestAuthRouter(t, "hunter2")

	rec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/set-password", setPasswordRequest{NewPassword: "newpass"}, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 changing a password with no session, got %d", rec.Code)
	}

	loginRec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", loginRequest{Password: "hunter2"}, nil)
	cookie := sessionCookieFrom(loginRec)

	changeRec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/set-password", setPasswordRequest{NewPassword: "newpass"}, cookie)
	if changeRec.Code != http.StatusOK {
		t.Fatalf("expected 200 changing a password with a valid session, got %d: %s", changeRec.Code, changeRec.Body.String())
	}

	// The OLD password must no longer work, and the NEW one must.
	oldRec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", loginRequest{Password: "hunter2"}, nil)
	if oldRec.Code != http.StatusUnauthorized {
		t.Fatalf("expected the old password to be rejected after a change, got %d", oldRec.Code)
	}
	newRec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", loginRequest{Password: "newpass"}, nil)
	if newRec.Code != http.StatusOK {
		t.Fatalf("expected the new password to work, got %d", newRec.Code)
	}
}

func TestClearPasswordRequiresAnExistingSessionAndDisablesAuth(t *testing.T) {
	r, _ := newTestAuthRouter(t, "hunter2")

	rec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/clear-password", nil, nil)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401 clearing the password with no session, got %d", rec.Code)
	}

	loginRec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", loginRequest{Password: "hunter2"}, nil)
	cookie := sessionCookieFrom(loginRec)

	clearRec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/clear-password", nil, cookie)
	if clearRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", clearRec.Code, clearRec.Body.String())
	}

	statusRec := doJSONWithCookie(t, r, http.MethodGet, "/api/auth/status", nil, nil)
	var got map[string]bool
	json.Unmarshal(statusRec.Body.Bytes(), &got)
	if got["authRequired"] {
		t.Fatalf("expected auth to now be disabled, got %+v", got)
	}
}

func TestAuthStatusReportsThePinLength(t *testing.T) {
	r, _ := newTestAuthRouter(t, "")
	rec := doJSONWithCookie(t, r, http.MethodPost, "/api/auth/set-password", map[string]string{"credentialType": "pin", "newPassword": "123456"}, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("set-password: %d %s", rec.Code, rec.Body.String())
	}
	rec = doJSONWithCookie(t, r, http.MethodGet, "/api/auth/status", nil, nil)
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	if out["credentialType"] != "pin" || out["pinLength"] != float64(6) {
		t.Fatalf("expected a pin credential of length 6 in the status, got %v", out)
	}
}

func TestLoginLockoutSendsRetryAfter(t *testing.T) {
	r, _ := newTestAuthRouter(t, "hunter2")
	var rec *httptest.ResponseRecorder
	for i := 0; i < 6; i++ {
		rec = doJSONWithCookie(t, r, http.MethodPost, "/api/auth/login", map[string]string{"password": "wrong"}, nil)
	}
	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected 429 once locked out, got %d: %s", rec.Code, rec.Body.String())
	}
	secs, err := strconv.Atoi(rec.Header().Get("Retry-After"))
	if err != nil || secs < 1 || secs > 60 {
		t.Fatalf("expected a Retry-After of 1-60 seconds, got %q", rec.Header().Get("Retry-After"))
	}
	var body map[string]string
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if !strings.Contains(body["error"], "second") {
		t.Fatalf("the message should include the wait, got %q", body["error"])
	}
}
