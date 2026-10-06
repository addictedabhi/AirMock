package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"regexp"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/auth"
)

// SessionCookieName is exported so RequireAuth-wrapping code elsewhere
// (none yet, but a future middleware ordering change might need it) and
// this file's own handlers agree on the same cookie without a second
// hardcoded literal drifting out of sync.
const SessionCookieName = "airmock_session"

type AuthHandler struct {
	manager *auth.Manager
}

func NewAuthHandler(manager *auth.Manager) *AuthHandler {
	return &AuthHandler{manager: manager}
}

// Routes mounts every /auth endpoint as ONE subrouter (chi panics if the
// same path is Mount()ed twice, which splitting this across a public and an
// authenticated group at the router level would do) — status/login/logout
// stay open (a not-yet-authenticated client must reach status/login, and
// one whose session already expired must still reach logout), while
// set-password/clear-password apply RequireAuth per-route instead: a
// no-op while auth is disabled, so the very first "enable login" call
// still works with no session yet, but changing or clearing an
// already-set password requires being logged in first, exactly like any
// other admin action.
func (h *AuthHandler) Routes(r chi.Router) {
	r.Get("/status", h.status)
	r.Post("/login", h.login)
	r.Post("/logout", h.logout)
	r.With(h.RequireAuth).Post("/set-password", h.setPassword)
	r.With(h.RequireAuth).Post("/clear-password", h.clearPassword)
}

// status is a pure observation, not an action — it uses Peek (not
// Validate) specifically so that the frontend's own periodic poll of this
// endpoint (see ui/src/lib/auth.js's installSessionExpiryPoller) can't
// itself keep the session alive by re-extending it on every check.
func (h *AuthHandler) status(w http.ResponseWriter, r *http.Request) {
	authenticated := false
	if h.manager.Enabled() {
		if c, err := r.Cookie(SessionCookieName); err == nil {
			authenticated = h.manager.Peek(c.Value)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"authRequired":   h.manager.Enabled(),
		"authenticated":  authenticated,
		"credentialType": h.manager.CredentialType(),
		"pinLength":      h.manager.PinLength(),
	})
}

type loginRequest struct {
	Password string `json:"password"`
}

func (h *AuthHandler) login(w http.ResponseWriter, r *http.Request) {
	if !h.manager.Enabled() {
		// Nothing to log into — a client that always calls login
		// defensively (rather than checking status first) still gets a
		// clean success instead of a confusing error.
		writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		return
	}
	var body loginRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	token, err := h.manager.Login(remoteIP(r), body.Password)
	if err != nil {
		status := http.StatusUnauthorized
		if errors.Is(err, auth.ErrTooManyAttempts) {
			status = http.StatusTooManyRequests
			var lock *auth.LockoutError
			if errors.As(err, &lock) {
				w.Header().Set("Retry-After", strconv.Itoa(lock.Seconds()))
			}
		}
		writeErr(w, status, err)
		return
	}
	setSessionCookie(w, token)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *AuthHandler) logout(w http.ResponseWriter, r *http.Request) {
	if c, err := r.Cookie(SessionCookieName); err == nil {
		h.manager.Logout(c.Value)
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

const minAdminPasswordLength = 4

var pinPattern = regexp.MustCompile(`^\d{4,6}$`)

type setPasswordRequest struct {
	// CredentialType defaults to auth.CredentialTypePassword when empty —
	// callers that don't care about PIN-vs-password (e.g. a script driving
	// this endpoint directly) still get sensible free-text validation.
	CredentialType string `json:"credentialType"`
	NewPassword    string `json:"newPassword"`
}

func (h *AuthHandler) setPassword(w http.ResponseWriter, r *http.Request) {
	var body setPasswordRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	credType := body.CredentialType
	if credType == "" {
		credType = auth.CredentialTypePassword
	}
	switch credType {
	case auth.CredentialTypePIN:
		if !pinPattern.MatchString(body.NewPassword) {
			writeErr(w, http.StatusBadRequest, errors.New("a PIN must be 4-6 digits"))
			return
		}
	case auth.CredentialTypePassword:
		if len(body.NewPassword) < minAdminPasswordLength {
			writeErr(w, http.StatusBadRequest, fmt.Errorf("password must be at least %d characters", minAdminPasswordLength))
			return
		}
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("credentialType must be %q or %q", auth.CredentialTypePIN, auth.CredentialTypePassword))
		return
	}
	wasEnabled := h.manager.Enabled()
	if err := h.manager.SetPassword(credType, body.NewPassword); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !wasEnabled {
		// First-time enable: log the caller in immediately, so whoever just
		// configured this instance isn't locked out of the admin UI/API
		// they're sitting in right now.
		if token, err := h.manager.Login(remoteIP(r), body.NewPassword); err == nil {
			setSessionCookie(w, token)
		}
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (h *AuthHandler) clearPassword(w http.ResponseWriter, r *http.Request) {
	if err := h.manager.ClearPassword(); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	clearSessionCookie(w)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func setSessionCookie(w http.ResponseWriter, token string) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// No Secure flag: the admin server never runs TLS itself (see
		// server.go — only the separate mock gateway ever can), and Secure
		// would silently make this cookie unusable over that plain-HTTP
		// admin port.
	})
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		MaxAge:   -1,
	})
}

// RequireAuth is chi-router middleware gating every route it wraps behind a
// valid session — a no-op when auth is disabled (no admin password
// configured), so existing local/trusted deployments see zero behavior
// change unless they opt in.
func (h *AuthHandler) RequireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !h.manager.Enabled() {
			next.ServeHTTP(w, r)
			return
		}
		c, err := r.Cookie(SessionCookieName)
		if err != nil || !h.manager.Validate(c.Value) {
			writeErr(w, http.StatusUnauthorized, errors.New("authentication required"))
			return
		}
		next.ServeHTTP(w, r)
	})
}

// remoteIP strips the port from r.RemoteAddr so the failed-login lockout
// (see auth.Manager) scopes correctly per client address rather than per
// address:port, which would never repeat and so never actually lock out
// anything.
func remoteIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
