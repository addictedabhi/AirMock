// Package auth implements AirMock's optional admin login — see
// cmd/airmock/main.go's --admin-password flag/AIRMOCK_ADMIN_PASSWORD env
// var. It exists because the admin UI/API has no access control at all by
// default, which is fine for a single trusted local machine but not for an
// instance deployed on a server reachable by anyone on the network.
package auth

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"sync"
	"time"

	"golang.org/x/crypto/bcrypt"
)

var (
	ErrIncorrectPassword = errors.New("incorrect password")
	ErrTooManyAttempts   = errors.New("too many failed attempts, try again later")
)

// The two credential types the Settings UI lets an admin choose between —
// purely a UX/validation distinction (a PIN is numeric-only, checked
// against a stricter length; a password is free text with a lower minimum
// length) that also tells the login screen which input widget to render
// (see internal/web/api/auth.go's status endpoint and ui/src/lib/Login.
// svelte). Both are verified identically underneath (bcrypt compare) —
// this is not two different security mechanisms.
const (
	CredentialTypePIN      = "pin"
	CredentialTypePassword = "password"
)

const (
	maxFailedAttempts     = 5
	lockoutWindow         = 60 * time.Second
	defaultSessionTimeout = 24 * time.Hour
)

type session struct {
	expiresAt time.Time
}

type attempt struct {
	count       int
	lockedUntil time.Time
	lastAttempt time.Time
}

// LockoutError is returned while a caller is locked out after too many wrong
// passwords. It says how long is left, so the API can send a Retry-After and
// the login screen can tell the user, instead of just "try again later".
// errors.Is(err, ErrTooManyAttempts) still holds.
type LockoutError struct {
	RetryAfter time.Duration
}

// Seconds is the wait rounded up to whole seconds (at least 1).
func (e *LockoutError) Seconds() int {
	n := int((e.RetryAfter + time.Second - 1) / time.Second)
	if n < 1 {
		n = 1
	}
	return n
}

func (e *LockoutError) Error() string {
	n := e.Seconds()
	unit := "seconds"
	if n == 1 {
		unit = "second"
	}
	return fmt.Sprintf("too many failed attempts, try again in %d %s", n, unit)
}

func (e *LockoutError) Is(target error) bool { return target == ErrTooManyAttempts }

// Manager tracks server-side session state for AirMock's admin login, and
// (via store, when given) the password itself. Sessions are deliberately
// in-memory only, never persisted: a restart already drops every in-flight
// mock connection, so requiring a fresh login too is a small, expected
// cost — and it means a crash or redeploy can never leave a stale session
// valid forever. The password hash is a different matter — see store's own
// doc comment on Manager's NewManager/SetPassword/ClearPassword.
type Manager struct {
	mu             sync.Mutex
	passwordHash   []byte // nil when auth is disabled (no admin password configured)
	credentialType string // CredentialTypePIN or CredentialTypePassword — meaningless while disabled
	pinLength      int    // digits in the PIN; 0 when not a PIN or not known yet
	sessions       map[string]session
	attempts       map[string]*attempt
	sessionTimeout time.Duration
	store          *Store // nil in tests that don't care about persistence
}

// NewManager returns a Manager. If store already has a persisted password
// hash (set via SetPassword — e.g. from the Settings UI), that ALWAYS wins,
// since it represents whatever was most recently and intentionally
// configured — not a possibly-stale adminPassword left over in a deploy
// script's CLI flag/env var. adminPassword is only used to seed the
// in-memory password when store has nothing persisted yet (or is nil);
// either way, an empty result disables auth entirely — Enabled reports
// false and every check passes through, preserving today's zero-friction
// behavior for anyone who never opts in to a login.
func NewManager(adminPassword string, store *Store) (*Manager, error) {
	m := &Manager{
		sessions:       make(map[string]session),
		attempts:       make(map[string]*attempt),
		sessionTimeout: defaultSessionTimeout,
		store:          store,
	}
	if store != nil {
		credType, hash, err := store.GetCredential()
		if err != nil {
			return nil, err
		}
		if hash != "" {
			m.passwordHash = []byte(hash)
			m.credentialType = credType
			if credType == CredentialTypePIN {
				if n, err := store.GetPinLength(); err == nil {
					m.pinLength = n
				}
			}
			return m, nil
		}
	}
	if adminPassword == "" {
		return m, nil
	}
	hash, err := bcrypt.GenerateFromPassword([]byte(adminPassword), bcrypt.DefaultCost)
	if err != nil {
		return nil, err
	}
	m.passwordHash = hash
	// The CLI flag/env var has no concept of PIN vs password — treated as
	// a free-text password, the more permissive of the two validation
	// rules (see SetPassword), since whatever was passed there was never
	// validated against either rule to begin with.
	m.credentialType = CredentialTypePassword
	return m, nil
}

// SetPassword hashes and applies newPassword immediately under
// credentialType (enabling auth if it wasn't already), and persists both
// via store, if one was given — e.g. the Settings page's "enable login" /
// "change password" action. The very first time this is called (auth not
// yet enabled), there's nothing to authenticate the caller against,
// matching the same "whoever gets there first" tradeoff a first-run setup
// page would always have; every subsequent call happens through a route
// already gated by RequireAuth. Format validation (PIN must be digits-
// only, minimum lengths) happens in the HTTP handler, not here — this
// method trusts its caller.
func (m *Manager) SetPassword(credentialType, newPassword string) error {
	hash, err := bcrypt.GenerateFromPassword([]byte(newPassword), bcrypt.DefaultCost)
	if err != nil {
		return err
	}
	pinLength := 0
	if credentialType == CredentialTypePIN {
		pinLength = len(newPassword)
	}
	m.mu.Lock()
	m.passwordHash = hash
	m.credentialType = credentialType
	m.pinLength = pinLength
	m.mu.Unlock()
	if m.store == nil {
		return nil
	}
	if err := m.store.SetCredential(credentialType, string(hash)); err != nil {
		return err
	}
	return m.store.SetPinLength(pinLength)
}

// ClearPassword disables login entirely (the Settings page's "Turn off"
// action) and invalidates every existing session immediately. Enabled()
// reporting false already makes RequireAuth a no-op regardless, but it's
// cleaner not to leave a pile of now-meaningless sessions sitting in
// memory — especially since a later SetPassword call re-enables auth
// without clearing them itself.
func (m *Manager) ClearPassword() error {
	m.mu.Lock()
	m.passwordHash = nil
	m.credentialType = ""
	m.pinLength = 0
	m.sessions = make(map[string]session)
	m.mu.Unlock()
	if m.store == nil {
		return nil
	}
	if err := m.store.SetCredential("", ""); err != nil {
		return err
	}
	return m.store.SetPinLength(0)
}

// Enabled reports whether a password was configured at all.
func (m *Manager) Enabled() bool {
	return m.passwordHash != nil
}

// CredentialType reports which of CredentialTypePIN/CredentialTypePassword
// is currently configured — meaningless (empty) while Enabled() is false.
// Exposed via /api/auth/status so the login screen knows which input
// widget to render.
func (m *Manager) CredentialType() string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.credentialType
}

// PinLength is how many digits the configured PIN has, or 0 if the credential
// is not a PIN or the length is not known yet (a PIN saved before it was
// recorded; the first successful login learns it). Exposed via
// /api/auth/status so the keypad can submit exactly when the PIN is complete.
func (m *Manager) PinLength() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.pinLength
}

// SetSessionTimeout is live-updatable from the Settings page (see
// internal/web/api/settings.go) — same pattern as httpengine.Engine's
// SetBodyCaptureLimit/SetRedactedHeaders. d <= 0 falls back to the
// default. Re-anchors every currently active session to the new duration
// immediately, rather than leaving each to ride out whatever expiry it
// already had — the request that changes this setting is itself an
// authenticated action, so without this, the very session making the
// change would have already been extended under the OLD duration by
// RequireAuth's Validate() call moments earlier in the same request, and
// a newly-shortened timeout wouldn't actually take effect until whatever
// next genuine action happened to extend it under the new value (which
// might never come before the old, longer expiry was reached anyway).
func (m *Manager) SetSessionTimeout(d time.Duration) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if d <= 0 {
		d = defaultSessionTimeout
	}
	m.sessionTimeout = d
	now := time.Now()
	for token, s := range m.sessions {
		s.expiresAt = now.Add(d)
		m.sessions[token] = s
	}
}

func randomToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

// Login verifies password against the configured admin password and, on
// success, creates a new session and returns its token. remoteIP scopes
// the failed-attempt lockout per caller (see maxFailedAttempts/
// lockoutWindow), so one locked-out IP can't affect anyone else — this
// server is reachable by anyone on the network, not just one person.
func (m *Manager) Login(remoteIP, password string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	now := time.Now()
	a := m.attempts[remoteIP]
	if a != nil {
		if now.Before(a.lockedUntil) {
			return "", &LockoutError{RetryAfter: a.lockedUntil.Sub(now)}
		}
		// The lockout window (if any) has passed, and it's been a while
		// since the last failed attempt — treat this as a fresh start
		// rather than resuming a stale count from long ago.
		if now.Sub(a.lastAttempt) > lockoutWindow {
			a.count = 0
		}
	}

	if bcrypt.CompareHashAndPassword(m.passwordHash, []byte(password)) != nil {
		if a == nil {
			a = &attempt{}
			m.attempts[remoteIP] = a
		}
		a.count++
		a.lastAttempt = now
		if a.count >= maxFailedAttempts {
			a.lockedUntil = now.Add(lockoutWindow)
		}
		return "", ErrIncorrectPassword
	}

	delete(m.attempts, remoteIP)
	// A correct PIN of a not-yet-known length teaches it, so older PINs get
	// the same exact-length keypad behaviour after one successful login.
	if m.credentialType == CredentialTypePIN && m.pinLength == 0 && m.store != nil {
		m.pinLength = len(password)
		_ = m.store.SetPinLength(m.pinLength)
	}
	token, err := randomToken()
	if err != nil {
		return "", err
	}
	m.sessions[token] = session{expiresAt: now.Add(m.sessionTimeout)}
	return token, nil
}

// Validate reports whether token is a currently-valid session, extending
// its expiry on success (a rolling timeout) — an actively-used session
// should survive as long as it's genuinely still in use, not expire out
// from under someone mid-task.
func (m *Manager) Validate(token string) bool {
	if token == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	now := time.Now()
	if !ok || now.After(s.expiresAt) {
		delete(m.sessions, token)
		return false
	}
	s.expiresAt = now.Add(m.sessionTimeout)
	m.sessions[token] = s
	return true
}

// Peek reports whether token is a currently-valid session WITHOUT
// extending its expiry — for a caller that's only observing current
// status, not performing a real action. Using Validate for this (as
// /api/auth/status's own periodic frontend poll — see ui/src/lib/auth.js's
// installSessionExpiryPoller — briefly did) would keep resetting the
// rolling timeout on every poll and defeat it entirely: a poll every 30s
// would never let even a 1-minute session timeout actually expire, since
// each poll re-extends it past its own interval.
func (m *Manager) Peek(token string) bool {
	if token == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	s, ok := m.sessions[token]
	if !ok || time.Now().After(s.expiresAt) {
		return false
	}
	return true
}

// Logout invalidates token immediately, regardless of its expiry.
func (m *Manager) Logout(token string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.sessions, token)
}
