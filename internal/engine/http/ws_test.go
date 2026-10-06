package httpengine

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"nhooyr.io/websocket"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/session"
	"github.com/addictedabhi/airmock/internal/validate"
)

func TestWSMockSendsOnConnectMessageAndMatchesInteractions(t *testing.T) {
	e := New()
	def := &mock.Definition{
		ID: "ws1", ProtocolType: "ws", PathPattern: "/echo", Enabled: true,
		WS: &mock.WSConfig{
			OnConnectMessage: `{"type":"welcome"}`,
			Interactions: []mock.WSInteraction{
				{Match: "ping", MatchType: "exact", Response: "pong"},
				{Match: "bye", MatchType: "exact", Response: "goodbye", CloseAfter: true},
			},
			DefaultResponse: "unrecognized",
		},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18714"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, "ws://"+addr+"/echo", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseNow()

	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()

	_, welcome, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("read welcome: %v", err)
	}
	if string(welcome) != `{"type":"welcome"}` {
		t.Fatalf("unexpected welcome message: %q", welcome)
	}

	if err := conn.Write(readCtx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	_, pong, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("read pong: %v", err)
	}
	if string(pong) != "pong" {
		t.Fatalf("expected pong, got %q", pong)
	}

	if err := conn.Write(readCtx, websocket.MessageText, []byte("something else")); err != nil {
		t.Fatalf("write unmatched: %v", err)
	}
	_, def2, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("read default response: %v", err)
	}
	if string(def2) != "unrecognized" {
		t.Fatalf("expected the default response, got %q", def2)
	}
}

func TestWSMockClosesAfterMatchedInteraction(t *testing.T) {
	e := New()
	def := &mock.Definition{
		ID: "ws2", ProtocolType: "ws", PathPattern: "/bye", Enabled: true,
		WS: &mock.WSConfig{
			Interactions: []mock.WSInteraction{{Match: "bye", MatchType: "exact", Response: "goodbye", CloseAfter: true}},
		},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18715"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, "ws://"+addr+"/bye", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseNow()

	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()

	if err := conn.Write(readCtx, websocket.MessageText, []byte("bye")); err != nil {
		t.Fatalf("write bye: %v", err)
	}
	_, goodbye, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("read goodbye: %v", err)
	}
	if string(goodbye) != "goodbye" {
		t.Fatalf("expected goodbye, got %q", goodbye)
	}

	// The server should close the connection right after — a further read
	// must fail rather than hang or return more data.
	if _, _, err := conn.Read(readCtx); err == nil {
		t.Fatal("expected the connection to be closed after a closeAfter interaction")
	}
}

func TestWSMockTemplatesResponseAgainstReceivedMessage(t *testing.T) {
	e := New()
	def := &mock.Definition{
		ID: "ws3", ProtocolType: "ws", PathPattern: "/tmpl", Enabled: true,
		WS: &mock.WSConfig{
			Interactions: []mock.WSInteraction{
				{Match: "hello", MatchType: "contains", Response: `{"echo":"{{.Request.Body}}"}`},
			},
		},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18716"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, "ws://"+addr+"/tmpl", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseNow()

	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()

	if err := conn.Write(readCtx, websocket.MessageText, []byte("hello there")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, resp, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	want := `{"echo":"hello there"}`
	if string(resp) != want {
		t.Fatalf("expected %q, got %q", want, resp)
	}
}

// TestWSMockRejectsHandshakeFailingValidation guards against a real gap: WS
// mocks never ran Validation at all — a WS mock's Validation rules were
// silently ignored no matter what was configured. Validation runs against
// the handshake (query/header/path params) since that's the only point at
// which an HTTP error response can still be sent, before the connection
// becomes a WebSocket.
func TestWSMockRejectsHandshakeFailingValidation(t *testing.T) {
	e := New()
	def := &mock.Definition{
		ID: "ws-validate", ProtocolType: "ws", PathPattern: "/secure", Enabled: true,
		Validation: []validate.Rule{{Field: "query.token", Required: true}},
		WS:         &mock.WSConfig{DefaultResponse: "ok"},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18717"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dialCancel()

	// Missing the required query.token — the handshake must be rejected,
	// not silently upgraded.
	if _, _, err := websocket.Dial(dialCtx, "ws://"+addr+"/secure", nil); err == nil {
		t.Fatal("expected the handshake to be rejected for missing query.token, but it succeeded")
	} else if !strings.Contains(err.Error(), "400") {
		t.Fatalf("expected a 400 status in the dial error, got: %v", err)
	}

	// With the required param present, the handshake must succeed.
	conn, _, err := websocket.Dial(dialCtx, "ws://"+addr+"/secure?token=abc", nil)
	if err != nil {
		t.Fatalf("expected the handshake to succeed with query.token present, got: %v", err)
	}
	conn.CloseNow()
}

// TestWSInteractionAsyncTriggersCallback guards against a real gap:
// WSInteraction had no Async field at all (unlike TCPInteraction, which has
// carried one deliberately for exactly this "sync reply plus a separate
// webhook" use case) — a WS message could never additionally trigger an
// out-of-band callback.
func TestWSInteractionAsyncTriggersCallback(t *testing.T) {
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
		ID: "ws-async", ProtocolType: "ws", PathPattern: "/orders", Enabled: true,
		WS: &mock.WSConfig{
			Interactions: []mock.WSInteraction{
				{
					Match: "place-order", MatchType: "exact", Response: "ack",
					Async: &mock.AsyncConfig{
						CallbackTargetMode:   "fixed",
						CallbackFixedURL:     callbackSrv.URL + "/webhook",
						CallbackBodyTemplate: `{}`,
					},
				},
			},
		},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	addr := "127.0.0.1:18718"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, "ws://"+addr+"/orders", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseNow()

	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()
	if err := conn.Write(readCtx, websocket.MessageText, []byte("place-order")); err != nil {
		t.Fatalf("write: %v", err)
	}
	_, resp, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(resp) != "ack" {
		t.Fatalf("expected the immediate ack %q, got %q", "ack", resp)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&callbackHits) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected the async callback to have been delivered within the retry window")
}

// TestWSMockFaultTimeoutDropsConnection guards against a real gap found by
// a follow-up audit: def.Fault was applied for every other protocol
// (REST/SOAP/GraphQL/TCP/SMTP/MQTT/FTP) but silently did nothing at all for
// WS — a WS mock's FaultConfig was completely inert.
func TestWSMockFaultTimeoutDropsConnection(t *testing.T) {
	e := New()
	def := &mock.Definition{
		ID: "ws-fault", ProtocolType: "ws", PathPattern: "/echo", Enabled: true,
		Fault: &mock.FaultConfig{TimeoutRatePercent: 100},
		WS: &mock.WSConfig{
			Interactions: []mock.WSInteraction{{Match: "ping", MatchType: "exact", Response: "pong"}},
		},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := freeAddr(t)
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, "ws://"+addr+"/echo", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseNow()

	writeCtx, writeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer writeCancel()
	if err := conn.Write(writeCtx, websocket.MessageText, []byte("ping")); err != nil {
		t.Fatalf("write: %v", err)
	}

	readCtx, readCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer readCancel()
	if _, resp, err := conn.Read(readCtx); err == nil {
		t.Fatalf("expected the connection to be dropped with no response, got %q", resp)
	}
}

// TestWSMockFaultDoesNotSuppressAsyncCallback mirrors the identical fix
// applied to tcpengine's handleTCPLine: fault injection must only affect
// the immediate in-connection response, never an Async interaction's
// separate, out-of-band webhook/email delivery.
func TestWSMockFaultDoesNotSuppressAsyncCallback(t *testing.T) {
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
		ID: "ws-fault-async", ProtocolType: "ws", PathPattern: "/orders", Enabled: true,
		Fault: &mock.FaultConfig{TimeoutRatePercent: 100},
		WS: &mock.WSConfig{
			Interactions: []mock.WSInteraction{
				{
					Match: "place-order", MatchType: "exact", Response: "ack",
					Async: &mock.AsyncConfig{
						CallbackTargetMode:   "fixed",
						CallbackFixedURL:     callbackSrv.URL + "/webhook",
						CallbackBodyTemplate: `{}`,
					},
				},
			},
		},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	addr := freeAddr(t)
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, "ws://"+addr+"/orders", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseNow()

	writeCtx, writeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer writeCancel()
	if err := conn.Write(writeCtx, websocket.MessageText, []byte("place-order")); err != nil {
		t.Fatalf("write: %v", err)
	}

	// The 100% fault must still drop the connection with no response.
	readCtx, readCancel := context.WithTimeout(context.Background(), 1*time.Second)
	defer readCancel()
	if _, resp, err := conn.Read(readCtx); err == nil {
		t.Fatalf("expected the connection to be dropped with no response, got %q", resp)
	}

	// But the async callback must still fire.
	waitForCallbackJobStatus(t, store, def.ID, "succeeded")
	if atomic.LoadInt32(&callbackHits) == 0 {
		t.Fatal("expected the async callback webhook to have been hit despite the fault dropping the immediate response")
	}
}

// TestWSSessionsListCloseAndSend covers the admin "Connected sessions" API
// surface: a connected client shows up in ListSessions, SendToSession pushes
// an unsolicited message the client actually receives (distinct from the
// mock's own interaction-matched replies), and CloseSession forcibly drops
// the connection (the client's next read errors).
func TestWSSessionsListCloseAndSend(t *testing.T) {
	e := New()
	def := &mock.Definition{
		ID: "ws-sessions", ProtocolType: "ws", PathPattern: "/live", Enabled: true,
		WS: &mock.WSConfig{
			Interactions: []mock.WSInteraction{{Match: "ping", MatchType: "exact", Response: "pong"}},
		},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18719"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	defer e.Stop(context.Background())

	dialCtx, dialCancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer dialCancel()
	conn, _, err := websocket.Dial(dialCtx, "ws://"+addr+"/live", nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer conn.CloseNow()

	// Give serveWS's goroutine a moment to register the session before
	// listing — Accept returning to the client doesn't guarantee the
	// server side has reached sessReg.Add yet.
	var sessions []session.Info
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		sessions = e.ListSessions(def.ID)
		if len(sessions) == 1 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 connected session, got %d", len(sessions))
	}
	if sessions[0].Protocol != "ws" {
		t.Fatalf("expected protocol %q, got %q", "ws", sessions[0].Protocol)
	}

	if err := e.SendToSession(def.ID, sessions[0].ID, "operator-pushed", nil); err != nil {
		t.Fatalf("SendToSession: %v", err)
	}
	readCtx, readCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer readCancel()
	_, pushed, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("read pushed message: %v", err)
	}
	if string(pushed) != "operator-pushed" {
		t.Fatalf("expected the operator-pushed message, got %q", pushed)
	}

	if err := e.CloseSession(def.ID, sessions[0].ID); err != nil {
		t.Fatalf("CloseSession: %v", err)
	}
	closeCtx, closeCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer closeCancel()
	if _, _, err := conn.Read(closeCtx); err == nil {
		t.Fatal("expected the client's next read to error after CloseSession")
	}

	if err := e.CloseSession(def.ID, "no-such-session"); err == nil {
		t.Fatal("expected an error closing a session id that doesn't exist")
	}
}
