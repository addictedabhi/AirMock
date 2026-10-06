package hitlog

import "sync"

// Broadcaster fans out newly-recorded entries to any live-tail subscribers
// (a WS connection per browser tab). Best-effort: a slow subscriber drops
// entries rather than blocking new hits from being recorded.
type Broadcaster struct {
	mu   sync.Mutex
	subs map[chan *Entry]struct{}
}

func NewBroadcaster() *Broadcaster {
	return &Broadcaster{subs: map[chan *Entry]struct{}{}}
}

// Subscribe returns a channel of newly-published entries and a cancel func
// that must be called (e.g. via defer) when the subscriber disconnects.
func (b *Broadcaster) Subscribe() (ch chan *Entry, cancel func()) {
	ch = make(chan *Entry, 16)
	b.mu.Lock()
	b.subs[ch] = struct{}{}
	b.mu.Unlock()

	cancel = func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		if _, ok := b.subs[ch]; ok {
			delete(b.subs, ch)
			close(ch)
		}
	}
	return ch, cancel
}

func (b *Broadcaster) Publish(e *Entry) {
	b.mu.Lock()
	defer b.mu.Unlock()
	for ch := range b.subs {
		select {
		case ch <- e:
		default: // subscriber isn't keeping up; drop rather than block recording
		}
	}
}

// BroadcastingStore composes a *Store and a *Broadcaster behind the same
// Record signature httpengine's HitLogger interface expects, so recording
// a hit both persists it and pushes it to any live-tail subscribers in one
// call from the engine's perspective.
type BroadcastingStore struct {
	*Store
	broadcaster *Broadcaster
}

func NewBroadcastingStore(store *Store, b *Broadcaster) *BroadcastingStore {
	return &BroadcastingStore{Store: store, broadcaster: b}
}

func (b *BroadcastingStore) Record(e *Entry) error {
	if err := b.Store.Record(e); err != nil {
		return err
	}
	b.broadcaster.Publish(e)
	return nil
}
