package scheduledevent

import (
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/storage"
)

func TestWorkerFire(t *testing.T) {
	var receivedBody string
	var receivedMethod string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedMethod = r.Method
		buf := make([]byte, 1024)
		n, _ := r.Body.Read(buf)
		receivedBody = string(buf[:n])
		w.WriteHeader(http.StatusCreated)
	}))
	defer srv.Close()

	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	hitLog := hitlog.NewStore(db)
	worker := NewWorker(store, hitLog)

	e, err := store.Create(&Event{
		Name: "test-event", Enabled: true, IntervalSecs: 30, TargetURL: srv.URL, Method: "PUT",
		BodyTemplate: `{"pinged": true}`,
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	worker.Fire(e)

	if receivedMethod != "PUT" {
		t.Fatalf("expected PUT, got %q", receivedMethod)
	}
	if receivedBody != `{"pinged": true}` {
		t.Fatalf("expected the rendered body template, got %q", receivedBody)
	}

	got, err := store.Get(e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastStatus != http.StatusCreated {
		t.Fatalf("expected LastStatus 201, got %d", got.LastStatus)
	}
	if got.LastFiredAt == nil {
		t.Fatal("expected LastFiredAt to be set")
	}

	entries, err := hitLog.Query(hitlog.QueryOptions{Direction: hitlog.DirectionScheduledEvent})
	if err != nil {
		t.Fatalf("Query hit log: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected exactly 1 scheduled-event hit-log entry, got %d", len(entries))
	}
	if entries[0].ResponseStatus != http.StatusCreated || entries[0].TargetURL != srv.URL {
		t.Fatalf("unexpected hit-log entry: %+v", entries[0])
	}
}

func TestWorkerFire_networkError(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	store := NewStore(db)
	hitLog := hitlog.NewStore(db)
	worker := NewWorker(store, hitLog)

	e, err := store.Create(&Event{Name: "bad", Enabled: true, IntervalSecs: 30, TargetURL: "http://127.0.0.1:1"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	worker.Fire(e)

	got, err := store.Get(e.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.LastError == "" {
		t.Fatal("expected LastError to be recorded for a network failure")
	}
	if got.LastStatus != 0 {
		t.Fatalf("expected LastStatus 0 on network failure, got %d", got.LastStatus)
	}
}
