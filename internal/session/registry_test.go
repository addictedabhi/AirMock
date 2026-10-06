package session

import (
	"sync"
	"testing"
)

func TestAddListGetRemove(t *testing.T) {
	r := NewRegistry[string]()

	id := r.Add("mock1", "tcp", "conn-a", "127.0.0.1:1234", map[string]string{"foo": "bar"})
	if id == "" {
		t.Fatal("expected a non-empty session id")
	}

	list := r.List("mock1")
	if len(list) != 1 {
		t.Fatalf("expected 1 session, got %d", len(list))
	}
	if list[0].ID != id || list[0].MockID != "mock1" || list[0].Protocol != "tcp" || list[0].RemoteAddr != "127.0.0.1:1234" {
		t.Fatalf("unexpected info: %+v", list[0])
	}
	if list[0].Meta["foo"] != "bar" {
		t.Fatalf("expected meta to carry through, got %+v", list[0].Meta)
	}
	if list[0].ConnectedAt.IsZero() || list[0].LastActiveAt.IsZero() {
		t.Fatal("expected ConnectedAt/LastActiveAt to be set")
	}

	conn, ok := r.Get("mock1", id)
	if !ok || conn != "conn-a" {
		t.Fatalf("expected to get back the tracked conn, got %q ok=%v", conn, ok)
	}

	r.Remove("mock1", id)
	if _, ok := r.Get("mock1", id); ok {
		t.Fatal("expected session to be gone after Remove")
	}
	if len(r.List("mock1")) != 0 {
		t.Fatal("expected an empty list after removing the only session")
	}
}

func TestListScopedPerMockAndOrderedByConnectTime(t *testing.T) {
	r := NewRegistry[string]()
	idA := r.Add("mockA", "tcp", "conn-a1", "a1", nil)
	_ = r.Add("mockB", "tcp", "conn-b1", "b1", nil)
	idA2 := r.Add("mockA", "tcp", "conn-a2", "a2", nil)

	listA := r.List("mockA")
	if len(listA) != 2 {
		t.Fatalf("expected 2 sessions for mockA, got %d", len(listA))
	}
	if listA[0].ID != idA || listA[1].ID != idA2 {
		t.Fatalf("expected oldest-first order, got %+v", listA)
	}

	listB := r.List("mockB")
	if len(listB) != 1 {
		t.Fatalf("expected 1 session for mockB, got %d", len(listB))
	}

	if len(r.List("mock-does-not-exist")) != 0 {
		t.Fatal("expected an empty (not nil-panicking) list for an unknown mock id")
	}
}

func TestTouchBumpsActivityAndCounters(t *testing.T) {
	r := NewRegistry[string]()
	id := r.Add("mock1", "tcp", "conn-a", "addr", nil)
	firstActive := r.List("mock1")[0].LastActiveAt

	r.Touch("mock1", id, 1, 0)
	r.Touch("mock1", id, 2, 3)

	info := r.List("mock1")[0]
	if info.MessagesIn != 3 {
		t.Fatalf("expected MessagesIn=3, got %d", info.MessagesIn)
	}
	if info.MessagesOut != 3 {
		t.Fatalf("expected MessagesOut=3, got %d", info.MessagesOut)
	}
	if info.LastActiveAt.Before(firstActive) {
		t.Fatal("expected LastActiveAt to move forward, not backward")
	}

	// Touching an id that doesn't exist must not panic or create a session.
	r.Touch("mock1", "no-such-id", 1, 1)
	if len(r.List("mock1")) != 1 {
		t.Fatal("Touch on an unknown id must be a no-op, not create an entry")
	}
}

func TestUpdateMetaMerges(t *testing.T) {
	r := NewRegistry[string]()
	id := r.Add("mock1", "mqtt", "conn-a", "addr", map[string]string{"a": "1"})

	r.UpdateMeta("mock1", id, map[string]string{"b": "2"})
	r.UpdateMeta("mock1", id, map[string]string{"a": "override"})

	meta := r.List("mock1")[0].Meta
	if meta["a"] != "override" || meta["b"] != "2" {
		t.Fatalf("expected merged meta {a:override, b:2}, got %+v", meta)
	}

	// UpdateMeta on an unknown session must not panic.
	r.UpdateMeta("mock1", "no-such-id", map[string]string{"x": "y"})
}

func TestUpdateMetaOnEntryWithNilMeta(t *testing.T) {
	r := NewRegistry[string]()
	id := r.Add("mock1", "mqtt", "conn-a", "addr", nil)
	r.UpdateMeta("mock1", id, map[string]string{"clientId": "abc"})
	if got := r.List("mock1")[0].Meta["clientId"]; got != "abc" {
		t.Fatalf("expected clientId=abc, got %+v", r.List("mock1")[0].Meta)
	}
}

func TestAllReturnsEveryConnForMockOnly(t *testing.T) {
	r := NewRegistry[string]()
	idA := r.Add("mockA", "tcp", "conn-a", "a", nil)
	idA2 := r.Add("mockA", "tcp", "conn-a2", "a2", nil)
	r.Add("mockB", "tcp", "conn-b", "b", nil)

	all := r.All("mockA")
	if len(all) != 2 || all[idA] != "conn-a" || all[idA2] != "conn-a2" {
		t.Fatalf("unexpected All() result: %+v", all)
	}
	if len(r.All("mock-does-not-exist")) != 0 {
		t.Fatal("expected an empty map for an unknown mock id")
	}
}

func TestCloseAllRemovesAndInvokesCloseFnForEveryConn(t *testing.T) {
	r := NewRegistry[string]()
	r.Add("mock1", "tcp", "conn-a", "a", nil)
	r.Add("mock1", "tcp", "conn-b", "b", nil)
	r.Add("mock2", "tcp", "conn-c", "c", nil)

	var mu sync.Mutex
	var closed []string
	r.CloseAll("mock1", func(c string) {
		mu.Lock()
		closed = append(closed, c)
		mu.Unlock()
	})

	if len(closed) != 2 {
		t.Fatalf("expected closeFn called for both mock1 sessions, got %v", closed)
	}
	if len(r.List("mock1")) != 0 {
		t.Fatal("expected mock1 sessions gone after CloseAll")
	}
	if len(r.List("mock2")) != 1 {
		t.Fatal("CloseAll(mock1) must not touch mock2's sessions")
	}
}

// TestConcurrentAccess exercises every method under -race from many
// goroutines at once — the registry is shared across each connection's own
// goroutine (Add/Remove/Touch) and the admin API's request-handling
// goroutines (List/Get/CloseAll) simultaneously in real use.
func TestConcurrentAccess(t *testing.T) {
	r := NewRegistry[int]()
	const n = 50
	var wg sync.WaitGroup

	for i := 0; i < n; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			id := r.Add("mock1", "tcp", i, "addr", map[string]string{"n": "x"})
			r.Touch("mock1", id, 1, 1)
			r.UpdateMeta("mock1", id, map[string]string{"m": "y"})
			_, _ = r.Get("mock1", id)
			_ = r.List("mock1")
			r.Remove("mock1", id)
		}(i)
	}
	wg.Wait()

	if len(r.List("mock1")) != 0 {
		t.Fatalf("expected all sessions removed, got %d left", len(r.List("mock1")))
	}
}
