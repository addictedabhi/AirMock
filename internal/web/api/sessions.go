package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/session"
)

// SessionLister is the narrow slice of a protocol engine this handler needs
// to list a mock's currently-connected sessions — implemented by every
// session-tracking engine (TCP, WS, MQTT, SMTP, Kafka, SMPP, Diameter, JMS),
// none of which is otherwise reachable through the shared engine.Engine
// interface (see each engine's own ListSessions doc comment).
type SessionLister interface {
	ListSessions(mockID string) []session.Info
}

// SessionCloser additionally lets an operator forcibly disconnect one
// session — implemented by the same 8 engines as SessionLister.
type SessionCloser interface {
	CloseSession(mockID, sessionID string) error
}

// SessionSender additionally lets an operator push an unsolicited message
// to one targeted session — only implemented by the 5 protocols where
// that's a genuine operation (TCP, WS, MQTT, SMPP, JMS); SMTP/Kafka/
// Diameter satisfy SessionLister/SessionCloser but not this, so a send
// against one of those cleanly fails the type assertion below rather than
// silently doing nothing.
type SessionSender interface {
	SessionCloser
	SendToSession(mockID, sessionID, payload string, extra map[string]string) error
}

type SessionsHandler struct {
	store *mock.Store
}

func NewSessionsHandler(store *mock.Store) *SessionsHandler {
	return &SessionsHandler{store: store}
}

func (h *SessionsHandler) Routes(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/{sessionId}/close", h.close)
	r.Post("/{sessionId}/send", h.send)
}

// engineFor resolves the engine hosting mockID by looking up its own
// ProtocolType and dispatching through engine.ProtocolFamily — the same
// resolution mocks.go already uses for CRUD, reused here rather than
// inventing a second mock-ID-to-engine lookup.
func (h *SessionsHandler) engineFor(mockID string) (engine.Engine, error) {
	def, err := h.store.Get(mockID)
	if err != nil {
		return nil, err
	}
	e, ok := engine.Get(engine.ProtocolFamily(def.ProtocolType))
	if !ok {
		return nil, fmt.Errorf("no engine registered for protocol family of %q", def.ProtocolType)
	}
	return e, nil
}

func (h *SessionsHandler) list(w http.ResponseWriter, r *http.Request) {
	mockID := chi.URLParam(r, "id")
	e, err := h.engineFor(mockID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	lister, ok := e.(SessionLister)
	if !ok {
		// REST/SOAP/GraphQL and any other request-response protocol has no
		// notion of a connected session at all — an empty list, not an
		// error, since "no sessions" is the correct answer for that page.
		writeJSON(w, http.StatusOK, []session.Info{})
		return
	}
	writeJSON(w, http.StatusOK, lister.ListSessions(mockID))
}

func (h *SessionsHandler) close(w http.ResponseWriter, r *http.Request) {
	mockID := chi.URLParam(r, "id")
	sessionID := chi.URLParam(r, "sessionId")
	e, err := h.engineFor(mockID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	closer, ok := e.(SessionCloser)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("this protocol has no connected sessions"))
		return
	}
	if err := closer.CloseSession(mockID, sessionID); err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// sendSessionBody's Extra carries whatever the targeted protocol's own
// SendToSession needs beyond a plain payload string — MQTT's "topic",
// SMPP's "sourceAddr"/"destAddr", JMS's "address" — deliberately generic
// rather than a fixed per-protocol field list, since each protocol's own
// UI panel is what actually knows which keys to send.
type sendSessionBody struct {
	Payload string            `json:"payload"`
	Extra   map[string]string `json:"extra,omitempty"`
}

func (h *SessionsHandler) send(w http.ResponseWriter, r *http.Request) {
	mockID := chi.URLParam(r, "id")
	sessionID := chi.URLParam(r, "sessionId")
	var body sendSessionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	e, err := h.engineFor(mockID)
	if err != nil {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	sender, ok := e.(SessionSender)
	if !ok {
		writeErr(w, http.StatusBadRequest, errors.New("this protocol has no send-to-session support"))
		return
	}
	if err := sender.SendToSession(mockID, sessionID, body.Payload, body.Extra); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
