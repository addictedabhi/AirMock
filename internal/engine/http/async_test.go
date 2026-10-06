package httpengine

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/smtp"
	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestMockStore(t *testing.T) *mock.Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return mock.NewStore(db)
}

// freeAddr returns an OS-assigned free "127.0.0.1:port" address — used in
// place of this file's previous hardcoded literal ports (18740-18743),
// which risked a rare bind collision (another process, or this same file's
// own tests under -parallel) or a stale TIME_WAIT hold from a prior run,
// either of which would fail e.Start with no real bug behind it.
func freeAddr(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	ln.Close()
	return addr
}

func TestAsyncMockAcksImmediatelyThenDeliversCallback(t *testing.T) {
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
		ID: "async1", ProtocolType: "rest", Method: http.MethodPost, PathPattern: "/orders", Enabled: true, Mode: "async",
		AsyncConfig: &mock.AsyncConfig{
			AckResponse:          mock.ResponseTemplate{StatusCode: 202, BodyTemplate: `{"status":"accepted"}`},
			CallbackTargetMode:   "fixed",
			CallbackFixedURL:     callbackSrv.URL + "/webhook",
			CallbackBodyTemplate: `{"orderId":"{{.Request.Body.orderId}}"}`,
		},
	}
	e.RegisterMock(def)

	addr := freeAddr(t)
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	resp, err := http.Post("http://"+addr+"/orders", "application/json", strings.NewReader(`{"orderId":"o-1"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("expected immediate 202 ack, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"status":"accepted"}` {
		t.Fatalf("unexpected ack body: %s", body)
	}

	jobs := waitForCallbackJobStatus(t, store, def.ID, "succeeded")
	if atomic.LoadInt32(&callbackHits) == 0 {
		t.Fatal("expected the callback target to have been hit within the retry window")
	}
	if string(receivedBody) != `{"orderId":"o-1"}` {
		t.Fatalf("unexpected callback payload: %s", receivedBody)
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
// is closed" error from the worker's now-racing write — this is the exact
// flake this test and internal/engine/tcp's equivalent were both observed
// hitting). Polling the actual awaited condition instead of a proxy for it
// closes that race outright.
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

// TestAsyncCallbackDeliversExactlyOnceEvenWhenSlowerThanPollInterval
// reproduces a real duplicate-delivery bug: ClaimDueCallbackJobs used to be
// a bare SELECT with no claiming step, so a job stayed 'pending' in the
// database for the full duration of its delivery attempt — if that took
// longer than the worker's poll interval (500ms; an SMTP handshake or a
// slow webhook easily can), the very next tick re-selected the SAME row
// and dispatched a second delivery goroutine before the first had even
// finished, fanning one trigger out into several duplicate deliveries.
func TestAsyncCallbackDeliversExactlyOnceEvenWhenSlowerThanPollInterval(t *testing.T) {
	var callbackHits int32
	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(800 * time.Millisecond) // longer than the worker's 500ms poll interval, on purpose
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
		ID: "async-slow", ProtocolType: "rest", Method: http.MethodPost, PathPattern: "/orders", Enabled: true, Mode: "async",
		AsyncConfig: &mock.AsyncConfig{
			AckResponse:          mock.ResponseTemplate{StatusCode: 202, BodyTemplate: `ok`},
			CallbackTargetMode:   "fixed",
			CallbackFixedURL:     callbackSrv.URL + "/webhook",
			CallbackBodyTemplate: `{}`,
		},
	}
	e.RegisterMock(def)

	addr := freeAddr(t)
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	resp, err := http.Post("http://"+addr+"/orders", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	// Waits for the job to actually reach "succeeded" (rather than a fixed
	// sleep) — long enough past several poll intervals (500ms each) plus the
	// slow target's own 800ms for the old bug to have re-claimed and
	// re-delivered the same job multiple times, had the fix not landed, but
	// without the flakiness risk of a fixed-duration sleep under load.
	jobs := waitForCallbackJobStatus(t, store, def.ID, "succeeded")
	if got := atomic.LoadInt32(&callbackHits); got != 1 {
		t.Fatalf("expected the callback target to be hit exactly once, got %d hits", got)
	}
	if len(jobs) != 1 {
		t.Fatalf("expected exactly one callback job, got %+v", jobs)
	}
}

func TestAsyncCallbackRecordsRetryAttemptOnFailure(t *testing.T) {
	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
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
		ID: "async2", ProtocolType: "rest", Method: http.MethodPost, PathPattern: "/orders", Enabled: true, Mode: "async",
		AsyncConfig: &mock.AsyncConfig{
			AckResponse:          mock.ResponseTemplate{StatusCode: 202, BodyTemplate: `ok`},
			CallbackTargetMode:   "fixed",
			CallbackFixedURL:     callbackSrv.URL + "/webhook",
			CallbackBodyTemplate: `{}`,
		},
	}
	e.RegisterMock(def)

	addr := freeAddr(t)
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	resp, err := http.Post("http://"+addr+"/orders", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	// One poll cycle (worker polls every 500ms) is enough for exactly one
	// failed attempt to be recorded; the retry backoff (5s+) intentionally
	// isn't waited out here — recording the attempt is what this asserts.
	var job *mock.CallbackJob
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		jobs, err := store.ListCallbackJobsForMock(def.ID)
		if err != nil {
			t.Fatalf("ListCallbackJobsForMock: %v", err)
		}
		if len(jobs) == 1 && jobs[0].AttemptCount > 0 {
			job = jobs[0]
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if job == nil {
		t.Fatal("expected a callback job with at least one recorded attempt")
	}
	if job.Status != "pending" {
		t.Fatalf("expected job to still be pending (retrying) after one failed attempt, got status=%s", job.Status)
	}
	if job.AttemptCount != 1 {
		t.Fatalf("expected attempt_count=1, got %d", job.AttemptCount)
	}
	if job.LastError == "" {
		t.Fatal("expected last_error to be recorded")
	}
}

// fakeSMTPServer speaks just enough SMTP (no STARTTLS/AUTH advertised) to
// accept one real EHLO/MAIL/RCPT/DATA/QUIT conversation over a real TCP
// connection, capturing the DATA payload for assertion.
func fakeSMTPServer(t *testing.T) (settings smtp.Settings, captured chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	captured = make(chan string, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		fmt.Fprintf(conn, "220 fake.smtp ESMTP\r\n")
		inData := false
		var data strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case inData:
				if strings.TrimRight(line, "\r\n") == "." {
					inData = false
					captured <- data.String()
					fmt.Fprintf(conn, "250 OK\r\n")
					continue
				}
				data.WriteString(line)
			case strings.HasPrefix(strings.ToUpper(line), "DATA"):
				inData = true
				fmt.Fprintf(conn, "354 Go ahead\r\n")
			case strings.HasPrefix(strings.ToUpper(line), "QUIT"):
				fmt.Fprintf(conn, "221 Bye\r\n")
				return
			default:
				fmt.Fprintf(conn, "250 OK\r\n")
			}
		}
	}()

	host, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return smtp.Settings{Host: host, Port: port, FromAddress: "mock@example.com", UseTLS: false}, captured
}

// smtpSettingsStub adapts a fixed smtp.Settings to mock.SMTPSettingsProvider
// without needing a real database-backed smtp.Store for this test.
type smtpSettingsStub struct{ settings smtp.Settings }

func (s smtpSettingsStub) GetSettings() (smtp.Settings, error) { return s.settings, nil }

func TestAsyncMockEmailChannelDeliversViaSMTP(t *testing.T) {
	settings, captured := fakeSMTPServer(t)

	store := newTestMockStore(t)
	e := New()
	e.SetCallbackScheduler(store)
	worker := mock.NewCallbackWorker(store, smtpSettingsStub{settings}, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.Start(ctx)

	def := &mock.Definition{
		ID: "async-email", ProtocolType: "rest", Method: http.MethodPost, PathPattern: "/orders", Enabled: true, Mode: "async",
		AsyncConfig: &mock.AsyncConfig{
			AckResponse:          mock.ResponseTemplate{StatusCode: 202, BodyTemplate: `{"status":"accepted"}`},
			CallbackTargetMode:   "fixed",
			CallbackFixedURL:     "customer@example.com", // the "target" is an email address for this channel
			CallbackChannel:      "email",
			EmailSubjectTemplate: `Order {{.Request.Body.orderId}} shipped`,
			CallbackBodyTemplate: `<p>Your order {{.Request.Body.orderId}} shipped.</p>`,
		},
	}
	e.RegisterMock(def)

	addr := freeAddr(t)
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	resp, err := http.Post("http://"+addr+"/orders", "application/json", strings.NewReader(`{"orderId":"o-9"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	select {
	case data := <-captured:
		if !strings.Contains(data, "Subject: Order o-9 shipped") {
			t.Fatalf("expected the rendered subject, got:\n%s", data)
		}
		if !strings.Contains(data, "<p>Your order o-9 shipped.</p>") {
			t.Fatalf("expected the rendered HTML body, got:\n%s", data)
		}
		if !strings.Contains(data, "To: customer@example.com") {
			t.Fatalf("expected the resolved recipient, got:\n%s", data)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("expected the fake SMTP server to have received the email within the retry window")
	}

	deadline := time.Now().Add(3 * time.Second)
	var jobs []*mock.CallbackJob
	for time.Now().Before(deadline) {
		jobs, err = store.ListCallbackJobsForMock(def.ID)
		if err != nil {
			t.Fatalf("ListCallbackJobsForMock: %v", err)
		}
		if len(jobs) == 1 && jobs[0].Status == "succeeded" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if len(jobs) != 1 || jobs[0].Channel != "email" || jobs[0].Status != "succeeded" {
		t.Fatalf("expected exactly one succeeded email-channel job, got %+v", jobs[0])
	}
}

// A callback with no callbackBodyTemplate used to be POSTed with an empty
// body (Content-Length: 0). It now gets the same default body the UI
// pre-fills.
func TestAsyncCallbackWithNoBodyTemplateSendsTheDefaultBody(t *testing.T) {
	var receivedBody []byte
	var contentLength int64 = -1
	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		receivedBody, _ = io.ReadAll(r.Body)
		contentLength = r.ContentLength
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
		ID: "async-nobody", ProtocolType: "rest", Method: http.MethodPost, PathPattern: "/nb", Enabled: true, Mode: "async",
		AsyncConfig: &mock.AsyncConfig{
			AckResponse:        mock.ResponseTemplate{StatusCode: 202},
			CallbackTargetMode: "fixed",
			CallbackFixedURL:   callbackSrv.URL + "/hook",
		},
	}
	e.RegisterMock(def)
	addr := freeAddr(t)
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	resp, err := http.Post("http://"+addr+"/nb", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	resp.Body.Close()

	waitForCallbackJobStatus(t, store, def.ID, "succeeded")
	if string(receivedBody) != mock.DefaultCallbackBody || contentLength != int64(len(mock.DefaultCallbackBody)) {
		t.Fatalf("expected the default callback body %q with a matching Content-Length, got %q (Content-Length %d)", mock.DefaultCallbackBody, receivedBody, contentLength)
	}
}
