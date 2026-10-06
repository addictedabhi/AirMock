package api

import (
	"encoding/json"
	"errors"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/session"
	"github.com/addictedabhi/airmock/internal/storage"
)

// fakeSessionEngine is a fakeEngine (see mocks_test.go) that additionally
// implements SessionLister/SessionCloser but NOT SessionSender — standing
// in for a view-only protocol (SMTP/Kafka/Diameter). fakeSendCapableEngine
// below embeds this and adds SendToSession for the send-capable protocols
// (TCP/WS/MQTT/SMPP/JMS); Go has no way to make a method conditionally
// present on one type, hence two types instead of a bool field.
type fakeSessionEngine struct {
	fakeEngine

	sessions  []session.Info
	closedIDs []string
	closeErr  error
	sentTo    []string // sessionIDs SendToSession was called with
	sentBody  string
	sentExtra map[string]string
	sendErr   error
}

func (f *fakeSessionEngine) ListSessions(mockID string) []session.Info { return f.sessions }

func (f *fakeSessionEngine) CloseSession(mockID, sessionID string) error {
	if f.closeErr != nil {
		return f.closeErr
	}
	f.closedIDs = append(f.closedIDs, sessionID)
	return nil
}

// sendToSessionImpl backs fakeSendCapableEngine's SendToSession (see the
// type-level comment on fakeSessionEngine for why it isn't just a method
// directly on this type).
func (f *fakeSessionEngine) sendToSessionImpl(mockID, sessionID, payload string, extra map[string]string) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.sentTo = append(f.sentTo, sessionID)
	f.sentBody = payload
	f.sentExtra = extra
	return nil
}

// fakeSendCapableEngine embeds fakeSessionEngine and adds SendToSession as a
// real method, so it satisfies SessionSender — Go has no way to make a
// method conditionally present on one type, hence this separate type
// instead of the `send bool` field actually gating anything at the
// interface level.
type fakeSendCapableEngine struct{ fakeSessionEngine }

func (f *fakeSendCapableEngine) SendToSession(mockID, sessionID, payload string, extra map[string]string) error {
	return f.sendToSessionImpl(mockID, sessionID, payload, extra)
}

func newTestSessionsRouter(t *testing.T) (chi.Router, *mock.Store) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := mock.NewStore(db)
	r := chi.NewRouter()
	r.Route("/api/mocks/{id}/sessions", NewSessionsHandler(store).Routes)
	return r, store
}

func createMock(t *testing.T, store *mock.Store, protocolType string) *mock.Definition {
	t.Helper()
	def, err := store.Create(&mock.Definition{Name: "m-" + protocolType, ProtocolType: protocolType, Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	return def
}

func TestSessionsListDispatchesToTheMocksOwnEngine(t *testing.T) {
	r, store := newTestSessionsRouter(t)
	fe := &fakeSendCapableEngine{fakeSessionEngine{
		fakeEngine: fakeEngine{name: "list-dispatch"},
		sessions:   []session.Info{{ID: "s1", MockID: "x", Protocol: "list-dispatch", RemoteAddr: "1.2.3.4:5"}},
	}}
	engine.Register(fe)
	def := createMock(t, store, "list-dispatch")

	rec := doJSON(t, r, http.MethodGet, "/api/mocks/"+def.ID+"/sessions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []session.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 1 || got[0].ID != "s1" {
		t.Fatalf("expected the fake engine's own session list back, got %+v", got)
	}
}

func TestSessionsListReturnsEmptyForANonSessionProtocol(t *testing.T) {
	r, store := newTestSessionsRouter(t)
	engine.Register(&fakeEngine{name: "no-sessions"}) // no SessionLister at all — e.g. REST/SOAP/GraphQL
	def := createMock(t, store, "no-sessions")

	rec := doJSON(t, r, http.MethodGet, "/api/mocks/"+def.ID+"/sessions", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got []session.Info
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(got) != 0 {
		t.Fatalf("expected an empty list, got %+v", got)
	}
}

func TestSessionsCloseDispatchesAndReportsNotFound(t *testing.T) {
	r, store := newTestSessionsRouter(t)
	fe := &fakeSendCapableEngine{fakeSessionEngine{fakeEngine: fakeEngine{name: "close-dispatch"}}}
	engine.Register(fe)
	def := createMock(t, store, "close-dispatch")

	rec := doJSON(t, r, http.MethodPost, "/api/mocks/"+def.ID+"/sessions/sess-1/close", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(fe.closedIDs) != 1 || fe.closedIDs[0] != "sess-1" {
		t.Fatalf("expected CloseSession called with %q, got %+v", "sess-1", fe.closedIDs)
	}

	fe.closeErr = errors.New("session not found")
	rec = doJSON(t, r, http.MethodPost, "/api/mocks/"+def.ID+"/sessions/gone/close", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a missing session, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSessionsSendDispatchesWithExtraFields(t *testing.T) {
	r, store := newTestSessionsRouter(t)
	fe := &fakeSendCapableEngine{fakeSessionEngine{fakeEngine: fakeEngine{name: "send-dispatch"}}}
	engine.Register(fe)
	def := createMock(t, store, "send-dispatch")

	body := map[string]any{"payload": "hello", "extra": map[string]string{"topic": "orders"}}
	rec := doJSON(t, r, http.MethodPost, "/api/mocks/"+def.ID+"/sessions/sess-1/send", body)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", rec.Code, rec.Body.String())
	}
	if len(fe.sentTo) != 1 || fe.sentTo[0] != "sess-1" || fe.sentBody != "hello" || fe.sentExtra["topic"] != "orders" {
		t.Fatalf("unexpected SendToSession call: sentTo=%v body=%q extra=%v", fe.sentTo, fe.sentBody, fe.sentExtra)
	}
}

// TestSessionsSendFailsForANonSendCapableProtocol covers SMTP/Kafka/
// Diameter: they satisfy SessionLister/SessionCloser but not SessionSender,
// so a send must fail cleanly (400) instead of silently doing nothing.
func TestSessionsSendFailsForANonSendCapableProtocol(t *testing.T) {
	r, store := newTestSessionsRouter(t)
	fe := &fakeSessionEngine{fakeEngine: fakeEngine{name: "view-only"}}
	engine.Register(fe)
	def := createMock(t, store, "view-only")

	rec := doJSON(t, r, http.MethodPost, "/api/mocks/"+def.ID+"/sessions/sess-1/send", map[string]any{"payload": "x"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-send-capable protocol, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSessionsRequestsFor404ForAnUnknownMockID(t *testing.T) {
	r, _ := newTestSessionsRouter(t)
	rec := doJSON(t, r, http.MethodGet, "/api/mocks/no-such-mock/sessions", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for an unknown mock id, got %d: %s", rec.Code, rec.Body.String())
	}
}
