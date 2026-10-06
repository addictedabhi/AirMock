// Package wslock tracks, per calling browser, which locked Collections
// workspaces (see internal/apiclient.Workspace's lock fields) that browser
// has already unlocked — the workspace-scoped analogue of what
// internal/auth.Manager's session map does for the single app-wide admin
// login, but deliberately kept separate rather than folded into that
// Manager: workspace locks exist and must keep working even when the admin
// login is completely disabled (auth.Manager.Enabled() == false, the most
// common deployment today), so they can't be keyed by the admin session
// cookie, which in that case never exists at all. Callers identify "this
// browser" with their own separate, always-present client-id cookie (see
// internal/web/api's clientID helper) instead.
//
// In-memory only, same as auth.Manager's sessions — a restart already drops
// every in-flight mock connection, so requiring locked workspaces to be
// re-unlocked too is a small, expected cost, and it means a crash or
// redeploy can never leave a workspace unlock valid forever.
package wslock

import "sync"

// Tracker is safe for concurrent use.
type Tracker struct {
	mu       sync.Mutex
	unlocked map[string]map[string]bool // clientID -> set of unlocked workspace IDs
}

func New() *Tracker {
	return &Tracker{unlocked: make(map[string]map[string]bool)}
}

// Unlock marks workspaceID as unlocked for clientID — subsequent
// IsUnlocked calls with the same pair return true until ClearWorkspace or a
// process restart.
func (t *Tracker) Unlock(clientID, workspaceID string) {
	if clientID == "" || workspaceID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	set, ok := t.unlocked[clientID]
	if !ok {
		set = make(map[string]bool)
		t.unlocked[clientID] = set
	}
	set[workspaceID] = true
}

// IsUnlocked reports whether clientID has already unlocked workspaceID.
// An empty clientID (a caller with no client-id cookie yet — it can only be
// set by a successful Unlock call, so this means the browser has never
// unlocked anything) always reports false.
func (t *Tracker) IsUnlocked(clientID, workspaceID string) bool {
	if clientID == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.unlocked[clientID][workspaceID]
}

// ClearWorkspace forgets every client's unlock of workspaceID — called when
// a workspace's lock is changed or removed, so a since-changed or
// since-removed password doesn't leave stale unlocks lying around (removing
// the lock makes them moot anyway, since IsUnlocked is only ever consulted
// while the workspace is still locked; changing the password is the case
// that actually matters here, since without this, every browser that had
// unlocked the OLD password would stay unlocked under the new one too).
func (t *Tracker) ClearWorkspace(workspaceID string) {
	if workspaceID == "" {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	for _, set := range t.unlocked {
		delete(set, workspaceID)
	}
}
