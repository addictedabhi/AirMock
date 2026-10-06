// Package session is a generic, thread-safe live-connection tracker shared
// by every protocol engine that exposes "connected sessions" to the admin
// UI (TCP, WS, MQTT, SMTP, Kafka, SMPP, Diameter, JMS) — written once,
// parameterized over each engine's own connection/session value type
// (net.Conn, a protocol-specific *session struct, diam.Conn, ...), rather
// than once per engine. An engine embeds a *Registry[C] in place of
// whatever bare map[...]bool it used to track "a connection exists" with,
// gaining a stable per-connection ID, connect/activity timestamps, message
// counters, and free-form protocol-specific metadata for free.
package session

import (
	"sort"
	"sync"
	"time"

	"github.com/google/uuid"
)

// Info is the read-only, JSON-serializable view of one tracked session —
// everything the admin API/UI needs, with the engine-specific connection
// value itself deliberately left out (that only ever needs to be reached
// via Get, from inside the owning engine, to actually close or write to it).
type Info struct {
	ID           string            `json:"id"`
	MockID       string            `json:"mockId"`
	Protocol     string            `json:"protocol"`
	RemoteAddr   string            `json:"remoteAddr"`
	ConnectedAt  time.Time         `json:"connectedAt"`
	LastActiveAt time.Time         `json:"lastActiveAt"`
	MessagesIn   int64             `json:"messagesIn"`
	MessagesOut  int64             `json:"messagesOut"`
	// Meta carries whatever protocol-specific detail is worth showing —
	// MQTT's clientId/subscribed filters, SMPP's systemId, JMS's
	// destination/role/credit, Diameter's peer Origin-Host/Realm. Absent
	// entirely for protocols with nothing extra to show (TCP, WS, Kafka).
	Meta map[string]string `json:"meta,omitempty"`
}

type entry[C any] struct {
	info Info
	conn C
}

// Registry tracks every live connection for every mock that uses it,
// keyed first by mock ID (so listing/closing "this mock's sessions" never
// has to scan unrelated mocks) then by a generated session ID.
type Registry[C any] struct {
	mu     sync.RWMutex
	byMock map[string]map[string]*entry[C]
}

// NewRegistry constructs an empty registry for connection type C.
func NewRegistry[C any]() *Registry[C] {
	return &Registry[C]{byMock: map[string]map[string]*entry[C]{}}
}

// Add registers a newly-accepted connection and returns its generated
// session ID — call this once, right after a connection/session is
// established, before handing off to whatever per-connection read loop
// serves it.
func (r *Registry[C]) Add(mockID, protocol string, conn C, remoteAddr string, meta map[string]string) string {
	id := uuid.NewString()
	now := time.Now().UTC()
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.byMock[mockID] == nil {
		r.byMock[mockID] = map[string]*entry[C]{}
	}
	r.byMock[mockID][id] = &entry[C]{
		info: Info{
			ID: id, MockID: mockID, Protocol: protocol, RemoteAddr: remoteAddr,
			ConnectedAt: now, LastActiveAt: now, Meta: meta,
		},
		conn: conn,
	}
	return id
}

// Remove drops a session from the registry — call this once the
// connection's own read loop returns (defer, right alongside the
// connection's own Close), regardless of whether it was closed by the
// remote end, an idle timeout, or an operator-initiated CloseSession.
func (r *Registry[C]) Remove(mockID, id string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	delete(r.byMock[mockID], id)
	if len(r.byMock[mockID]) == 0 {
		delete(r.byMock, mockID)
	}
}

// Touch bumps a session's last-activity timestamp and message counters —
// call this from the connection's own read/write path as messages actually
// flow, so "connected sessions" reads as live traffic, not just a static
// connect-time snapshot. in/out are counts to ADD (usually 1), not new
// totals.
func (r *Registry[C]) Touch(mockID, id string, in, out int64) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byMock[mockID][id]
	if e == nil {
		return
	}
	e.info.LastActiveAt = time.Now().UTC()
	e.info.MessagesIn += in
	e.info.MessagesOut += out
}

// UpdateMeta merges keys into a session's Meta map — for protocol detail
// that isn't known yet at Add time (e.g. an MQTT client ID only arrives in
// the CONNECT packet's payload, parsed just after the connection itself is
// registered).
func (r *Registry[C]) UpdateMeta(mockID, id string, meta map[string]string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	e := r.byMock[mockID][id]
	if e == nil {
		return
	}
	if e.info.Meta == nil {
		e.info.Meta = map[string]string{}
	}
	for k, v := range meta {
		e.info.Meta[k] = v
	}
}

// List returns every session currently tracked for mockID, oldest
// connection first — a plain value-copy snapshot, safe to hand straight to
// an HTTP handler with no risk of a caller mutating live registry state.
func (r *Registry[C]) List(mockID string) []Info {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Info, 0, len(r.byMock[mockID]))
	for _, e := range r.byMock[mockID] {
		out = append(out, e.info)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ConnectedAt.Before(out[j].ConnectedAt) })
	return out
}

// Get returns the underlying engine-specific connection value for one
// session, for CloseSession/SendToSession to actually act on — ok is false
// if that mock/id pair isn't currently tracked (already disconnected, or
// never existed).
func (r *Registry[C]) Get(mockID, id string) (conn C, ok bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	e := r.byMock[mockID][id]
	if e == nil {
		var zero C
		return zero, false
	}
	return e.conn, true
}

// All returns every (id, conn) pair currently tracked for mockID — for an
// engine that needs to act on every live connection itself (e.g. MQTT's
// topic-matching broadcast, which has to check each session's own
// subscriptions), rather than just listing Info metadata via List.
func (r *Registry[C]) All(mockID string) map[string]C {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make(map[string]C, len(r.byMock[mockID]))
	for id, e := range r.byMock[mockID] {
		out[id] = e.conn
	}
	return out
}

// CloseAll removes and closes every session tracked for mockID, calling
// closeFn once per connection — mirrors every engine's existing
// closeConnsLocked helper (called on mock re-registration/unregistration),
// now backed by this shared registry instead of a bare map.
func (r *Registry[C]) CloseAll(mockID string, closeFn func(C)) {
	r.mu.Lock()
	entries := r.byMock[mockID]
	delete(r.byMock, mockID)
	r.mu.Unlock()
	for _, e := range entries {
		closeFn(e.conn)
	}
}
