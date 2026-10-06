package auth

import (
	"errors"
	"strings"
	"testing"
	"time"

	"golang.org/x/crypto/bcrypt"
)

func TestNewManagerWithEmptyPasswordDisablesAuth(t *testing.T) {
	m, err := NewManager("", nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if m.Enabled() {
		t.Fatal("expected auth to be disabled when no admin password is configured")
	}
}

func TestNewManagerWithPasswordEnablesAuth(t *testing.T) {
	m, err := NewManager("hunter2", nil)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if !m.Enabled() {
		t.Fatal("expected auth to be enabled when an admin password is configured")
	}
}

func TestLoginWithCorrectPasswordReturnsAValidSessionToken(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	token, err := m.Login("1.2.3.4", "hunter2")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if token == "" {
		t.Fatal("expected a non-empty session token")
	}
	if !m.Validate(token) {
		t.Fatal("expected the returned token to validate")
	}
}

func TestLoginWithWrongPasswordFails(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	_, err := m.Login("1.2.3.4", "wrong")
	if !errors.Is(err, ErrIncorrectPassword) {
		t.Fatalf("expected ErrIncorrectPassword, got %v", err)
	}
}

func TestValidateRejectsUnknownOrEmptyToken(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	if m.Validate("") {
		t.Fatal("empty token must never validate")
	}
	if m.Validate("some-made-up-token") {
		t.Fatal("an unknown token must never validate")
	}
}

func TestValidateRejectsAnExpiredSession(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	m.SetSessionTimeout(10 * time.Millisecond)
	token, err := m.Login("1.2.3.4", "hunter2")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	time.Sleep(30 * time.Millisecond)
	if m.Validate(token) {
		t.Fatal("expected the session to have expired")
	}
}

func TestValidateExtendsExpiryOnEachSuccess(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	m.SetSessionTimeout(50 * time.Millisecond)
	// Seeds the session directly rather than via Login — bcrypt's own
	// ~100ms+ comparison cost (paid once, at login, unrelated to Validate's
	// actual expiry-check speed) would otherwise dominate/flake against a
	// short timeout chosen to keep this test fast, especially under -race.
	const token = "test-token"
	m.sessions[token] = session{expiresAt: time.Now().Add(m.sessionTimeout)}

	// Keep "using" the session faster than it expires — a rolling timeout
	// should never expire it as long as it's genuinely still active.
	for i := 0; i < 4; i++ {
		time.Sleep(20 * time.Millisecond)
		if !m.Validate(token) {
			t.Fatalf("session expired early on iteration %d despite rolling activity", i)
		}
	}
}

// TestSetSessionTimeoutReanchorsAlreadyActiveSessions is the actual bug
// report this guards against: shortening the configured timeout while a
// session is already active (e.g. via the Settings page, itself an
// authenticated action) must apply to that session right away — not
// leave it riding out whatever expiry it already had from before the
// change, which could be much later.
func TestSetSessionTimeoutReanchorsAlreadyActiveSessions(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	const token = "test-token"
	// Seed a session with a long expiry, as if it had been extended under
	// the old (much longer) timeout moments earlier in the same request.
	m.sessions[token] = session{expiresAt: time.Now().Add(24 * time.Hour)}

	m.SetSessionTimeout(30 * time.Millisecond)

	if !m.Peek(token) {
		t.Fatal("expected the session to still be valid immediately after shortening the timeout")
	}
	time.Sleep(60 * time.Millisecond)
	if m.Peek(token) {
		t.Fatal("expected the shortened timeout to have actually applied to the already-active session")
	}
}

// TestPeekDoesNotExtendExpiry is the whole reason Peek exists — a caller
// that only wants to observe current status (the frontend's periodic
// /api/auth/status poll) must not itself keep the session alive by
// resetting its rolling expiry on every check, the way Validate does.
func TestPeekDoesNotExtendExpiry(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	m.SetSessionTimeout(60 * time.Millisecond)
	const token = "test-token"
	expiresAt := time.Now().Add(m.sessionTimeout)
	m.sessions[token] = session{expiresAt: expiresAt}

	// Repeated Peek calls (mimicking a status poll) must not push expiresAt
	// forward at all.
	for i := 0; i < 3; i++ {
		if !m.Peek(token) {
			t.Fatalf("expected the session to still be valid on Peek iteration %d", i)
		}
	}
	if m.sessions[token].expiresAt != expiresAt {
		t.Fatal("expected Peek to leave expiresAt completely unchanged")
	}

	time.Sleep(80 * time.Millisecond)
	if m.Peek(token) {
		t.Fatal("expected the session to have actually expired — Peek calls must not have kept it alive")
	}
}

func TestPeekRejectsUnknownOrEmptyToken(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	if m.Peek("") {
		t.Fatal("empty token must never validate")
	}
	if m.Peek("some-made-up-token") {
		t.Fatal("an unknown token must never validate")
	}
}

func TestLogoutInvalidatesTheSession(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	token, _ := m.Login("1.2.3.4", "hunter2")
	m.Logout(token)
	if m.Validate(token) {
		t.Fatal("expected the session to be invalid after logout")
	}
}

func TestLoginLocksOutAfterRepeatedFailures(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	for i := 0; i < maxFailedAttempts; i++ {
		if _, err := m.Login("9.9.9.9", "wrong"); !errors.Is(err, ErrIncorrectPassword) {
			t.Fatalf("attempt %d: expected ErrIncorrectPassword, got %v", i, err)
		}
	}
	// One more attempt, even with the CORRECT password, must be locked out now.
	if _, err := m.Login("9.9.9.9", "hunter2"); !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("expected ErrTooManyAttempts once locked out, got %v", err)
	}
}

func TestLockoutIsScopedPerIP(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	for i := 0; i < maxFailedAttempts; i++ {
		m.Login("9.9.9.9", "wrong")
	}
	// A different IP must be completely unaffected by the first IP's lockout.
	if _, err := m.Login("8.8.8.8", "hunter2"); err != nil {
		t.Fatalf("expected a different IP to log in successfully, got %v", err)
	}
}

func TestSuccessfulLoginResetsThatIPsFailedAttemptCount(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	m.Login("1.2.3.4", "wrong")
	m.Login("1.2.3.4", "wrong")
	if _, err := m.Login("1.2.3.4", "hunter2"); err != nil {
		t.Fatalf("expected success: %v", err)
	}
	// Should now take a fresh maxFailedAttempts failures to lock out again,
	// not just the 1 remaining from before the successful login.
	for i := 0; i < maxFailedAttempts-1; i++ {
		if _, err := m.Login("1.2.3.4", "wrong"); !errors.Is(err, ErrIncorrectPassword) {
			t.Fatalf("attempt %d: expected ErrIncorrectPassword, got %v", i, err)
		}
	}
	if _, err := m.Login("1.2.3.4", "hunter2"); err != nil {
		t.Fatalf("expected the count to have reset after the earlier success, got locked out: %v", err)
	}
}

func TestNewManagerLoadsPersistedHashFromStoreOverCLIPassword(t *testing.T) {
	store := newTestStore(t)
	hash, err := bcrypt.GenerateFromPassword([]byte("frompersisted"), bcrypt.DefaultCost)
	if err != nil {
		t.Fatalf("bcrypt.GenerateFromPassword: %v", err)
	}
	if err := store.SetCredential(CredentialTypePassword, string(hash)); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}

	// A DIFFERENT adminPassword is also given here — the persisted hash in
	// the store must win, since it represents whatever was most recently
	// and intentionally set (e.g. via the Settings UI), not a possibly
	// stale CLI flag/env var left over from an old deploy script.
	m, err := NewManager("clipassword", store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.Login("1.2.3.4", "frompersisted"); err != nil {
		t.Fatalf("expected the persisted password to work, got %v", err)
	}
	if _, err := m.Login("1.2.3.4", "clipassword"); !errors.Is(err, ErrIncorrectPassword) {
		t.Fatalf("expected the CLI password to be ignored in favor of the persisted one, got %v", err)
	}
}

func TestNewManagerFallsBackToCLIPasswordWhenStoreEmpty(t *testing.T) {
	store := newTestStore(t)
	m, err := NewManager("clipassword", store)
	if err != nil {
		t.Fatalf("NewManager: %v", err)
	}
	if _, err := m.Login("1.2.3.4", "clipassword"); err != nil {
		t.Fatalf("expected the CLI password to work when the store has nothing persisted, got %v", err)
	}
}

func TestSetPasswordEnablesAuthAndPersistsToStore(t *testing.T) {
	store := newTestStore(t)
	m, _ := NewManager("", store)
	if m.Enabled() {
		t.Fatal("expected auth to start disabled")
	}

	if err := m.SetPassword(CredentialTypePassword, "newpass"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if !m.Enabled() {
		t.Fatal("expected auth to be enabled after SetPassword")
	}
	if m.CredentialType() != CredentialTypePassword {
		t.Fatalf("expected the credential type to be recorded, got %q", m.CredentialType())
	}
	if _, err := m.Login("1.2.3.4", "newpass"); err != nil {
		t.Fatalf("expected the new password to work, got %v", err)
	}

	credType, hash, err := store.GetCredential()
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if hash == "" {
		t.Fatal("expected SetPassword to persist a hash to the store")
	}
	if credType != CredentialTypePassword {
		t.Fatalf("expected the credential type to persist too, got %q", credType)
	}
}

func TestSetPasswordWithPINType(t *testing.T) {
	store := newTestStore(t)
	m, _ := NewManager("", store)
	if err := m.SetPassword(CredentialTypePIN, "1234"); err != nil {
		t.Fatalf("SetPassword: %v", err)
	}
	if m.CredentialType() != CredentialTypePIN {
		t.Fatalf("expected credential type %q, got %q", CredentialTypePIN, m.CredentialType())
	}
	if _, err := m.Login("1.2.3.4", "1234"); err != nil {
		t.Fatalf("expected the PIN to work as a login password, got %v", err)
	}
}

func TestClearPasswordDisablesAuthAndClearsStore(t *testing.T) {
	store := newTestStore(t)
	m, _ := NewManager("hunter2", store)
	m.SetPassword(CredentialTypePassword, "hunter2") // also seeds the store, not just the CLI-provided in-memory hash

	if err := m.ClearPassword(); err != nil {
		t.Fatalf("ClearPassword: %v", err)
	}
	if m.Enabled() {
		t.Fatal("expected auth to be disabled after ClearPassword")
	}
	_, hash, err := store.GetCredential()
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if hash != "" {
		t.Fatalf("expected the store's hash to be cleared, got %q", hash)
	}
}

func TestClearPasswordInvalidatesExistingSessions(t *testing.T) {
	m, _ := NewManager("hunter2", nil)
	token, err := m.Login("1.2.3.4", "hunter2")
	if err != nil {
		t.Fatalf("Login: %v", err)
	}
	if err := m.ClearPassword(); err != nil {
		t.Fatalf("ClearPassword: %v", err)
	}
	if m.Validate(token) {
		t.Fatal("expected every existing session to be invalidated once login is disabled")
	}
}

func TestPinLengthIsRememberedSoTheLoginScreenKnowsWhenItIsComplete(t *testing.T) {
	store := newTestStore(t)
	m, err := NewManager("", store)
	if err != nil {
		t.Fatal(err)
	}
	if got := m.PinLength(); got != 0 {
		t.Fatalf("no credential yet: PinLength = %d, want 0", got)
	}
	if err := m.SetPassword(CredentialTypePIN, "123456"); err != nil {
		t.Fatal(err)
	}
	if got := m.PinLength(); got != 6 {
		t.Fatalf("PinLength = %d, want 6", got)
	}

	// It survives a restart (a new Manager over the same store).
	m2, err := NewManager("", store)
	if err != nil {
		t.Fatal(err)
	}
	if got := m2.PinLength(); got != 6 {
		t.Fatalf("PinLength after restart = %d, want 6", got)
	}

	// A password has no PIN length, and clearing resets it.
	if err := m2.SetPassword(CredentialTypePassword, "a long password"); err != nil {
		t.Fatal(err)
	}
	if got := m2.PinLength(); got != 0 {
		t.Fatalf("PinLength for a password = %d, want 0", got)
	}
	if err := m2.SetPassword(CredentialTypePIN, "1234"); err != nil {
		t.Fatal(err)
	}
	if err := m2.ClearPassword(); err != nil {
		t.Fatal(err)
	}
	if got := m2.PinLength(); got != 0 {
		t.Fatalf("PinLength after clearing = %d, want 0", got)
	}
}

// A PIN saved before the length was recorded is of unknown length; the first
// successful login teaches it.
func TestPinLengthIsLearnedFromTheFirstSuccessfulLoginOfALegacyPIN(t *testing.T) {
	store := newTestStore(t)
	m, _ := NewManager("", store)
	if err := m.SetPassword(CredentialTypePIN, "654321"); err != nil {
		t.Fatal(err)
	}
	if err := store.SetPinLength(0); err != nil { // simulate a row from before this column existed
		t.Fatal(err)
	}
	legacy, _ := NewManager("", store)
	if got := legacy.PinLength(); got != 0 {
		t.Fatalf("legacy PIN length should be unknown (0), got %d", got)
	}
	if _, err := legacy.Login("10.0.0.1", "000000"); err == nil {
		t.Fatal("wrong PIN must fail")
	}
	if got := legacy.PinLength(); got != 0 {
		t.Fatalf("a failed login must not teach a length, got %d", got)
	}
	if _, err := legacy.Login("10.0.0.1", "654321"); err != nil {
		t.Fatalf("login: %v", err)
	}
	if got := legacy.PinLength(); got != 6 {
		t.Fatalf("PinLength after a successful login = %d, want 6", got)
	}
	again, _ := NewManager("", store)
	if got := again.PinLength(); got != 6 {
		t.Fatalf("the learned length must be persisted, got %d", got)
	}
}

func TestLockoutErrorSaysHowLongToWait(t *testing.T) {
	m, err := NewManager("hunter2", nil)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < maxFailedAttempts; i++ {
		_, _ = m.Login("7.7.7.7", "wrong")
	}
	_, err = m.Login("7.7.7.7", "hunter2")
	if !errors.Is(err, ErrTooManyAttempts) {
		t.Fatalf("a locked-out caller must still match ErrTooManyAttempts, got %v", err)
	}
	var lock *LockoutError
	if !errors.As(err, &lock) {
		t.Fatalf("expected a *LockoutError carrying the wait, got %T", err)
	}
	if lock.RetryAfter <= 0 || lock.RetryAfter > lockoutWindow {
		t.Fatalf("RetryAfter should be within the lockout window, got %v", lock.RetryAfter)
	}
	if !strings.Contains(err.Error(), "seconds") && !strings.Contains(err.Error(), "second") {
		t.Fatalf("the message should say how long to wait, got %q", err.Error())
	}
}
