package tcpengine

import (
	"context"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
)

// TestTCPInteractionFaultDoesNotSuppressAsyncCallback guards against a real
// gap found by a follow-up audit: writeInteractionResponse returning false
// (fault fired, connection dropped) used to make interactionLoop return
// BEFORE ever reaching the Async-scheduling check, so a TCP interaction
// with both Fault (100% timeout, dropping the immediate response) and
// Async configured had its webhook silently, permanently dropped —
// inconsistent with REST/SOAP/GraphQL's serveAsync, which always schedules
// its callback regardless of what fault injection did to the immediate ack.
func TestTCPInteractionFaultDoesNotSuppressAsyncCallback(t *testing.T) {
	var callbackHits int32
	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callbackHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	store := newTestMockStore(t)
	e := New()
	e.SetCallbackScheduler(store)
	worker := mock.NewCallbackWorker(store, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.Start(ctx)

	def := &mock.Definition{
		ID: "tcp-fault-async", Name: "fault-and-async", Enabled: true, ProtocolType: "tcp",
		Fault: &mock.FaultConfig{TimeoutRatePercent: 100},
		TCP: &mock.TCPConfig{
			Interactions: []mock.TCPInteraction{
				{
					Match: "PLACE_ORDER", Response: "OK\n",
					Async: &mock.AsyncConfig{
						CallbackTargetMode:   "fixed",
						CallbackFixedURL:     callbackSrv.URL + "/webhook",
						CallbackBodyTemplate: `order: {{.Request.Body}}`,
					},
				},
			},
		},
	}
	addr := registerOnFreePort(t, e, def)
	defer e.Stop(context.Background())

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("PLACE_ORDER 7\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The 100% fault must still drop the connection with no response.
	conn.SetReadDeadline(time.Now().Add(1 * time.Second))
	buf := make([]byte, 16)
	if n, rerr := conn.Read(buf); rerr == nil {
		t.Fatalf("expected the connection to be dropped with no response, got %d bytes: %q", n, buf[:n])
	}

	// But the async callback must still fire — fault only affects the
	// immediate in-connection response.
	waitForCallbackJobStatus(t, store, def.ID, "succeeded")
	if atomic.LoadInt32(&callbackHits) == 0 {
		t.Fatal("expected the async callback webhook to have been hit despite the fault dropping the immediate response")
	}
}

func newTestMockStore(t *testing.T) *mock.Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return mock.NewStore(db)
}

// TestTCPInteractionAsyncTriggersCallback proves a TCP interaction's Async
// config actually schedules and delivers a real HTTP webhook — the same
// durable callback machinery a REST async mock uses, just triggered by a
// line of TCP input ("PLACE_ORDER ...") instead of an HTTP request.
func TestTCPInteractionAsyncTriggersCallback(t *testing.T) {
	var callbackHits int32
	var receivedBody []byte
	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		receivedBody = body
		atomic.AddInt32(&callbackHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	store := newTestMockStore(t)
	e := New()
	e.SetCallbackScheduler(store)
	worker := mock.NewCallbackWorker(store, nil, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.Start(ctx)

	def := &mock.Definition{
		ID: "tcp-async1", Name: "order-line", Enabled: true, ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			Interactions: []mock.TCPInteraction{
				{
					Match: "PLACE_ORDER", Response: "OK\n",
					Async: &mock.AsyncConfig{
						CallbackTargetMode:   "fixed",
						CallbackFixedURL:     callbackSrv.URL + "/webhook",
						CallbackBodyTemplate: `order: {{.Request.Body}}`,
					},
				},
			},
		},
	}
	addr := registerOnFreePort(t, e, def)
	defer e.Stop(context.Background())

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	if _, err := conn.Write([]byte("PLACE_ORDER 42\n")); err != nil {
		t.Fatalf("write: %v", err)
	}

	buf := make([]byte, 64)
	conn.SetReadDeadline(time.Now().Add(3 * time.Second))
	n, err := conn.Read(buf)
	if err != nil {
		t.Fatalf("read response: %v", err)
	}
	if string(buf[:n]) != "OK\r\n" {
		t.Fatalf("expected immediate TCP response %q, got %q", "OK\r\n", buf[:n])
	}

	jobs := waitForCallbackJobStatus(t, store, def.ID, "succeeded")
	if atomic.LoadInt32(&callbackHits) == 0 {
		t.Fatal("expected the async callback webhook to have been hit within the retry window")
	}
	if string(receivedBody) != "order: PLACE_ORDER 42" {
		t.Fatalf("unexpected callback payload: %q", receivedBody)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected exactly one callback job, got %+v", jobs)
	}
}

// waitForCallbackJobStatus polls the store directly for mockID's callback
// job to reach wantStatus, rather than polling a proxy signal (like the
// callback HTTP hit landing) and then doing a single one-shot store check
// right after — that pattern raced with the callback worker's own
// deliver()->markSucceeded() write, which happens strictly after the HTTP
// round-trip completes: the test's poll loop could observe the HTTP hit and
// move on to assert on the store microseconds before that write actually
// landed, occasionally reading a stale "pending"/"claimed" status (or, if
// the test then returned and its t.Cleanup closed the DB, a "sql: database
// is closed" error from the worker's now-racing write). Polling the actual
// awaited condition instead of a proxy for it closes that race outright.
func waitForCallbackJobStatus(t *testing.T, store *mock.Store, mockID, wantStatus string) []*mock.CallbackJob {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	var jobs []*mock.CallbackJob
	for time.Now().Before(deadline) {
		var err error
		jobs, err = store.ListCallbackJobsForMock(mockID)
		if err != nil {
			t.Fatalf("ListCallbackJobsForMock: %v", err)
		}
		if len(jobs) == 1 && jobs[0].Status == wantStatus {
			return jobs
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("expected exactly one callback job with status %q within the retry window, got %+v", wantStatus, jobs)
	return nil
}

// TestTCPInteractionWithoutAsyncDoesNotScheduleCallback guards the common
// (non-async) path: an interaction with no Async config must not schedule
// anything, even when a scheduler is configured.
func TestTCPInteractionWithoutAsyncDoesNotScheduleCallback(t *testing.T) {
	store := newTestMockStore(t)
	e := New()
	e.SetCallbackScheduler(store)
	defer e.Stop(context.Background())

	def := &mock.Definition{
		ID: "tcp-async2", Name: "plain", Enabled: true, ProtocolType: "tcp",
		TCP: &mock.TCPConfig{
			Interactions: []mock.TCPInteraction{{Match: "PING", Response: "PONG\n"}},
		},
	}
	addr := registerOnFreePort(t, e, def)

	conn, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.Close()
	conn.Write([]byte("PING\n"))
	buf := make([]byte, 64)
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	conn.Read(buf)

	time.Sleep(200 * time.Millisecond)
	jobs, err := store.ListCallbackJobsForMock(def.ID)
	if err != nil {
		t.Fatalf("ListCallbackJobsForMock: %v", err)
	}
	if len(jobs) != 0 {
		t.Fatalf("expected no callback jobs scheduled for a non-async interaction, got %+v", jobs)
	}
}
