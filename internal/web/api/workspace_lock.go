package api

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/apiclient"
	"github.com/addictedabhi/airmock/internal/auth"
)

// clientIDCookieName identifies "this browser" for workspace-lock purposes
// only — deliberately separate from SessionCookieName (the admin login's
// own cookie), since a workspace lock must keep working even when the admin
// login is completely disabled (the common case: auth.Manager.Enabled() ==
// false), at which point no admin session cookie is ever set at all. This
// cookie carries no Max-Age/Expires (a browser-session cookie, same as the
// admin session cookie already is) — combined with wslock.Tracker's
// in-memory, restart-clears-everything state, a workspace unlock has
// exactly the same lifetime characteristics the admin login's own session
// already has, without needing to key off it.
const clientIDCookieName = "airmock_client_id"

// clientID returns the current request's client-id cookie value, minting
// and setting a new one if it doesn't have one yet. Only called from the
// unlock handler below — every OTHER place that needs to know "has this
// browser already unlocked workspace X" (internal/web/api/mocks.go) reads
// the cookie without minting one, since a browser that's never called
// unlock trivially hasn't unlocked anything either way.
func clientID(w http.ResponseWriter, r *http.Request) (string, error) {
	if c, err := r.Cookie(clientIDCookieName); err == nil && c.Value != "" {
		return c.Value, nil
	}
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	token := hex.EncodeToString(b)
	http.SetCookie(w, &http.Cookie{
		Name:     clientIDCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		// No Secure flag, same reasoning as auth.go's own session cookie:
		// the admin server never runs TLS itself.
	})
	return token, nil
}

// clientIDIfPresent reads the client-id cookie without minting one — used
// wherever we only need to check an EXISTING unlock, never to create the
// identity a browser would need in order to have one.
func clientIDIfPresent(r *http.Request) string {
	c, err := r.Cookie(clientIDCookieName)
	if err != nil {
		return ""
	}
	return c.Value
}

// requireCurrentWorkspacePasswordForDelete writes a 401/404 (and returns
// false) if workspaceID is currently locked and the request doesn't carry
// its CURRENT password in a JSON body ({"password": "..."}) — deleting a
// locked workspace destroys the lock along with everything it protected,
// the same irreversible-enough-to-need-fresh-proof category as
// removeWorkspaceLock below, NOT the lighter "has this browser unlocked it
// at some point this session" gate checkWorkspaceUnlocked applies to a mere
// edit. Deliberately does not consult h.tracker at all: a browser that
// unlocked this workspace purely to edit a mock in it should NOT be able to
// delete the whole workspace on that same, older unlock — every delete of
// a locked workspace demands typing the password again, right then. Uses
// the same plain 401 (not the {"code":"workspace_locked",...} shape
// checkWorkspaceUnlocked returns) as unlockWorkspace/removeWorkspaceLock
// below, since the frontend drives this through its own always-ask-fresh
// confirmation prompt rather than the retry-after-unlock flow those
// "workspace_locked" responses exist for.
func (h *APIClientHandler) requireCurrentWorkspacePasswordForDelete(w http.ResponseWriter, r *http.Request, workspaceID string) bool {
	locked, err := h.store.IsWorkspaceLocked(workspaceID)
	if err != nil {
		// An unknown workspace id has nothing to enforce — let the real
		// delete call return its own 404 for that case.
		return true
	}
	if !locked {
		return true
	}
	var body struct {
		Password string `json:"password"`
	}
	// A DELETE with no body at all (or a malformed one) just decodes to a
	// zero-value Password — VerifyWorkspacePassword then correctly rejects
	// it as incorrect rather than this needing its own error path.
	_ = json.NewDecoder(r.Body).Decode(&body)
	if err := h.store.VerifyWorkspacePassword(workspaceID, body.Password); err != nil {
		if errors.Is(err, apiclient.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return false
		}
		writeErr(w, http.StatusUnauthorized, err)
		return false
	}
	return true
}

type lockWorkspaceRequest struct {
	CredentialType string `json:"credentialType"`
	NewPassword    string `json:"newPassword"`
	// CurrentPassword is required (and verified) only when the workspace
	// already has a lock — changing or replacing it needs proof of the
	// existing one; setting a brand-new lock does not.
	CurrentPassword string `json:"currentPassword"`
}

func (h *APIClientHandler) lockWorkspace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body lockWorkspaceRequest
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

	// Changing an already-locked workspace's password requires proving the
	// current one first — VerifyWorkspacePassword itself distinguishes
	// "not locked yet" (fine, this is a first-time lock) from "locked, but
	// the supplied current password is wrong" (must reject).
	verifyErr := h.store.VerifyWorkspacePassword(id, body.CurrentPassword)
	if verifyErr != nil && !errors.Is(verifyErr, apiclient.ErrWorkspaceNotLocked) {
		if errors.Is(verifyErr, apiclient.ErrNotFound) {
			writeErr(w, http.StatusNotFound, verifyErr)
			return
		}
		writeErr(w, http.StatusUnauthorized, verifyErr)
		return
	}

	if err := h.store.LockWorkspace(id, credType, body.NewPassword); err != nil {
		switch {
		case errors.Is(err, apiclient.ErrNotFound):
			writeErr(w, http.StatusNotFound, err)
		case errors.Is(err, apiclient.ErrDefaultWorkspaceLock):
			writeErr(w, http.StatusBadRequest, err)
		default:
			writeErr(w, http.StatusInternalServerError, err)
		}
		return
	}
	h.tracker.ClearWorkspace(id)
	// Whoever just set/changed this lock is unlocked for it immediately —
	// otherwise the very action of locking a workspace would lock the
	// person doing it out of their own next edit.
	cid, err := clientID(w, r)
	if err == nil {
		h.tracker.Unlock(cid, id)
	}
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type unlockWorkspaceRequest struct {
	Password string `json:"password"`
}

func (h *APIClientHandler) unlockWorkspace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body unlockWorkspaceRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.store.VerifyWorkspacePassword(id, body.Password); err != nil {
		switch {
		case errors.Is(err, apiclient.ErrNotFound):
			writeErr(w, http.StatusNotFound, err)
		case errors.Is(err, apiclient.ErrWorkspaceNotLocked):
			// Nothing to unlock — treat as trivially successful rather
			// than an error, since the caller's goal ("be able to modify
			// mocks in this workspace") is already true.
			writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
		default:
			writeErr(w, http.StatusUnauthorized, err)
		}
		return
	}
	cid, err := clientID(w, r)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	h.tracker.Unlock(cid, id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

type removeWorkspaceLockRequest struct {
	Password string `json:"password"`
}

func (h *APIClientHandler) removeWorkspaceLock(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var body removeWorkspaceLockRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.store.VerifyWorkspacePassword(id, body.Password); err != nil {
		switch {
		case errors.Is(err, apiclient.ErrNotFound):
			writeErr(w, http.StatusNotFound, err)
		default:
			writeErr(w, http.StatusUnauthorized, err)
		}
		return
	}
	if err := h.store.ClearWorkspaceLock(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	h.tracker.ClearWorkspace(id)
	writeJSON(w, http.StatusOK, map[string]bool{"ok": true})
}
