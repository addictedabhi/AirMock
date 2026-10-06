package hitlog

import (
	"testing"
	"time"
)

func TestBroadcasterDeliversToSubscriber(t *testing.T) {
	b := NewBroadcaster()
	ch, cancel := b.Subscribe()
	defer cancel()

	b.Publish(&Entry{ID: "e1"})

	select {
	case e := <-ch:
		if e.ID != "e1" {
			t.Fatalf("expected e1, got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for published entry")
	}
}

func TestBroadcastingStoreRecordsAndPublishes(t *testing.T) {
	s := newTestStore(t)
	b := NewBroadcaster()
	bs := NewBroadcastingStore(s, b)

	ch, cancel := b.Subscribe()
	defer cancel()

	if err := bs.Record(&Entry{MockID: "m1", ProtocolType: "rest", Direction: DirectionInbound}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	select {
	case e := <-ch:
		if e.MockID != "m1" {
			t.Fatalf("expected the recorded entry to be published, got %+v", e)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for the recorded entry to be published")
	}

	// And it must actually be persisted too, not just broadcast.
	list, err := s.List("m1", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected the entry to be persisted, got %d rows", len(list))
	}
}
