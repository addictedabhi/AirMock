package wslock

import "testing"

func TestUnlockAndIsUnlocked(t *testing.T) {
	tr := New()
	if tr.IsUnlocked("client-a", "ws-1") {
		t.Fatal("expected not unlocked before any Unlock call")
	}
	tr.Unlock("client-a", "ws-1")
	if !tr.IsUnlocked("client-a", "ws-1") {
		t.Fatal("expected unlocked after Unlock")
	}
}

func TestIsUnlockedScopedPerClient(t *testing.T) {
	tr := New()
	tr.Unlock("client-a", "ws-1")
	if tr.IsUnlocked("client-b", "ws-1") {
		t.Fatal("a different client should not see client-a's unlock")
	}
}

func TestIsUnlockedScopedPerWorkspace(t *testing.T) {
	tr := New()
	tr.Unlock("client-a", "ws-1")
	if tr.IsUnlocked("client-a", "ws-2") {
		t.Fatal("unlocking ws-1 should not unlock ws-2 for the same client")
	}
}

func TestIsUnlockedEmptyClientIDAlwaysFalse(t *testing.T) {
	tr := New()
	tr.Unlock("client-a", "ws-1")
	if tr.IsUnlocked("", "ws-1") {
		t.Fatal("an empty client id (never unlocked anything) must never report unlocked")
	}
}

func TestClearWorkspaceRemovesEveryClientsUnlock(t *testing.T) {
	tr := New()
	tr.Unlock("client-a", "ws-1")
	tr.Unlock("client-b", "ws-1")
	tr.Unlock("client-a", "ws-2")

	tr.ClearWorkspace("ws-1")

	if tr.IsUnlocked("client-a", "ws-1") {
		t.Fatal("client-a should no longer be unlocked for ws-1")
	}
	if tr.IsUnlocked("client-b", "ws-1") {
		t.Fatal("client-b should no longer be unlocked for ws-1")
	}
	if !tr.IsUnlocked("client-a", "ws-2") {
		t.Fatal("clearing ws-1 should not affect client-a's unlock of ws-2")
	}
}

func TestUnlockIgnoresEmptyIDs(t *testing.T) {
	tr := New()
	tr.Unlock("", "ws-1")
	tr.Unlock("client-a", "")
	if tr.IsUnlocked("client-a", "ws-1") {
		t.Fatal("Unlock with an empty client or workspace id should be a no-op")
	}
}
