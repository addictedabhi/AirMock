package mock

import (
	"time"

	"github.com/addictedabhi/airmock/internal/validate"
)

// Definition is a configured mock endpoint. Later phases (validation,
// response rules, scenarios, fault injection) extend this struct and its
// own migrations without touching this file's existing fields.
type Definition struct {
	ID           string           `json:"id"`
	Name         string           `json:"name"`
	ProtocolType string           `json:"protocolType"` // "rest" | "soap"
	Method       string           `json:"method"`
	PathPattern  string           `json:"pathPattern"` // chi-style, e.g. "/hello/{name}"; the SOAP service endpoint path for protocolType=="soap"
	Enabled      bool             `json:"enabled"`
	Mode         string           `json:"mode"` // "sync" (default) | "async" | "proxy"
	Response     ResponseTemplate `json:"response"`
	// Proxy configures Mode=="proxy": forward the request to a real
	// upstream, return its real response unchanged, and capture the
	// exchange to the hit log (see internal/hitlog) tagged proxy-capture —
	// "point this at the real API for a bit" so real traffic can later be
	// promoted into hand-authored mocks/collection entries.
	Proxy *ProxyConfig `json:"proxy,omitempty"`
	// SOAPAction and OperationName disambiguate which SOAP mock on a shared
	// PathPattern a request is for (protocolType=="soap" only) — multiple
	// operations of one service share one POST endpoint, matched by the
	// client's SOAPAction header or (if absent) the envelope body's root
	// element/operation name.
	SOAPAction    string `json:"soapAction,omitempty"`
	OperationName string `json:"operationName,omitempty"`
	// ResponseRules are evaluated first-match-wins, ahead of Response, for
	// sync mocks — Response acts as the default when no rule matches (or
	// there are no rules at all).
	ResponseRules []ResponseRule `json:"responseRules,omitempty"`
	AsyncConfig   *AsyncConfig   `json:"asyncConfig,omitempty"`
	// Scenario, when set, replaces both Response and ResponseRules: each
	// hit from a given session advances a persisted per-session step
	// counter and serves that step's template — content-based branching
	// (ResponseRules) and sequence-based branching (Scenario) are kept as
	// two distinct mechanisms rather than composed, to keep v1 semantics
	// unambiguous about which one wins.
	Scenario *ScenarioConfig `json:"scenario,omitempty"`
	// Weighted, when set (and non-empty), overrides ResponseRules/Response
	// for sync REST/SOAP/GraphQL mocks the same way Scenario does — see
	// mock.SelectResponse for the exact precedence between the three.
	Weighted   *WeightedConfig `json:"weighted,omitempty"`
	Validation []validate.Rule `json:"validation,omitempty"`
	// Fault, when set, can make ANY mock (sync, async, or scenario) flaky —
	// applied in the one shared response-writing function so every response
	// path gets it for free rather than needing its own fault-injection call.
	Fault *FaultConfig `json:"fault,omitempty"`
	// ValidationErrorResponse overrides the default 400 + JSON error list
	// returned when Validation rules fail, so a mock can mimic a specific
	// target API's real validation-error contract.
	ValidationErrorResponse *ResponseTemplate `json:"validationErrorResponse,omitempty"`
	// TCP configures protocolType=="tcp" mocks — a raw line-oriented listener
	// (Telnet-style) rather than an HTTP-family endpoint. Unlike REST/SOAP/
	// GraphQL, which all share one gateway port disambiguated by path/
	// method/operation, a TCP mock owns its own dedicated listener port,
	// since raw TCP has no equivalent of an HTTP path to multiplex on.
	TCP *TCPConfig `json:"tcp,omitempty"`
	// SMTP configures protocolType=="smtp" mocks — a real inbound SMTP
	// listener (the mirror image of an async mock's "send email" callback
	// channel), for testing an application's own outbound-email code
	// against a fake receiving server instead of a real mailbox. Like TCP,
	// it owns its own dedicated listener port.
	SMTP *SMTPConfig `json:"smtp,omitempty"`
	// WS configures protocolType=="ws" mocks — a WebSocket endpoint that
	// shares the gateway's HTTP server/port like REST/SOAP/GraphQL (a WS
	// handshake is itself just an HTTP GET with an Upgrade header), unlike
	// TCP/SMTP which each need a dedicated listener port.
	WS *WSConfig `json:"ws,omitempty"`
	// MQTT configures protocolType=="mqtt" mocks — a minimal MQTT 3.1.1
	// broker (CONNECT/SUBSCRIBE/PUBLISH/PING/DISCONNECT) for testing a
	// client's own publish and/or subscribe code against a fake broker.
	// Like TCP/SMTP, it owns its own dedicated listener port.
	MQTT *MQTTConfig `json:"mqtt,omitempty"`
	// FTP configures protocolType=="ftp" mocks — a minimal FTP server
	// (USER/PASS, PASV-mode LIST/RETR/STOR) for testing a client's own
	// file-transfer code against a fake server instead of a real one.
	// Like TCP/SMTP/MQTT, it owns its own dedicated listener port.
	FTP *FTPConfig `json:"ftp,omitempty"`
	// Kafka configures protocolType=="kafka" mocks — a minimal single-node
	// Kafka broker (ApiVersions/Metadata/Produce/Fetch only — no consumer
	// groups, transactions, or compression) for testing a client's own
	// produce and/or consume code against a fake broker instead of a real
	// cluster. Like TCP/SMTP/MQTT/FTP, it owns its own dedicated listener
	// port.
	Kafka *KafkaConfig `json:"kafka,omitempty"`
	// SMPP configures protocolType=="smpp" mocks — a minimal SMPP v3.4 SMSC
	// (bind_transceiver, submit_sm/submit_sm_resp, deliver_sm/
	// deliver_sm_resp, enquire_link/enquire_link_resp, unbind — no
	// submit_multi, query_sm, replace_sm, or data_sm) for testing a client's
	// own SMS-sending/receiving code against a fake SMSC instead of a real
	// telecom connection. Like TCP/SMTP/MQTT/FTP/Kafka, it owns its own
	// dedicated listener port.
	SMPP *SMPPConfig `json:"smpp,omitempty"`
	// Diameter configures protocolType=="diameter" mocks — a minimal
	// Diameter peer (RFC 6733 CER/CEA capabilities-exchange handshake and
	// DWR/DWA watchdog, both handled automatically by the underlying
	// state machine, plus Credit-Control-Request/Answer for RFC 4006's
	// Credit-Control application — no other applications, no grouped
	// AVPs beyond what CCR/CCA already require) for testing a client's
	// own charging/credit-control integration (Gx/Gy-style) against a
	// fake peer instead of a real PCRF/OCS. Like TCP/SMTP/MQTT/FTP/Kafka/
	// SMPP, it owns its own dedicated listener port.
	Diameter *DiameterConfig `json:"diameter,omitempty"`
	// JMS configures protocolType=="jms" mocks — a minimal AMQP 1.0 peer
	// (protocol header exchange, open/begin/attach/flow/transfer/
	// disposition/detach/end/close — no SASL/auth, no transactions, no
	// link resumption) for testing a client's own JMS-style produce/
	// consume code (via a JMS provider that speaks AMQP 1.0, e.g. Qpid
	// JMS or ActiveMQ Artemis's AMQP connector) against a fake broker
	// instead of a real one. Like TCP/SMTP/MQTT/FTP/Kafka/SMPP/Diameter,
	// it owns its own dedicated listener port.
	JMS *JMSConfig `json:"jms,omitempty"`
	// ProjectID groups this mock under a Project (see project.go) for
	// organization only — it has no effect on gateway routing. A mock with
	// no ProjectID is "ungrouped" and shown outside any project in the UI.
	ProjectID string `json:"projectId,omitempty"`
	// WorkspaceID, when set, maps this mock to a Collections workspace
	// (apiclient.Workspace) — if that workspace is locked, editing,
	// deleting, or reassigning this mock's WorkspaceID requires that
	// workspace's password for the current browser (see internal/wslock).
	// Unlike ProjectID (REST/SOAP/GraphQL-only, purely organizational),
	// this applies to every protocol and exists purely to gate
	// modification, not for organization.
	WorkspaceID string    `json:"workspaceId,omitempty"`
	CreatedAt   time.Time `json:"createdAt"`
	UpdatedAt   time.Time `json:"updatedAt"`
}

// AsyncConfig describes an async mock's immediate ack response and the
// later callback it fires: to a fixed URL, or one extracted from the
// triggering request (e.g. "body.callbackUrl", "header.X-Callback-Url").
type AsyncConfig struct {
	AckResponse         ResponseTemplate `json:"ackResponse"`
	CallbackDelayMs     int              `json:"callbackDelayMs,omitempty"`
	CallbackTargetMode  string           `json:"callbackTargetMode"` // "fixed" | "extracted"
	CallbackFixedURL    string           `json:"callbackFixedUrl,omitempty"`
	CallbackExtractPath string           `json:"callbackExtractPath,omitempty"`
	// CallbackChannel selects how the callback is delivered: "" or "http"
	// (default) POSTs (or CallbackMethod) CallbackBodyTemplate to the
	// resolved target as a URL; "email" sends it as an HTML email through
	// the configured SMTP relay instead, with the SAME
	// CallbackTargetMode/CallbackFixedURL/CallbackExtractPath resolving an
	// email address rather than a URL — reusing the exact same "fixed or
	// extracted from the request" mechanism rather than inventing a
	// parallel one just for email.
	CallbackChannel      string            `json:"callbackChannel,omitempty"`
	CallbackMethod       string            `json:"callbackMethod,omitempty"` // default POST; http channel only
	CallbackHeaders      map[string]string `json:"callbackHeaders,omitempty"`
	CallbackBodyTemplate string            `json:"callbackBodyTemplate,omitempty"`
	MaxAttempts          int               `json:"maxAttempts,omitempty"` // default 3

	// EmailSubjectTemplate and EmailTemplateID apply only when
	// CallbackChannel=="email". EmailTemplateID, when set, selects a saved
	// internal/smtp.Template by id for the subject/HTML body instead of
	// EmailSubjectTemplate/CallbackBodyTemplate — reusing one named
	// template across many mocks rather than repeating the same HTML on
	// each. Both the subject and the (CallbackBodyTemplate-or-template)
	// body are rendered through the same text/template+sprig engine as
	// every other response body, with {{placeholder}}s resolved from the
	// triggering request.
	EmailSubjectTemplate string `json:"emailSubjectTemplate,omitempty"`
	EmailTemplateID      string `json:"emailTemplateId,omitempty"`
}

// ScenarioConfig is an ordered sequence of responses served one-per-hit,
// per session (e.g. an order-status mock returning pending -> shipped ->
// delivered across successive polls from the same client).
type ScenarioConfig struct {
	Steps []ResponseTemplate `json:"steps"`
	// SessionKeyMode selects how a request is attributed to a session:
	// "ip" (default, uses the client's remote address), "header", "query",
	// or "body" (the latter two via SessionKeyField, same ExtractField
	// convention used everywhere else).
	SessionKeyMode  string `json:"sessionKeyMode,omitempty"`
	SessionKeyField string `json:"sessionKeyField,omitempty"`
	// Loop: after the last step, wrap back to step 0 (true) or keep
	// serving the last step forever (false, the default — "clamp").
	Loop bool `json:"loop,omitempty"`
}

// FaultConfig injects chaos ahead of an otherwise-normal response: extra
// random latency, a probability of returning an error status instead of
// the real response, and a probability of simulating a dropped/timed-out
// connection instead of responding at all — useful for testing how a real
// client app degrades against an unreliable backend.
type FaultConfig struct {
	LatencyJitterMs    int     `json:"latencyJitterMs,omitempty"`    // adds a random 0..N ms delay to every response
	ErrorRatePercent   float64 `json:"errorRatePercent,omitempty"`   // 0-100 chance of substituting an error status
	ErrorStatusCodes   []int   `json:"errorStatusCodes,omitempty"`   // one is chosen at random when an error fires; defaults to 500
	TimeoutRatePercent float64 `json:"timeoutRatePercent,omitempty"` // 0-100 chance of dropping the connection instead of responding
}

// ProxyConfig is Mode=="proxy"'s only setting: the upstream to forward to.
// PathPattern for a proxy mock is expected to be a chi wildcard route
// (e.g. "/proxy/*") — the wildcard remainder is appended to TargetBaseURL
// so the mock transparently forwards an entire upstream path space, not
// just one fixed endpoint.
type ProxyConfig struct {
	TargetBaseURL string `json:"targetBaseUrl"`
}

// TCPConfig describes a raw TCP/Telnet mock: an optional banner sent right
// after connect, then a read-a-line/match/respond loop for the life of the
// connection. Each line read is matched against Interactions in order
// (first match wins); DefaultResponse is used when nothing matches.
type TCPConfig struct {
	Port int `json:"port"`
	// Banner, if set, is written immediately after a client connects,
	// before anything is read — the classic Telnet "220 Welcome" greeting.
	Banner string `json:"banner,omitempty"`
	// LineDelimiter is the byte sequence that ends one line of input;
	// defaults to "\n" if empty. May be multi-byte (e.g. "\r\n"). When it's
	// exactly "\n", a trailing "\r" is also trimmed from the matched line
	// for robustness against clients that send CRLF regardless.
	LineDelimiter string           `json:"lineDelimiter,omitempty"`
	Interactions  []TCPInteraction `json:"interactions,omitempty"`
	// DefaultResponse is rendered (via the same text/template+sprig engine
	// as HTTP response bodies, with {{.Request.Body}} bound to the received
	// line) when no Interaction matches; left empty, unmatched input gets
	// no reply at all, same as many real line-oriented servers ignoring
	// garbage input rather than erroring on it.
	DefaultResponse string `json:"defaultResponse,omitempty"`
	// Login, when set, gates Interactions behind a username/password
	// prompt exchanged right after Banner — simulating a credential-gated
	// line service rather than an open one.
	Login *TCPLoginConfig `json:"login,omitempty"`
	// SessionTimeoutSecs closes the connection if no line arrives within
	// this many seconds of the last read (login prompts included); 0 (the
	// default) means no idle timeout.
	SessionTimeoutSecs int `json:"sessionTimeoutSecs,omitempty"`
	// ResponseDelayMs adds a fixed delay before writing an Interaction/
	// DefaultResponse reply (not the Banner or Login prompts) — simulates
	// a slow server.
	ResponseDelayMs int `json:"responseDelayMs,omitempty"`
	// TLS, when set, wraps this mock's own dedicated listener in TLS using
	// a certificate from the certificate store — unlike the HTTP gateway,
	// where TLS is one shared setting for every HTTP-family mock, each TCP
	// mock already has its own port, so TLS here is genuinely per-mock.
	TLS *TCPTLSConfig `json:"tls,omitempty"`
}

// TCPLoginConfig is one username/password check, tried up to MaxAttempts
// times before the connection is closed. Prompts/messages default to
// generic Telnet-style text when left empty.
type TCPLoginConfig struct {
	// Mode selects the shape of the login exchange: "" (the default) is a
	// real interactive telnet-style prompt sequence — write
	// UsernamePrompt, read a line, write PasswordPrompt, read a line.
	// "line" instead expects the client to send its own single-line login
	// command (e.g. "LOGIN:admin:secret") matching LineFormat — for a
	// scripted/automated client whose own protocol already has a login
	// command, rather than a human answering two separate prompts.
	Mode           string `json:"mode,omitempty"`
	UsernamePrompt string `json:"usernamePrompt,omitempty"` // default "Username: "; interactive mode only — unused in "line" mode, see LineHint
	PasswordPrompt string `json:"passwordPrompt,omitempty"` // default "Password: "; interactive mode only — unused in "line" mode
	// LineHint is "line" mode's own optional one-time hint, sent once
	// before reading the client's login line — kept as its own field
	// rather than reusing UsernamePrompt so switching a mock's Login
	// style back and forth (or having both fields visible in the UI at
	// once) can never silently overwrite one mode's text with the
	// other's; there's nothing to prompt for a second time in "line"
	// mode, so this is a hint, not a real prompt. Unused outside "line"
	// mode.
	LineHint string `json:"lineHint,omitempty"`
	Username string `json:"username"`
	Password string `json:"password"`
	// LineFormat is a plain-text template of the client's login line,
	// written the way it actually looks on the wire, with the literal
	// placeholders {username} and {password} standing in for the values —
	// no regular expression knowledge required. Example: to match
	// "LOGIN:admin:secret", write "LOGIN:{username}:{password}". Each
	// placeholder must appear exactly once; everything else is matched
	// literally. A template that's missing a placeholder, fails to build,
	// or simply doesn't match the received line is treated the same as
	// wrong credentials (another attempt, up to MaxAttempts), not a fatal
	// error. Unused outside "line" mode.
	LineFormat     string `json:"lineFormat,omitempty"`
	SuccessMessage string `json:"successMessage,omitempty"`
	FailureMessage string `json:"failureMessage,omitempty"`
	MaxAttempts    int    `json:"maxAttempts,omitempty"` // default 3
}

// TCPTLSConfig enables TLS on a TCP or SMTP mock's listener, a Mock
// Project's dedicated port, or (via certs.GatewaySettings, which mirrors
// these same field names/semantics) the shared HTTP gateway. CertificateID/
// ClientCAID can be set directly for a one-off cert not part of any group,
// or BundleID can be set instead to resolve both at once from a named
// certs.Bundle (a CA + server cert + client cert grouped together) — when
// both are set, the bundle's own CertID/ClientCAID take precedence, since
// picking a bundle is meant to replace picking the two individually.
// ClientCertMode governs client-certificate verification (see
// certs.ClientCertMode) — "" is treated the same as ClientCertNone.
type TCPTLSConfig struct {
	CertificateID  string `json:"certificateId,omitempty"`
	BundleID       string `json:"bundleId,omitempty"`
	ClientCertMode string `json:"clientCertMode,omitempty"`
	ClientCAID     string `json:"clientCaId,omitempty"`
}

// TCPInteraction is one line-in/response-out rule within a TCP mock.
type TCPInteraction struct {
	Match string `json:"match"`
	// MatchType: "contains" (default), "exact", or "regex".
	MatchType string `json:"matchType,omitempty"`
	// Response is rendered via text/template+sprig, with {{.Request.Body}}
	// bound to the received line.
	Response string `json:"response"`
	// CloseAfter closes the connection right after this response is
	// written — useful for simulating a server that hangs up after an
	// error or a terminal command like "quit"/"bye".
	CloseAfter bool `json:"closeAfter,omitempty"`
	// Async, when set, additionally schedules a durable callback (HTTP
	// webhook or email — the exact same AsyncConfig/CallbackWorker
	// machinery a REST async mock uses) after this interaction's Response
	// is written over the TCP connection — e.g. a "PLACE_ORDER" line
	// triggers an immediate TCP reply plus a later out-of-band API call or
	// email, the way a real backend might ack a command synchronously but
	// notify some other system asynchronously. CallbackTargetMode/
	// CallbackFixedURL/CallbackExtractPath resolve the same "fixed or
	// extracted from the request" way as REST — extraction is against the
	// received line, so a "body.field" path only works if the line itself
	// is JSON. AckResponse is unused here (this interaction's own Response
	// already serves that role).
	Async *AsyncConfig `json:"async,omitempty"`
}

// SMTPConfig describes a mock SMTP server: an inbound-mail listener that
// speaks just enough real SMTP (EHLO/MAIL FROM/RCPT TO/DATA/QUIT) to accept
// a message, evaluates it against Rules, and accepts or rejects it — for
// testing an application's own outbound-email code against a fake receiving
// server rather than a real mailbox. This is the mirror image of an async
// mock's "email" callback channel (which SENDS mail); this RECEIVES it.
type SMTPConfig struct {
	Port int `json:"port"`
	// Hostname is used in the 220 banner and EHLO response; defaults to
	// "airmock" when empty.
	Hostname string `json:"hostname,omitempty"`
	// Banner, if set, replaces the default "220 <hostname> ESMTP AirMock"
	// greeting entirely.
	Banner string `json:"banner,omitempty"`
	// Rules are evaluated first-match-wins, once the full message
	// (envelope + DATA) has been received, against MatchField's value.
	// DefaultAccept decides the outcome when nothing matches.
	Rules         []SMTPRule `json:"rules,omitempty"`
	DefaultAccept bool       `json:"defaultAccept"`
	// TLS, when set, wraps the listener in implicit TLS (like a real
	// port-465 relay) using a certificate from the store — not inline
	// STARTTLS — same per-mock-listener shape as a TCP mock's TLS, since
	// each SMTP mock likewise owns its own dedicated port.
	TLS *TCPTLSConfig `json:"tls,omitempty"`
}

// SMTPRule is one accept/reject rule evaluated against a received message.
type SMTPRule struct {
	// MatchField: "from" | "to" | "subject" | "body".
	MatchField string `json:"matchField"`
	Match      string `json:"match"`
	// MatchType: "contains" (default), "exact", or "regex".
	MatchType string `json:"matchType,omitempty"`
	Accept    bool   `json:"accept"`
	// ResponseCode defaults to 250 when Accept, 550 when not.
	ResponseCode    int    `json:"responseCode,omitempty"`
	ResponseMessage string `json:"responseMessage,omitempty"`
}

// WSConfig describes a mock WebSocket endpoint: after the handshake,
// OnConnectMessage (if set) is sent immediately, then every inbound text
// message is matched against Interactions (first match wins, same
// contains/exact/regex semantics as TCPInteraction) and answered with the
// matched Response — or DefaultResponse if nothing matches, or nothing at
// all if that's empty too, mirroring how TCPConfig.DefaultResponse works.
// Every template is rendered with {{.Request.Body}} bound to the received
// message (parsed as JSON when possible, same as an HTTP body) alongside
// the connection's original {{.Request.Query}}/{{.Request.Header}}/
// {{.Request.PathParams}} from the upgrade request.
type WSConfig struct {
	OnConnectMessage string          `json:"onConnectMessage,omitempty"`
	Interactions     []WSInteraction `json:"interactions,omitempty"`
	DefaultResponse  string          `json:"defaultResponse,omitempty"`
}

type WSInteraction struct {
	Match string `json:"match"`
	// MatchType: "contains" (default), "exact", or "regex".
	MatchType string `json:"matchType,omitempty"`
	Response  string `json:"response"`
	// CloseAfter closes the connection right after this response is sent —
	// useful for simulating a server-initiated close on a specific message
	// (e.g. a "logout"/"bye" command).
	CloseAfter bool `json:"closeAfter,omitempty"`
	// Async, when set, additionally schedules a durable callback (HTTP
	// webhook or email — the exact same AsyncConfig/CallbackWorker
	// machinery a REST async mock or a TCPInteraction.Async uses) after
	// this interaction's Response is sent over the WebSocket connection —
	// e.g. an "order placed" message triggers an immediate WS reply plus a
	// later out-of-band API call or email, mirroring TCPInteraction.Async's
	// exact same use case for a message-oriented connection instead of a
	// line-oriented one. CallbackTargetMode/CallbackFixedURL/
	// CallbackExtractPath resolve the same "fixed or extracted from the
	// request" way as REST/TCP — extraction is against the received
	// message, so a "body.field" path only works if the message itself is
	// JSON. AckResponse is unused here (this interaction's own Response
	// already serves that role).
	Async *AsyncConfig `json:"async,omitempty"`
}

// MQTTConfig describes a mock MQTT broker: accepts any CONNECT, ACKs
// SUBSCRIBE/UNSUBSCRIBE/PING, and evaluates every inbound PUBLISH against
// Rules (first-match-wins). A matched rule with ReplyTopic set publishes a
// reply to every connected session currently subscribed to a filter
// matching ReplyTopic — including the publisher's own connection, if it
// also subscribed there — the same "app publishes a request, gets a
// correlated reply on another topic" pattern a real broker enables, just
// without needing a real backend behind it.
type MQTTConfig struct {
	Port  int        `json:"port"`
	Rules []MQTTRule `json:"rules,omitempty"`
}

type MQTTRule struct {
	// TopicPattern is an MQTT topic filter (supports the standard "+"
	// single-level and "#" multi-level wildcards) matched against the
	// topic a client PUBLISHes to.
	TopicPattern string `json:"topicPattern"`
	// PayloadMatch, if set, additionally requires the published payload to
	// match (per MatchType); left empty, the rule matches on topic alone.
	PayloadMatch string `json:"payloadMatch,omitempty"`
	// MatchType: "contains" (default), "exact", or "regex" — applies to
	// PayloadMatch only.
	MatchType string `json:"matchType,omitempty"`
	// ReplyTopic/ReplyPayload, if ReplyTopic is set, publish a reply
	// (QoS 0) to every session subscribed to a filter matching it.
	// ReplyPayload is rendered via text/template+sprig with
	// {{.Request.Body}} bound to the received payload.
	ReplyTopic   string `json:"replyTopic,omitempty"`
	ReplyPayload string `json:"replyPayload,omitempty"`
}

// KafkaConfig describes a mock Kafka broker: answers ApiVersions/Metadata
// so a real client library can discover it, accepts Produce onto any topic
// name (created implicitly on first reference, single partition 0, no
// persistence beyond the process lifetime), and serves Fetch requests back
// from an in-memory per-topic log. Every inbound Produce is additionally
// evaluated against Rules (first-match-wins); a matched rule with
// ReplyTopic set appends a reply record onto that topic's own log, so a
// consumer Fetching it (even one that only starts polling afterward) can
// read the correlated reply — the same "produce a request, consume a
// correlated reply from another topic" pattern MQTTConfig gives for pub/sub,
// adapted to Kafka's durable-log-instead-of-broadcast semantics.
type KafkaConfig struct {
	Port  int         `json:"port"`
	Rules []KafkaRule `json:"rules,omitempty"`
}

type KafkaRule struct {
	// TopicPattern is matched exactly against the topic a client Produces
	// to — Kafka topics are plain names, not filters, so unlike MQTT there
	// are no wildcards here.
	TopicPattern string `json:"topicPattern"`
	// PayloadMatch, if set, additionally requires the produced record's
	// value to match (per MatchType); left empty, the rule matches on
	// topic alone.
	PayloadMatch string `json:"payloadMatch,omitempty"`
	// MatchType: "contains" (default), "exact", or "regex" — applies to
	// PayloadMatch only.
	MatchType string `json:"matchType,omitempty"`
	// ReplyTopic/ReplyPayload, if ReplyTopic is set, append one reply
	// record onto that topic's log. ReplyPayload is rendered via
	// text/template+sprig with {{.Request.Body}} bound to the produced
	// record's value.
	ReplyTopic   string `json:"replyTopic,omitempty"`
	ReplyPayload string `json:"replyPayload,omitempty"`
}

// SMPPConfig describes a mock SMPP SMSC: accepts a bind_transceiver (any
// credentials, or a specific SystemID/Password if configured), acks
// enquire_link, and evaluates every inbound submit_sm against Rules
// (first-match-wins). A matched rule with ReplyMessage set sends one
// deliver_sm back down the SAME connection after the submit_sm_resp — the
// SMPP equivalent of MQTT/Kafka's "reply on another channel," just that for
// SMPP there's only ever the one bidirectional session to reply on.
type SMPPConfig struct {
	Port int `json:"port"`
	// SystemID/Password, if set, must match exactly for bind_transceiver to
	// succeed; left blank, any credentials are accepted.
	SystemID string     `json:"systemId,omitempty"`
	Password string     `json:"password,omitempty"`
	Rules    []SMPPRule `json:"rules,omitempty"`
}

type SMPPRule struct {
	// DestAddrPattern, if set, must match submit_sm's destination_addr per
	// DestAddrMatchType; left blank, the rule matches any destination. It may
	// list comma-separated alternatives (not for regex), and a leading "+" is
	// ignored on both sides.
	DestAddrPattern string `json:"destAddrPattern,omitempty"`
	// DestAddrMatchType: "exact", "prefix", "contains", "wildcard" (* and ?),
	// "regex" or "range" (inclusive numeric low-high). Blank is inferred from
	// the pattern: * or ? means wildcard, "low-high" means range, otherwise
	// exact. On a rule with no MessageMatch, a MatchType of prefix, regex,
	// exact, wildcard or range is used for the destination when this is blank.
	DestAddrMatchType string `json:"destAddrMatchType,omitempty"`
	// MessageMatch, if set, additionally requires submit_sm's short_message
	// to match (per MatchType); left empty, the rule matches on destination
	// alone.
	MessageMatch string `json:"messageMatch,omitempty"`
	// MatchType: "contains" (default), "exact", or "regex" — applies to
	// MessageMatch only.
	MatchType string `json:"matchType,omitempty"`
	// ReplySourceAddr/ReplyMessage, if ReplyMessage is set, send one
	// deliver_sm back down the connection — ReplySourceAddr becomes its
	// source_addr (blank reuses the triggering submit_sm's destination_addr,
	// i.e. "the number that was texted" replies back). ReplyMessage is
	// rendered via text/template+sprig with {{.Request.Body}} bound to the
	// received short_message.
	ReplySourceAddr string `json:"replySourceAddr,omitempty"`
	ReplyMessage    string `json:"replyMessage,omitempty"`
}

// DiameterConfig describes a mock Diameter peer. The CER/CEA handshake and
// DWR/DWA watchdog are handled entirely by the underlying state machine —
// there's nothing to configure for them. Every inbound Credit-Control-
// Request is evaluated against Rules (first-match-wins) to decide what
// Result-Code to answer with.
type DiameterConfig struct {
	Port int `json:"port"`
	// OriginHost/OriginRealm are this mock's own Diameter identity,
	// advertised in CER and echoed in every CCA. Blank defaults to
	// "airmock" / "airmock.test".
	OriginHost  string `json:"originHost,omitempty"`
	OriginRealm string `json:"originRealm,omitempty"`
	// DefaultResultCode is the Result-Code for a CCR that matches no rule.
	// Blank/0 keeps the historical DIAMETER_SUCCESS (2001); set e.g. 3002
	// (UNABLE_TO_DELIVER) or 5012 so a missing rule shows up in a test
	// instead of looking like success. A rule that matches but sets no
	// resultCode still answers 2001.
	DefaultResultCode int            `json:"defaultResultCode,omitempty"`
	Rules             []DiameterRule `json:"rules,omitempty"`
}

type DiameterRule struct {
	// CCRequestType, if set, must exactly match the CCR's CC-Request-Type
	// AVP (1=INITIAL_REQUEST, 2=UPDATE_REQUEST, 3=TERMINATION_REQUEST,
	// 4=EVENT_REQUEST); 0 (unset) matches any request type.
	CCRequestType int `json:"ccRequestType,omitempty"`
	// SessionIDMatch, if set, additionally requires the CCR's Session-Id
	// AVP to match (per MatchType); left empty, the rule matches on
	// CCRequestType alone.
	SessionIDMatch string `json:"sessionIdMatch,omitempty"`
	// SubscriptionIDMatch, if set, requires one of the CCR's Subscription-Id
	// (443) entries to have a Subscription-Id-Data (444) value matching it
	// (per MatchType) — e.g. an MSISDN or IMSI.
	SubscriptionIDMatch string `json:"subscriptionIdMatch,omitempty"`
	// RatingGroup, if non-zero, requires one of the request's
	// Multiple-Services-Credit-Control entries to carry this Rating-Group.
	RatingGroup uint32 `json:"ratingGroup,omitempty"`
	// MatchType: "contains" (default), "exact", or "regex" — applies to
	// SessionIDMatch and SubscriptionIDMatch.
	MatchType string `json:"matchType,omitempty"`
	// ResultCode is the Result-Code AVP to answer with — e.g. 2001
	// (DIAMETER_SUCCESS, the default when unset/0) or 5012
	// (DIAMETER_UNABLE_TO_COMPLY) to simulate a rejection.
	ResultCode int `json:"resultCode,omitempty"`

	// Granted units (RFC 4006). When any of these is set, the CCA carries
	// one Multiple-Services-Credit-Control AVP per MSCC in the request (or
	// a single one if the request had none), echoing that MSCC's
	// Rating-Group and Service-Identifier, with a Granted-Service-Unit
	// holding the configured values. Pointers so an explicit 0 (a zero
	// grant) is distinguishable from "not configured". Never applied to
	// TERMINATION_REQUEST answers.
	GrantedTotalOctets          *uint64 `json:"grantedTotalOctets,omitempty"`
	GrantedTime                 *uint32 `json:"grantedTime,omitempty"`
	GrantedServiceSpecificUnits *uint64 `json:"grantedServiceSpecificUnits,omitempty"`
	// ValidityTime is the MSCC Validity-Time AVP, in seconds.
	ValidityTime *uint32 `json:"validityTime,omitempty"`
	// FinalUnitAction, if set, adds a Final-Unit-Indication to the MSCC:
	// "terminate", "redirect" or "restrict_access".
	FinalUnitAction string `json:"finalUnitAction,omitempty"`

	// DelayMs is waited before answering. Fault, if set, replaces the
	// mock-level fault settings for requests this rule matches.
	DelayMs int          `json:"delayMs,omitempty"`
	Fault   *FaultConfig `json:"fault,omitempty"`
}

// HasGrant reports whether the rule asks for a Multiple-Services-Credit-
// Control answer at all.
func (r DiameterRule) HasGrant() bool {
	return r.GrantedTotalOctets != nil || r.GrantedTime != nil || r.GrantedServiceSpecificUnits != nil ||
		r.ValidityTime != nil || r.FinalUnitAction != ""
}

// JMSConfig describes a mock AMQP 1.0 peer (used to mock a JMS provider,
// since AMQP 1.0 is the wire protocol real JMS providers like Qpid JMS and
// ActiveMQ Artemis actually speak). Every inbound message transfer is
// evaluated against Rules (first-match-wins); a matched rule with
// ReplyPayload set delivers one message to any currently-attached receiver
// link whose source address equals ReplyAddress.
type JMSConfig struct {
	Port  int       `json:"port"`
	Rules []JMSRule `json:"rules,omitempty"`
}

type JMSRule struct {
	// AddressPattern, if set, must exactly match the target address of the
	// link the message was sent on (the JMS destination/queue name); left
	// blank, the rule matches any address.
	AddressPattern string `json:"addressPattern,omitempty"`
	// PayloadMatch, if set, additionally requires the message body to
	// match (per MatchType); left empty, the rule matches on address alone.
	PayloadMatch string `json:"payloadMatch,omitempty"`
	// MatchType: "contains" (default), "exact", or "regex" — applies to
	// PayloadMatch only.
	MatchType string `json:"matchType,omitempty"`
	// ReplyAddress/ReplyPayload, if ReplyPayload is set, deliver one
	// message to every receiver link currently attached with this address
	// as its source. ReplyPayload is rendered via text/template+sprig with
	// {{.Request.Body}} bound to the received message body.
	ReplyAddress string `json:"replyAddress,omitempty"`
	ReplyPayload string `json:"replyPayload,omitempty"`
}

// FTPConfig describes a mock FTP server: accepts USER/PASS (any credentials
// if Username is empty, otherwise must match exactly), and serves Files for
// LIST/RETR — RFC 959's passive-mode (PASV) data-connection flow only;
// active mode (PORT), which requires connecting back out to the client, is
// deliberately unsupported, matching how virtually every modern FTP client
// (including curl) already defaults to passive mode anyway.
type FTPConfig struct {
	Port     int       `json:"port"`
	Username string    `json:"username,omitempty"` // empty = any username/password accepted
	Password string    `json:"password,omitempty"`
	Files    []FTPFile `json:"files,omitempty"`
}

// FTPFile is one mock file: Content is returned verbatim for RETR, and
// listed (with Size, or len(Content) if Size is left 0) for LIST. STOR
// uploads aren't matched against this list — any filename is accepted and
// its content logged to the hit log, then discarded.
type FTPFile struct {
	Name    string `json:"name"`
	Content string `json:"content"`
	Size    int64  `json:"size,omitempty"`
}

// ResponseTemplate describes how a matched request is answered. BodyTemplate
// is rendered via text/template + sprig with a RequestContext in scope.
type ResponseTemplate struct {
	StatusCode   int               `json:"statusCode"`
	Headers      map[string]string `json:"headers,omitempty"`
	BodyTemplate string            `json:"bodyTemplate"`
	DelayMs      int               `json:"delayMs,omitempty"`
}
