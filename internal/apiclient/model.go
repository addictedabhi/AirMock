// Package apiclient is AirMock's built-in Postman-like API client: named
// collections of requests (organized into folders), environments of
// {{var}} substitutions, curl import/export, and Postman Collection v2.1
// as the interchange format for sharing collections outside AirMock.
package apiclient

import (
	"math/rand"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
)

type ItemType string

const (
	ItemFolder    ItemType = "folder"
	ItemRequest   ItemType = "request"
	ItemWSRequest ItemType = "wsrequest"
)

// KV is a header/query-param/form-field entry; Disabled lets a user keep a
// row around without it being sent, same as Postman's checkbox-to-disable
// pattern. Type/FileName only apply to a BodyMode=="formdata" FormFields
// entry — Type=="file" means Value holds the file's content base64-encoded
// (the request builder runs entirely in the browser, so this is the only
// way a file's bytes travel to the server inside the same JSON RequestSpec
// as everything else) rather than a real multipart upload straight from the
// browser. Header/query KVs and a "text" (or empty, the default) form field
// leave both fields empty and behave exactly as before.
type KV struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled,omitempty"`
	Type     string `json:"type,omitempty"`     // formdata only: "" or "text" (default), "file"
	FileName string `json:"fileName,omitempty"` // formdata file fields only
}

// Item is a collection tree node: a folder (with nested Items), a plain
// HTTP request, or a WebSocket request — never more than one of
// Items/Request/WSRequest populated, matching Type.
type Item struct {
	ID        string         `json:"id"`
	Type      ItemType       `json:"type"`
	Name      string         `json:"name"`
	Items     []Item         `json:"items,omitempty"`
	Request   *RequestSpec   `json:"request,omitempty"`
	WSRequest *WSRequestSpec `json:"wsRequest,omitempty"`
	// Examples are saved responses (Postman-style) — a request can be sent
	// once and its response kept as a named reference without needing to
	// hit the real endpoint again to see what a given case looks like.
	Examples []Example `json:"examples,omitempty"`
	// Description is free-text documentation for this item (Markdown-ish
	// plain text, rendered as-is) — what this request is for, quirks to
	// remember, anything worth writing down next to it rather than in a
	// separate doc that drifts out of sync.
	Description string `json:"description,omitempty"`
}

// Example is one saved response for a request Item.
type Example struct {
	ID         string            `json:"id"`
	Name       string            `json:"name"`
	StatusCode int               `json:"statusCode"`
	Headers    map[string]string `json:"headers,omitempty"`
	Body       string            `json:"body"`
}

type RequestSpec struct {
	Method  string `json:"method"`
	URL     string `json:"url"`
	Headers []KV   `json:"headers,omitempty"`
	Query   []KV   `json:"query,omitempty"`
	Body    string `json:"body,omitempty"`
	// BodyMode selects how Body/FormFields are sent: "" (the default,
	// meaning raw — Body sent as-is, RawContentType hints its Content-Type),
	// "none" (no body at all), "urlencoded" (FormFields as
	// application/x-www-form-urlencoded), or "formdata" (FormFields as
	// multipart/form-data).
	BodyMode string `json:"bodyMode,omitempty"`
	// RawContentType hints the Content-Type for BodyMode=="" (raw): "json"
	// (default when empty), "text", "xml", or "html".
	RawContentType string `json:"rawContentType,omitempty"`
	// FormFields holds the key/value pairs for BodyMode "urlencoded" or
	// "formdata". "formdata" fields can be plain text or a file (KV.Type==
	// "file", KV.Value holding the file's base64-encoded content) — file
	// fields are meaningless for "urlencoded", which has no concept of one.
	FormFields []KV `json:"formFields,omitempty"`
	// Auth, when set, is applied last — after Headers — so the Auth tab
	// always wins over a manually-typed Authorization header, matching
	// how Postman's dedicated Auth tab behaves.
	Auth *Auth `json:"auth,omitempty"`
	// ExtractRules, when set, are evaluated client-side against the
	// response body after this request runs (single send or as part of a
	// Collection Runner pass): each pulls one JSON field out of the
	// response and writes it into the active environment as a variable —
	// e.g. a login request extracting "token" so every later request's
	// {{token}} placeholder resolves to it automatically. Purely a
	// frontend-driven post-processing step; the server never evaluates
	// these itself, which is why there's no corresponding runner.go logic.
	ExtractRules []ExtractRule `json:"extractRules,omitempty"`
	// ExpectedStatus, when set (non-zero), is the pass/fail criterion the
	// Collection Runner (and a single send's pass/fail indicator) checks
	// the response status code against — an exact match required to pass.
	// Left at 0, the Runner falls back to a generic "2xx or 3xx and no
	// transport error" heuristic instead. Purely a frontend-evaluated
	// assertion, same as ExtractRules — the server never checks this.
	ExpectedStatus int `json:"expectedStatus,omitempty"`
	// Insecure, when true, skips TLS certificate verification for this
	// request (curl's -k/--insecure) — the one way to test a local/dev
	// HTTPS endpoint with a self-signed cert, which otherwise fails with no
	// workaround at all (the request builder previously had no such option,
	// and even recognized-but-ignored -k on curl import).
	Insecure bool `json:"insecure,omitempty"`
	// CookieJarKey, when set, makes Execute reuse a persistent cookie jar
	// across calls sharing the same key instead of a fresh cookie-less
	// client every time — the frontend sets this to the active environment
	// ID, so a login request's Set-Cookie response carries into later
	// requests run under the same environment, the same way a browser or
	// Postman's cookie jar behaves. Left empty (every existing caller/test),
	// Execute behaves exactly as before: no jar, no persistence.
	CookieJarKey string `json:"cookieJarKey,omitempty"`
	// ReadTimeoutSecs overrides Execute's default 30s http.Client timeout
	// for this one request — some real endpoints (a slow report generator,
	// a long-poll) legitimately take longer than 30s to respond, and
	// previously there was no way to test one without the request always
	// failing on a hardcoded timeout. 0 (every existing caller/test) means
	// "use the default"; ExecuteContext clamps the effective value to
	// MaxReadTimeoutSecs regardless of what's requested.
	ReadTimeoutSecs int `json:"readTimeoutSecs,omitempty"`
	// ClientCertID references a certificate in AirMock's own certificate
	// store to present as a TLS client certificate (mTLS) when calling an
	// endpoint that requires one. apiclient itself has no dependency on the
	// cert store — the API handler resolves this into ClientCertPEM/
	// ClientKeyPEM before Execute/RunLoadTest ever run, so this package
	// never needs to know how certificates are stored.
	ClientCertID string `json:"clientCertId,omitempty"`
	// ClientCertPEM/ClientKeyPEM carry the resolved certificate/key material
	// once ClientCertID has been looked up. Set only by the API handler,
	// never accepted directly from a request body (json:"-").
	ClientCertPEM string `json:"-"`
	ClientKeyPEM  string `json:"-"`
}

// ExtractRule pulls one field out of a JSON response body (Path is a
// dot-separated key path, e.g. "data.token") and writes it into the active
// environment under Variable.
type ExtractRule struct {
	Path     string `json:"path"`
	Variable string `json:"variable"`
}

type AuthType string

const (
	AuthNone   AuthType = "none"
	AuthBearer AuthType = "bearer"
	AuthBasic  AuthType = "basic"
	AuthAPIKey AuthType = "apikey"
)

// Auth describes one request's authorization, applied by Execute (and
// reflected into ToCurl) rather than requiring the user to hand-construct
// an Authorization header or query param themselves.
type Auth struct {
	Type     AuthType `json:"type"`
	Token    string   `json:"token,omitempty"`    // bearer
	Username string   `json:"username,omitempty"` // basic
	Password string   `json:"password,omitempty"` // basic
	KeyName  string   `json:"keyName,omitempty"`  // apikey
	KeyValue string   `json:"keyValue,omitempty"` // apikey
	AddTo    string   `json:"addTo,omitempty"`    // apikey only: "header" | "query"
}

type WSRequestSpec struct {
	URL     string `json:"url"`
	Headers []KV   `json:"headers,omitempty"`
	Message string `json:"message,omitempty"` // an initial message to send on connect, optional
}

// DefaultWorkspaceID is seeded by migration 000014 so every pre-existing
// (and any workspace-unaware caller's) collection/environment has somewhere
// to belong without the API rejecting an omitted workspaceId.
const DefaultWorkspaceID = "default"

// Workspace groups collections and environments into a separate namespace —
// e.g. one workspace per project or client — so a user working across
// several unrelated API surfaces isn't stuck with one long flat list of
// everything. Every install starts with exactly one, "Default".
type Workspace struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
	// Locked/LockCredentialType describe whether this workspace has its
	// own PIN/password, separate from the app's single admin login (see
	// internal/auth) — set via SetWorkspaceLock, checked by internal/
	// web/api's workspace-lock endpoints and the mocks handler before
	// modifying a mock mapped to this workspace (mock.Definition.
	// WorkspaceID). The bcrypt hash itself never leaves the store layer,
	// same "hash is server-internal only" convention as internal/auth's
	// own admin_auth table — it isn't a field on this struct at all.
	Locked             bool   `json:"locked"`
	LockCredentialType string `json:"lockCredentialType,omitempty"`
	// LockPinLength is the digit count of a PIN lock (0 for a password lock,
	// or while not yet known), so the unlock keypad knows when the PIN is
	// complete.
	LockPinLength int `json:"lockPinLength,omitempty"`
}

type Collection struct {
	ID          string `json:"id"`
	WorkspaceID string `json:"workspaceId"`
	Name        string `json:"name"`
	Items       []Item `json:"items"`
	// Variables are collection-scoped {{var}} values — resolved the same
	// way an Environment's Variables are (SubstituteVars doesn't care which
	// map a value came from), but scoped to this collection rather than
	// shared across every collection in a workspace. Mirrors Postman's own
	// collection-level `variable` array, which a real Postman collection
	// export commonly carries (e.g. a base URL every request in the
	// collection references) — previously silently discarded on import.
	Variables map[string]string `json:"variables,omitempty"`
	CreatedAt time.Time         `json:"createdAt"`
	UpdatedAt time.Time         `json:"updatedAt"`
}

type Environment struct {
	ID          string            `json:"id"`
	WorkspaceID string            `json:"workspaceId"`
	Name        string            `json:"name"`
	Variables   map[string]string `json:"variables"`
	CreatedAt   time.Time         `json:"createdAt"`
	UpdatedAt   time.Time         `json:"updatedAt"`
}

// SubstituteVars replaces every {{name}} in s with vars["name"], leaving
// unknown placeholders untouched so a typo is visible rather than silently
// swallowed into an empty string. It also resolves a small set of
// Postman-style dynamic variables (see DynamicVarNames) that need no
// environment set up at all — useful for idempotency keys, trace IDs, or
// cache-busting values that should be different on every send. Dynamic
// variables are resolved first so an environment variable can never
// accidentally shadow one (environment names aren't `$`-prefixed anyway).
func SubstituteVars(s string, vars map[string]string) string {
	if strings.Contains(s, "{{$") {
		s = resolveDynamicVars(s)
	}
	if len(vars) == 0 || !strings.Contains(s, "{{") {
		return s
	}
	for name, value := range vars {
		s = strings.ReplaceAll(s, "{{"+name+"}}", value)
	}
	return s
}

// DynamicVarNames lists every {{$...}} variable resolveDynamicVars
// understands, in the same order surfaced to the UI's "dynamic variables"
// hint — kept as one source of truth so the two can't drift apart.
var DynamicVarNames = []string{"timestamp", "isoTimestamp", "randomUUID", "randomInt"}

var dynamicVarPattern = regexp.MustCompile(`\{\{\$(\w+)\}\}`)

// resolveDynamicVars expands each {{$name}} independently — every
// reference gets its own freshly generated value, even multiple references
// to the same dynamic variable within one string, matching how Postman's
// own dynamic variables behave (they're generators, not stable values
// memoized across a request).
func resolveDynamicVars(s string) string {
	return dynamicVarPattern.ReplaceAllStringFunc(s, func(match string) string {
		switch match[3 : len(match)-2] { // strip leading "{{$" and trailing "}}"
		case "timestamp":
			return strconv.FormatInt(time.Now().Unix(), 10)
		case "isoTimestamp":
			return time.Now().UTC().Format(time.RFC3339)
		case "randomUUID":
			return uuid.NewString()
		case "randomInt":
			return strconv.Itoa(rand.Intn(1000))
		default:
			return match // unknown $name left untouched, same "typo stays visible" reasoning as environment vars
		}
	})
}

// EnsureScheme prepends defaultScheme (e.g. "http://", "ws://") to u if it
// doesn't already have one. Without this, a URL typed as just
// "localhost:8080/orders" (a completely natural thing to type, no scheme)
// fails with a confusing "unsupported protocol scheme \"localhost\"" —
// Go's URL parsing sees the colon and reads "localhost" itself as the
// scheme, not as a bare host:port.
func EnsureScheme(u, defaultScheme string) string {
	if strings.Contains(u, "://") {
		return u
	}
	return defaultScheme + u
}
