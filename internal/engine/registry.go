// Package engine defines the protocol-plugin abstraction every mock
// listener (HTTP-family now; TCP/Telnet, Kafka/MQTT, SMTP/FTP/SMPP/Diameter
// in later phases) implements, plus a small registry so the server can
// dispatch mock CRUD operations to the right engine by protocol type without
// a compile-time switch statement.
package engine

import (
	"context"
	"fmt"
	"sync"

	"github.com/addictedabhi/airmock/internal/mock"
)

// ListenerConfig configures how an engine binds its listener(s).
type ListenerConfig struct {
	Addr string
}

// Engine is implemented by each protocol's mock listener.
type Engine interface {
	Name() string
	Start(ctx context.Context, cfg ListenerConfig) error
	Stop(ctx context.Context) error
	RegisterMock(m *mock.Definition) error
	UnregisterMock(id string) error
}

var (
	mu       sync.RWMutex
	registry = map[string]Engine{}
)

// Register makes an engine instance available under its own Name().
// Called once at server bootstrap for each engine the build supports.
func Register(e Engine) {
	mu.Lock()
	defer mu.Unlock()
	registry[e.Name()] = e
}

// Get returns the engine registered for a protocol name (e.g. "http").
func Get(name string) (Engine, bool) {
	mu.RLock()
	defer mu.RUnlock()
	e, ok := registry[name]
	return e, ok
}

// All returns every registered engine, for startup/shutdown fan-out.
func All() []Engine {
	mu.RLock()
	defer mu.RUnlock()
	out := make([]Engine, 0, len(registry))
	for _, e := range registry {
		out = append(out, e)
	}
	return out
}

// Dispatch registers a mock definition with the engine matching its
// ProtocolType, returning an error if no such engine is registered.
func Dispatch(m *mock.Definition) error {
	e, ok := Get(ProtocolFamily(m.ProtocolType))
	if !ok {
		return fmt.Errorf("no engine registered for protocol family of %q", m.ProtocolType)
	}
	return e.RegisterMock(m)
}

// DispatchUnregister removes a mock from the engine matching protocolType.
func DispatchUnregister(protocolType, id string) error {
	e, ok := Get(ProtocolFamily(protocolType))
	if !ok {
		return fmt.Errorf("no engine registered for protocol family of %q", protocolType)
	}
	return e.UnregisterMock(id)
}

// ProtocolFamily maps a mock's specific ProtocolType ("rest", "soap",
// "graphql", "ws", ...) to the Engine that hosts it ("http" for all four,
// since a WS handshake is itself just an HTTP GET with an Upgrade header —
// it shares the same net/http.Server as REST/SOAP/GraphQL rather than
// needing its own dedicated listener the way TCP/SMTP do). Exported so a
// caller changing a mock's ProtocolType on update (e.g. tcp -> rest) can
// tell whether the OLD engine needs an explicit UnregisterMock call — the
// new engine's own RegisterMock has no way to know a stale registration
// under the same ID is sitting in a completely different engine.
func ProtocolFamily(protocolType string) string {
	switch protocolType {
	case "rest", "soap", "graphql", "ws":
		return "http"
	default:
		return protocolType
	}
}
