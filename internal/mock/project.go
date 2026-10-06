package mock

import "time"

// Project groups related mocks (REST/SOAP/GraphQL and TCP alike) under one
// named container, mirroring how apiclient.Workspace groups collections.
// Unlike a Workspace, a Project is purely organizational — mocks with no
// ProjectID still work exactly as before ("ungrouped"), and there's no
// seeded default every install must have.
type Project struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	// BasePath is the shared path prefix of the REST/SOAP/GraphQL/WS mocks in
	// this project. It is applied when a mock is saved: the UI pre-fills it
	// into a new mock's path, and the admin API (including "mocks apply")
	// prepends it to a path that does not already start with it. What is
	// registered on the gateway is each mock's own stored PathPattern, which
	// already includes the prefix, so changing BasePath later does not move
	// mocks that already exist.
	BasePath string `json:"basePath,omitempty"`
	// GatewayPort, when set, gives this project its own dedicated HTTP
	// listener — every REST/SOAP/GraphQL mock inside is served there
	// instead of the shared default gateway port, the same idea as each
	// TCP mock already owning its own listener. 0 (the default) means "use
	// the shared gateway port," same as before this field existed.
	GatewayPort int `json:"gatewayPort,omitempty"`
	// TLS, when set, wraps this project's own dedicated listener
	// (GatewayPort) in TLS — the same per-listener idea as TCPConfig.TLS on
	// a TCP mock, since a project with its own port is otherwise no
	// different from one. Only meaningful alongside a non-zero GatewayPort:
	// a project still sharing the default gateway gets its TLS (if any)
	// from the one shared gateway-wide setting on the Certificates page
	// instead, same as every other mock on that shared port.
	TLS *TCPTLSConfig `json:"tls,omitempty"`
	// WorkspaceID, when set, maps this whole project to a Collections
	// workspace — every mock inside it is then treated as if its own
	// WorkspaceID were also set to this, so locking the workspace protects
	// every mock in the project at once instead of requiring each one to be
	// mapped individually. A mock's own WorkspaceID (if set) is still
	// checked in addition, not instead — either one being locked is enough
	// to require unlocking it (see internal/web/api/mocks.go's
	// checkWorkspaceUnlocked call sites).
	WorkspaceID string    `json:"workspaceId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}
