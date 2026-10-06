# AirMock — Detailed Feature Reference

AirMock is a single static Go binary with an embedded Svelte web UI and an embedded SQLite database. It mocks **12 protocols** (REST, SOAP, GraphQL, WebSocket, TCP/Telnet, SMTP, MQTT, FTP, Kafka, SMPP, Diameter, JMS), includes a Postman-class API client with a load tester, a certificate store, and observability tooling.

This document is derived from the source (HEAD `deca44d`). Items marked *(unverified)* were read from code but not exercised.

**Contents**
1. [Architecture at a glance](#1-architecture-at-a-glance)
2. [Running AirMock (CLI and configuration)](#2-running-airmock)
3. [Mock model and shared behaviour](#3-mock-model-and-shared-behaviour)
4. [Response mechanisms (REST/SOAP/GraphQL)](#4-response-mechanisms)
5. [Templating and dynamic data](#5-templating-and-dynamic-data)
6. [Protocol reference](#6-protocol-reference)
7. [Connected Sessions](#7-connected-sessions)
8. [Async callbacks](#8-async-callbacks)
9. [Mock Projects and Workspaces](#9-mock-projects-and-workspaces)
10. [Certificates and TLS](#10-certificates-and-tls)
11. [Built-in API client (Collections)](#11-built-in-api-client)
12. [Load testing](#12-load-testing)
13. [Scheduled Events](#13-scheduled-events)
14. [Import / export](#14-import--export)
15. [Observability](#15-observability)
16. [Security and access control](#16-security-and-access-control)
17. [Web UI guide](#17-web-ui-guide)
18. [Admin REST API map](#18-admin-rest-api-map)
19. [Limits, quirks and known gaps](#19-limits-quirks-and-known-gaps)

---

## 1. Architecture at a glance

| Piece | Detail |
|---|---|
| Language / deps | Go 1.25, chi router, cobra/viper CLI, modernc SQLite (pure Go), golang-migrate |
| Storage | `<data-dir>/airmock.db`, WAL mode, 50 embedded up-only migrations; DB file mode 0600 |
| Admin server | `:8080` — embedded Svelte 5 UI, `/api/*`, `/metrics`, `/healthz` |
| Mock gateway | `:8081` — shared by REST/SOAP/GraphQL/WS mocks (plus optional TLS on `:8443`) |
| Dedicated listeners | TCP, SMTP, MQTT, FTP, Kafka, SMPP, Diameter, JMS mocks each bind their own port; projects can give HTTP-family mocks a dedicated port too |
| Background workers | callback delivery (500 ms poll, 5 concurrent), hit-log retention (hourly), load-test run retention (hourly), scheduled-event worker (1 s poll) |
| Startup | Enabled mocks are re-dispatched to their engines; a failure in one mock is logged and skipped. Persisted gateway TLS is restored |

## 2. Running AirMock

```sh
airmock serve --data-dir ./.airmock-dev
```

| Subcommand | Purpose |
|---|---|
| `serve` | Start admin UI + gateway. A bare `airmock` (no args) runs `serve` (for Windows double-click) |
| `version` | Print version |
| `mocks export / list / apply` | Mocks-as-code against a running instance |
| `scheduled-events export / list / apply` | Same for scheduled events (alias `scheduledevents`) |

`serve` flags (each also available as an `AIRMOCK_*` environment variable):

| Flag | Default | Notes |
|---|---|---|
| `--admin-host` | `127.0.0.1` | Interface the admin UI/API listens on. Use `0.0.0.0` to reach it from other machines (the Linux package's systemd unit does); AirMock logs a warning at start if the admin API is network-reachable with login off |
| `--allowed-origin` | none | Extra browser origin allowed to make admin changes, repeatable (e.g. a reverse proxy's public URL) |
| `--admin-port` | 8080 | |
| `--gateway-port` | 8081 | |
| `--gateway-tls-port` | 8443 | Used only once gateway TLS is enabled |
| `--data-dir` | `~/.airmock` | Created with mode 0700; log tee'd to `airmock.log` |
| `--headless` | false | Skip browser auto-open |
| `--admin-password` / `AIRMOCK_ADMIN_PASSWORD` | unset | Seeds admin login in memory (see §16) |

Behaviours: if something already answers on the admin port, `serve` just re-opens the browser. SIGINT/SIGTERM trigger a 5 s graceful shutdown.

**Mocks-as-code CLI.** `--url` (or `AIRMOCK_URL`, default `http://localhost:8080`) targets any instance. `apply -f file` accepts JSON or YAML (`{"mocks":[...]}` or a bare array) and is idempotent: matched by case-insensitive trimmed name, update via PUT else create via POST. `mocks apply` also takes `--project-id`. Each mock is reported as **created**, **updated** or **unchanged** (an identical definition is not rewritten, so it adds no version-history entry; the server signals this with an `X-AirMock-Unchanged` header). `--dry-run` asks the server to validate everything (shape, templates, name and endpoint conflicts) and prints what would be created, updated or left alone, saving nothing. `--atomic` does that validation pass first and applies nothing unless every mock passes. Without `--atomic`, mocks are applied one by one, so a bad mock fails on its own while the good ones are still created. Runtime failures print just the error, not the usage text. The API behind this: `POST`/`PUT /api/mocks?dryRun=true` returns `{"dryRun":true,"action":"create"|"update"|"unchanged"}`; a `PUT` that omits `protocolType` now defaults to `rest` like a `POST`. Both CLIs accept `--password` (or `AIRMOCK_ADMIN_PASSWORD`) and log in before calling the API, so they work against an instance with admin login enabled; without it they send no credentials.

## 3. Mock model and shared behaviour

Every mock has: `id, name, protocolType, method, pathPattern, enabled, mode, response, proxy, soapAction, operationName, responseRules, asyncConfig, scenario, weighted, validation, fault, validationErrorResponse`, a protocol config object (`tcp`, `smtp`, `ws`, `mqtt`, `ftp`, `kafka`, `smpp`, `diameter`, `jms`), `projectId`, `workspaceId`, and timestamps.

- `mode` is `sync | async | proxy` (the UI also surfaces Scenario and Weighted as modes).
- **Uniqueness:** names are unique case-insensitively across all protocols (409). REST and WS mocks must be unique on method+path per effective gateway port (409); SOAP/GraphQL are exempt because many operations share one POST path.
- **Validation of shape:** dedicated-port protocols require their config and a port > 0; REST/SOAP/GraphQL need method + path; REST methods allowed: GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS, CONNECT, TRACE, QUERY.
- **Disabled mocks** bind no listener / register no route.
- **Updating a mock** closes its live sessions (except FTP) and, if the protocol family changed, unregisters it from the old engine.
- **Version history:** every update snapshots the previous definition (default keep 20, configurable). Restore is itself an update, so it is snapshotted and undoable. History is deleted with the mock/project.
- **Bulk actions** (`enable | disable | delete | move`) report per-item failures.
- **Duplicate port** between dedicated mocks is not detected up front; it surfaces as a 500 listen error.

### Saving a mock: strict fields, validation and warnings
- **Unknown fields are rejected**, not ignored. `POST`/`PUT /api/mocks` (and so `airmock mocks apply`) return `400` naming the field (for example `unknown field "grantedUnits"`), so a typo or an unsupported setting can't silently disappear from the saved mock.
- **Templates are parsed at save time.** A response, rule, scenario, weighted, async, TCP/WS/MQTT/Kafka/SMPP/JMS reply or scheduled-event template that does not parse (for example `{{ if }`, or an unknown function) is rejected with `400` naming the field, e.g. `responseRules[1].response.bodyTemplate`. A mock that is being disabled is exempt, so one that already has a broken template can still be switched off.
- **Shape checks at save time:** an SMTP `banner` must start with a three-digit reply code; a `mode: "proxy"` mock needs `proxy.targetBaseUrl`; Diameter rules validate `finalUnitAction`, `ccRequestType` (0–4) and a non-negative `delayMs`.
- **Warnings:** a save that is valid but probably unintended still succeeds and returns a `warnings` array in the response body, the same text as `X-AirMock-Warning` response headers (for scripts that don't parse the body), again on `GET /api/mocks/{id}`, printed by `airmock mocks apply`, and shown in the UI as a warning toast. Today that covers an async callback, on an HTTP-family async mock or a TCP/WS interaction, with no `callbackBodyTemplate`: such a POST/PUT/PATCH callback sends the default body `{"status":"done"}` (the same one the UI pre-fills) rather than an empty request. A template that is set but renders to nothing is left empty on purpose, and the callback worker logs a warning when it sends one.

### Fault / chaos injection
`latencyJitterMs`, `errorRatePercent`, `errorStatusCodes`, `timeoutRatePercent`. Timeout is rolled first, then error.

| Protocol | Fired error/timeout does |
|---|---|
| REST/SOAP/GraphQL | timeout hijacks and closes the connection; error writes only the status (random from `errorStatusCodes`, default 500) |
| TCP, WS, SMTP, FTP | drop the connection (WS only when a reply would be sent; FTP only around data transfers) |
| MQTT, Kafka, SMPP, JMS | suppress the reply/broadcast only |
| Diameter | no CCA sent; jitter and a per-rule delay are applied before an answer |

Not subject to fault: validation-error responses and proxy mode. Async callbacks are still scheduled when the immediate reply is dropped.

### Validation rules
`{field, required, type, pattern, allowedValues, min, max, minLen, maxLen}`. Types: `string, number, boolean, enum, regex, email, uuid, date` (RFC3339, `2006-01-02`, `2006-01-02T15:04:05`). All errors are collected and returned together: HTTP 400 `{"errors":[{"field","message"}]}` by default, or a custom templated `validationErrorResponse`. Field prefixes: `body.` (gjson path), `header.`, `query.`, `xpath.` (XML). A missing optional field skips its rule. Applies to HTTP-family mocks; for WS it runs on the upgrade handshake.

## 4. Response mechanisms

Applies to REST, SOAP and GraphQL. Net precedence:

**proxy › validation › async › scenario › weighted › response rules › static response**

1. **Proxy / record-replay** — forwards to `targetBaseUrl` + wildcard remainder + query; returns upstream response verbatim (5 MB cap); records a `proxy-capture` hit that can be **promoted to a mock**. Recording is automatic in proxy mode: there is no separate "record" switch, and `proxy.targetBaseUrl` is required. No trailing slash is added to the upstream URL unless the client's own path had one. Bypasses validation and fault.
2. **Conditional response rules** — ordered list, all conditions in a rule ANDed, first match wins. Operators: `equals, notEquals, contains, regex, gt, lt, gte, lte, exists, notExists`. A missing field makes every operator false except `notExists` (including `notEquals`). `gt/lt/gte/lte` parse numbers or numeric strings.
3. **Stateful scenarios** — step sequence per session key (`ip` default, `header`, `query`, `body` gjson path); `loop` wraps to step 0, otherwise the last step repeats. State is persisted in SQLite (`mock_state`) and **survives restarts**.
4. **Weighted/random** — relative weights; entries with weight ≤ 0 are skipped.
5. **Static/templated response** — status (0 → 200), headers, body, `delayMs`. Default Content-Type `application/json` (REST/GraphQL) or `text/xml; charset=utf-8` (SOAP). A template render error returns 500.

Rules, scenarios and weighted responses apply only to HTTP-family sync mocks, not to non-HTTP protocols.

## 5. Templating and dynamic data

Go `text/template` + full **sprig** + custom functions.

| Function | Behaviour |
|---|---|
| `fake "kind" [args]` | `uuid, email, name, firstname, lastname, phone, address, city, state, zipcode, country, company, jobtitle, word, sentence, paragraph, date, pastdate, futuredate, number [min max], bool, creditcardnumber, ipv4, username, color, hexcolor, currency`; unknown kind → "" |
| `counter "name" [step]` | atomic persisted counter scoped to the mock (or scheduled event); negative step decrements |
| `csv "column"` | row from an attached CSV (`round_robin` persisted cursor, or `random`); one row per render, shared by all `csv` calls in that template. Needs header + ≥1 data row; 5 MiB upload cap |

Template data: `.Request.Body` (JSON-parsed when possible), `.Request.BodyBytes`, `.Request.Query`, `.Request.Header`, `.Request.PathParams`. **Only HTTP-family and WS populate Query/Header/PathParams**; TCP, MQTT, Kafka, SMPP and JMS expose just the raw string as `.Request.Body`. SMTP, FTP and Diameter have no templating. Scheduled-event bodies have no request context.

Counters and CSV sources are deleted with their mock/project.

## 6. Protocol reference

### 6.1 REST
Method + path (chi patterns, e.g. `/hello/{name}`, path params available to templates). Request bodies up to 10 MB (`413` beyond that; gzip is decoded). Full pipeline from §4. Quick-start templates in the UI: Health check, Get by ID, List (paginated), Create (201), Auth/login, Not found (404).

### Browser access: HEAD and CORS
A GET mock also answers **HEAD** (same status and headers, no body) unless you define a HEAD mock for that path; note a HEAD runs the same logic as a GET, so it advances a scenario step or counter. **CORS** is on by default and configurable in Settings (allowed origins, methods, headers, credentials, preflight cache): the gateway answers a preflight `OPTIONS` itself, before routing, and adds `Access-Control-Allow-Origin` (and `Access-Control-Expose-Headers`) to responses for requests that carry an `Origin`. Requests without an `Origin` are untouched. Turn it off if you want your own `OPTIONS` mock to answer preflights.

### 6.2 SOAP
Mocks sharing a path form one POST handler. Operation resolution order: `SOAPAction` header → first child element of the SOAP Body (local name vs `operationName`) → the only mock on the path. No match returns a SOAP 1.1 Fault (HTTP 500, `soap:Client`). Rule/validation fields may use `xpath.`. No SOAP 1.2 specific handling was found.

### 6.3 GraphQL
POST with `{query, variables, operationName}`. Operation name = `operationName`, else parsed operation name, else first top-level field. Errors are HTTP 200 with `{"errors":[{"message"}]}`. No schema enforcement. Inline arguments (`user(id: "1")`) are merged into `variables` for rules, validation and templates, with variable references resolved and explicitly sent variables never overridden, so `body.variables.id` works whether the client used an argument or a variable.

### 6.4 WebSocket
Route GET `pathPattern`; config `onConnectMessage`, `interactions[] {match, matchType, response, closeAfter, async}`, `defaultResponse`. Match types `contains` (default) / `exact` / `regex`, first match wins. Validation runs on the handshake. Replies are text frames. `closeAfter` closes after replying. Interactions may schedule async callbacks. Scenario/weighted/rules/`delayMs` do not apply.

### 6.5 TCP / Telnet
Own listener. Config: `banner`, `lineDelimiter` (default `\n`, may be multi-byte), `interactions`, `defaultResponse`, `login`, `sessionTimeoutSecs` (0 = none), `responseDelayMs` (not applied to banner/login), per-mock `tls`.
- **Line handling in:** split on the delimiter; with `\n` a trailing `\r` is trimmed; for other delimiters buffered CR/LF after it are drained; a final unterminated line at EOF is processed; no max line length.
- **Line handling out:** banner, replies and login messages always end with exactly one `\r\n` (existing trailing CR/LF stripped), independent of `lineDelimiter`. Login prompts are written raw.
- **Login gate** (after banner, before interactions): `mode ""` interactive (Username/Password prompts) or `line` (single line matched against `lineFormat` such as `LOGIN:{username}:{password}`); `maxAttempts` default 3 then disconnect; optional success/failure messages.
- Telnet IAC option negotiation is not implemented. `.Request.Body` is the raw line (not JSON-parsed unless the line itself is JSON, for async extraction).

### 6.6 SMTP (fake inbound mail server)
Config: `hostname` (default `airmock`), `banner` (sent verbatim as the greeting, so it must start with a reply code such as `220 `; the API rejects anything else), `rules[] {matchField from|to|subject|body, match, matchType, accept, responseCode, responseMessage}`, `defaultAccept`, `tls`.
Commands: EHLO/HELO (single-line 250, no extensions), MAIL FROM, RCPT TO, DATA (dot-unstuffing, 10 MB cap), RSET, NOOP, QUIT; anything else `500`. `from` rules are applied at **MAIL FROM** and `to` rules at **RCPT TO** (one recipient at a time), so a refusal reaches the client at that command and other recipients can still be accepted; if every recipient is refused, DATA gets `554`. `subject` and `body` rules run after the full message arrives. The first matching rule for a stage wins; accept default 250, reject default 550. Accepted and rejected messages, and recipient or sender refusals, go to the hit log; messages are not otherwise stored. TLS is implicit-only (like port 465); no STARTTLS/AUTH/pipelining.

### 6.7 MQTT
Minimal broker. Packets: CONNECT (always accepted), SUBSCRIBE (QoS 0 granted for all), UNSUBSCRIBE, PUBLISH, PINGREQ, DISCONNECT. No retained messages, persistent sessions, auth, will, or TLS. Rules `{topicPattern (+ and # wildcards), payloadMatch, matchType, replyTopic, replyPayload}`; first topic match wins; reply is broadcast at QoS 0 to every matching subscriber (including the publisher). A matched rule with no `replyTopic` consumes the message silently.

### 6.8 FTP
Passive-mode only. Config: `username`/`password` (blank = accept anything), `files[] {name, content, size}`. Commands: USER, PASS, SYST, TYPE, PWD, CWD (always succeeds), NOOP, QUIT, PASV, LIST/NLST, RETR (exact name, verbatim content, 550 if missing), STOR (accepts any name, up to 10 MB, logged then discarded). PORT unsupported; no directories. PASV/LIST/NLST/RETR/STOR require a completed login (`530` otherwise). FTP has no session registry.

### 6.9 Kafka
Single-node in-memory broker on top of the kafka-go protocol. APIs: ApiVersions v0–2, Metadata v0–8, Produce v0–8, Fetch v0–11, ListOffsets v1–5. **No consumer groups**, transactions or InitProducerId. Topics auto-create with one partition (0); state resets whenever the mock is re-registered. Fetch long-polls. Rules `{topicPattern (exact), payloadMatch, matchType, replyTopic, replyPayload}`: a matching produce appends a reply record to the reply topic. Metadata advertises the connection's local address and the configured port, so remote clients are sent back to a routable host.

### 6.10 SMPP
SMPP v3.4 SMSC. Config `systemId`, `password`, rules `{destAddrPattern, destAddrMatchType, messageMatch, matchType, replySourceAddr, replyMessage}`. The destination can be matched as `exact`, `prefix`, `contains`, `wildcard` (`*` any run, `?` one character), `regex` or a numeric `range` (`15550000-15559999`, inclusive); blank means auto (a pattern with `*`/`?` is a wildcard, `low-high` digits is a range, anything else exact). On a rule with no `messageMatch`, its generic `matchType` of `prefix`, `regex`, `exact`, `wildcard` or `range` is used for the destination when `destAddrMatchType` is blank (`contains` is ignored there, since the UI sets it on every rule by default). Comma-separate alternatives (`1555*,4479*`; not for regex), and a leading `+` is ignored.. Handles `bind_transceiver` (bad system id → 0x0F, bad password → 0x0E), `enquire_link`, `submit_sm` (0x04 if not bound; else resp with sequential hex message id), `unbind`; everything else `generic_nack` (status 3). A matching rule with `replyMessage` sends a `deliver_sm` on the same connection. Messages over 255 bytes would overflow the hand-encoded length; no UDH/concatenation.

### 6.11 Diameter
CER/CEA and DWR/DWA are handled automatically (a client must complete the CER/CEA handshake before it can send a CCR, as with any Diameter peer); only **CCR** has a handler. Config `originHost` (default `airmock`), `originRealm` (default `airmock.test`), and rules, first match wins.

- **Match on:** `ccRequestType` (0 = any; 1 initial, 2 update, 3 termination, 4 event), `sessionIdMatch`, `subscriptionIdMatch` (the Subscription-Id-Data of any Subscription-Id, e.g. an MSISDN or IMSI) and `ratingGroup` (any MSCC in the request). `matchType` (`contains` default, `exact`, `regex`) applies to the two text matches.
- **Answer with:** `resultCode` (default 2001). The CCA always carries Session-Id, Origin-Host/Realm and CC-Request-Type/Number.
- **Granted units (RFC 4006):** set any of `grantedTotalOctets` (CC-Total-Octets), `grantedTime` (CC-Time), `grantedServiceSpecificUnits`, `validityTime` (Validity-Time) or `finalUnitAction` (`terminate`, `redirect`, `restrict_access` → Final-Unit-Indication) and the CCA carries one **Multiple-Services-Credit-Control** per MSCC in the request, echoing its Rating-Group and Service-Identifier, with a Granted-Service-Unit holding the configured values. A request with no MSCC gets a single bare MSCC. An explicit `0` is a real zero grant. Grants are never added to TERMINATION answers. Put a `finalUnitAction` on a `ccRequestType: 2` rule to test quota exhaustion and re-authorization.
- **No rule matches:** the CCA is a plain success (2001) unless the mock sets `defaultResultCode` (for example 3002 or 5012), which makes a missing rule fail visibly in a test. A rule that matches but leaves its `resultCode` blank still answers 2001.
- **Per rule:** `delayMs` and a `fault` object (error rate, timeout rate, jitter) that replaces the mock-level fault for requests the rule matches. An error or timeout means no answer is sent.

### 6.12 JMS (AMQP 1.0)
Hand-rolled AMQP 1.0 (no SASL/TLS/transactions). Open/Begin/Attach/Flow/Transfer/Detach/End/Close. Rules `{addressPattern (exact target address), payloadMatch, matchType, replyAddress, replyPayload}`. Replies are delivered to consumer links whose source address equals `replyAddress` and which have credit; **no queueing** — no credit means the message is lost.

## 7. Connected Sessions

Live view of clients attached to a mock, per mock detail page (polled every 3 s). Admin API: `GET /api/mocks/{id}/sessions`, `POST …/{sessionId}/close`, `POST …/{sessionId}/send {payload, extra}`.

| Protocol | List | Close | Send (`extra` fields) |
|---|---|---|---|
| TCP | ✓ | ✓ | raw bytes to the socket (no line ending added) |
| WebSocket | ✓ | ✓ | text frame (bypasses interaction matching) |
| MQTT | ✓ | ✓ | `topic` (QoS 0, bypasses subscription filter) |
| SMPP | ✓ | ✓ | `sourceAddr`, `destAddr` → `deliver_sm` |
| JMS | ✓ | ✓ | `address` (needs a consumer link with credit) |
| SMTP, Kafka, Diameter | ✓ | ✓ | — |
| REST, SOAP, GraphQL, FTP | empty | — | — |

Each row shows remote address, connected/active times, in/out message counts, and protocol meta (MQTT client id + filters, SMPP system id, Diameter origin host/realm, JMS links). The Dashboard shows total connected sessions with a per-mock popover.

## 8. Async callbacks

Available on: HTTP-family `mode=async` mocks, TCP interactions, WS interactions (not on MQTT, SMTP, FTP, Kafka, SMPP, Diameter or JMS).

- `ackResponse` returned immediately (REST/SOAP/GraphQL), then a durable job is queued.
- Target: `fixed` URL/address, or `extracted` from the request via a field path (the ack returns 400 if extraction yields nothing).
- Channel: `http` (webhook; method default POST; custom headers; templated body; Content-Type defaults to JSON) or `email` (via the SMTP relay; inline subject/body or a saved email template).
- `callbackDelayMs` before first attempt; `maxAttempts` default 3; backoff +5 s after attempt 1, +30 s after later attempts. HTTP success = any 2xx; 10 s client timeout.
- **Durable:** jobs live in `callback_jobs` (`pending | claimed | succeeded | failed`); a 60 s claim lease lets stuck/crashed jobs be re-claimed after a restart. Every attempt is written to the hit log (direction `callback`).
- Known quirk: more than 5 simultaneously due jobs may leave the excess waiting up to ~60 s.

## 9. Mock Projects and Workspaces

**Mock Projects** group REST/SOAP/GraphQL/WS mocks: `name` (unique), `basePath` (a shared path prefix, added to a mock's path when the mock is saved, by the UI, the admin API or `mocks apply` alike; a path that already starts with it is left alone, and changing it later does not move existing mocks), `gatewayPort` (dedicated listener; 0 = shared gateway), `tls` (only with a dedicated port; cert/bundle + client-cert mode/CA), `workspaceId`. Updating a project rebuilds the HTTP engine immediately. Deleting a project cascade-deletes its mocks, version history, counters and CSVs. A stale project reference fails open to the shared gateway.

**Workspaces** namespace collections and environments, and mocks/projects can be mapped to one. A seeded `Default` workspace cannot be deleted or locked; the last workspace cannot be deleted; duplicate names return 409.

**Workspace lock** — a per-workspace PIN (4–6 digits) or password (≥ 4 chars), bcrypt-hashed, **independent of admin login** (works even with admin auth off). A lock protects: listing/creating/updating/deleting/moving the workspace's collections and environments; creating/updating/deleting/bulk-acting mapped mocks; and project create/update/delete. Blocked calls return `403 {"code":"workspace_locked","workspaceId":…}`. Unlock state is held in memory per browser (cookie `airmock_client_id`) and is cleared on restart or when the lock is changed. Changing a lock needs the current password; deleting a locked workspace needs its password every time. No failed-attempt lockout exists for workspace unlock.

## 10. Certificates and TLS

- **Generate:** kinds `ca | server | client`; RSA-2048 (default) or ECDSA P-256; validity default 365 days; SANs (IPs and DNS names; CN auto-added); 128-bit random serial; keys stored as PKCS#8. Non-CA certs need an issuer CA from the store. ASCII-only CN/SANs.
- **Import:** PEM (single or full chain, leaf first) or DER certs; PKCS#1/PKCS#8/SEC1 keys, optionally passphrase-encrypted (legacy `Proc-Type` or modern `ENCRYPTED PRIVATE KEY`); PKCS#12/`.pfx`; a CA with **no key** as trust-anchor-only. Cert/key mismatch is rejected at import. 10 MiB limit.
- **Renew** in place (same id/name/issuer/algorithm, new key); live listeners using it are refreshed.
- **Usage lookup** shows what depends on a cert (gateway server/CA, TCP/SMTP mocks, projects, bundles, issued certs); the UI warns before deleting.
- **Download** returns cert + key PEM (the only API response that includes a key).
- **Bundles:** CA + optional server cert + optional client cert under one name; "Generate all at once" creates `<name>-ca`, `<name>-server`, optional `<name>-client`. A bundle's server cert/CA override individually set IDs. Deleting a bundle does not delete its certs.
- **Gateway TLS** (`/api/gateway/tls`): enabled, cert or bundle, `clientCertMode` `none | optional | required`, client CA; applied live and persisted. mTLS also available per project dedicated port, and per TCP/SMTP mock. MQTT, FTP, Kafka, SMPP, Diameter and JMS have no TLS.
- **TLS test tool** — dials `host:port`, reports the chain, expiry (flags within 14 days), hostname mismatch, verification errors.
- Prometheus exposes certificate expiry gauges; the Dashboard flags expired and expiring-in-30-days certs.

## 11. Built-in API client

- **Structure:** workspaces → collections → folders/requests (REST and WebSocket request types) with saved **examples** and collection-level variables. Environments hold variable maps.
- **Variables:** `{{var}}` (unknown left as-is); dynamic `{{$timestamp}}`, `{{$isoTimestamp}}`, `{{$randomUUID}}`, `{{$randomInt}}` (0–999). UI warns about undefined variables.
- **Request:** method, URL, headers, query, per-row enable/disable; auth `none | bearer | basic | apikey (header or query)` (applied after headers, overriding a manual Authorization); body modes none / raw (JSON, text, XML, HTML, with Beautify and syntax error highlighting) / urlencoded / formdata (including file fields).
- **Extract rules** copy response fields into environment variables for chaining (e.g. `data.token` → `authToken`); **expected status** gives pass/fail.
- **Cookie jar** per environment (in memory; lost on restart).
- **TLS options:** skip verification (`-k`), client-certificate (mTLS) picker from the store (PEM resolved server-side), read timeout 1–300 s (default 30). Response read cap 5 MB. Timing breakdown: DNS, connect, TLS, first byte, total.
- **Code snippets:** curl, JavaScript (fetch), Python (requests), Go.
- **curl:** import (`-X -H -d/--data* -u -F --data-urlencode -b --url -A -G -k`; `-s -i -v -L` ignored) and export.
- **SOAP requests** with envelope templates and SOAPAction handling; **WebSocket requests** with connect-and-send.
- **Collection Runner:** run a collection or folder; pass = expected status if set, otherwise 2xx/3xx; extract rules chain across requests; "N/M passed" summary.
- **Create mock from request / "Mock this collection"** builds mocks from requests (using the default saved example when present).
- **UX:** tabs (rename, close others/right/all, scroll arrows), favorites bar (device-local), per-workspace persistence of open tabs and active environment, search across collections/folders/requests/URLs, `Ctrl/Cmd+S` save, `Ctrl/Cmd+Enter` send, move/copy items between collections and workspaces.
- Every client call is written to the hit log (direction `outbound-client-call`, with collection/request names).

## 12. Load testing

> Load tests run from the machine hosting AirMock, so the numbers include the cost of the tester and the mock sharing one machine. Treat them as relative comparisons between runs, not as a capacity figure for the target.

Available for HTTP requests and WebSocket requests.

| Limit | Value |
|---|---|
| Concurrency | 1–50 |
| Total requests | up to 2000 (default 50 when neither requests nor duration given) |
| Duration | up to 60 s (when set, overrides request count) |

- **Result:** totals, status classes (2xx/3xx/4xx/5xx/error), min/avg/max, p50/p90/p95/p99, duration, requests/sec, plus per-request samples (`index, elapsedMs, latencyMs, statusCode, error`). **Detailed output** additionally captures response bodies/headers in memory (HTTP only; never persisted).
- **WS load test** measures dial + optional send, then disconnects without listening.
- **History:** every run is persisted (`load_test_runs` + `load_test_run_samples`). The UI lists past runs per saved request (When, Requests, Req/s, p95, Error rate) with **View**, **JSON**, **CSV**, **Delete** actions. Retention: max age 30 days and max 50 runs per request (both configurable; runs from unsaved tabs expire by age only), enforced hourly.
- **Downloads:** JSON, CSV, and a self-contained offline **Full report (HTML)** with charts.
- **Charts** currently shipped are plain CSS stopgaps (status breakdown bar, a latency-over-time strip of the first 200 samples, a 10-bucket histogram). Chart.js is **not** installed; commit `5b94e4d` swapped it for CSS bars. Each history row has a **Delete** button.
- Out of scope: multi-step scenarios, ramp-up shapes, pass/fail thresholds, run comparison.

## 13. Scheduled Events

Fire an outbound HTTP request on a fixed interval, with no inbound trigger. Fields: name, `intervalSecs` (> 0), `targetUrl`, method (POST/PUT/PATCH/GET/DELETE), headers, body template (sprig, `fake`, `counter`, `csv`; no request context), CSV source, enabled. Worker polls every second, claims up to 20 due events, reschedules before firing (so slow targets don't cause repeats); 10 s delivery timeout; Content-Type defaults to JSON. Records last status/error/fired-at and a hit-log entry (direction `scheduled-event`). **Fire now** button / API. CLI `scheduled-events export/list/apply`.

## 14. Import / export

| Source | Result |
|---|---|
| **WSDL** (paste/upload/URL; extra XSDs) | one SOAP mock per operation (stub envelope) |
| **SoapUI project XML** | SOAP/REST mocks from MockServices; or a collection (folder per TestSuite/TestCase) |
| **GraphQL SDL** | one mock per Query/Mutation field |
| **OpenAPI 3.x** | one REST mock per operation, example body (spec example or synthesized), first success status, derived validation rules from required query/header/body params; `servers[]` base path included |
| **HAR** / **Postman examples** | one REST mock per method+path with captured response; validation derived from JSON request bodies |
| **Postman Collection v2.1** | collection (folders, requests, auth, variables, examples; WS requests via `x-airmock-ws`) |
| **Postman environment** | environment (disabled vars dropped) |
| **curl** | request |
| **Mocks JSON / YAML** | mocks (new ids; skipped with reasons) |
| **Folder imports** | WSDL + XSDs, or bulk `.json` collections |
| **Bulk collection export** | zip of Postman files |

Import forms accept paste, file upload, or **From URL** (server-side fetch, 10 s timeout, 10 MB cap). Collection name collisions become `Name (2)`.

**Whole-instance backup** (`/api/backup/export`, `/import`): certificates (including private keys), bundles, projects, mocks, email templates, workspaces, environments, collections, scheduled events in one JSON (`airmock-backup.json`). Import is a **merge**: fresh ids, name collisions suffixed `(imported)`, `(imported 2)`…, references remapped, per-entity imported/skipped report. Deliberately excluded: gateway TLS settings, SMTP relay config, app settings, the admin credential, workspace lock hashes, hit logs, load-test runs. Project `gatewayPort` is not carried over.

## 15. Observability

- **Hit log** (directions: `inbound`, `proxy-capture`, `outbound-client-call`, `callback`, `scheduled-event`). Filter by mock, direction, protocol, method, status class, date range, free text; list default 200 rows; export CSV/JSON (default 5000, cap 20000; CSV guards against formula injection); delete one or everything matching the filter; **live tail** over WebSocket (slow clients drop entries rather than block recording); **promote proxy capture to a mock** (named `Promoted: METHOD path`; `409` if a mock with that method+path or name already exists).
- **Redaction:** header values replaced with `***REDACTED***` (defaults `Authorization, Cookie, Set-Cookie, X-Api-Key, X-Auth-Token`; configurable, applied live). **HTTP-family inbound only**; bodies and other protocols are not redacted, and outbound API-client entries were not seen to be redacted.
- **Body capture cap:** 64 KiB by default.
- **Retention:** max age 30 days and 10 000 rows per mock by default; hourly purge (rows with no mock id age out only).
- **Prometheus `/metrics`** (unauthenticated): `airmock_mocks_configured{enabled}`, `airmock_hits_total` (by status class, per mock), `airmock_hit_latency_ms_sum`, `airmock_certificate_expiry_seconds`, `airmock_callback_deliveries` (+ latency sum), `airmock_proxy_captures`, `airmock_scheduled_event_fires`, `airmock_callback_jobs{status}`. `/healthz` returns `ok`.
- **Dashboard** — see §17.

## 16. Security and access control

- **Admin login is optional.** One instance-wide credential (no user accounts): a PIN (4–6 digits) or password (≥ 4 chars), bcrypt-hashed in SQLite. Set/changed/cleared from Settings; `--admin-password` only seeds an in-memory credential when none is persisted (a persisted one wins).
- **Sessions:** in-memory (restart logs everyone out), 32-byte random token, cookie `airmock_session` (HttpOnly, SameSite=Lax, not Secure, no Max-Age), **rolling timeout** default 24 h (configurable). Optional UI **auto-lock on inactivity** (UI-enforced).
- **PIN length:** the length of a PIN (4–6 digits) is stored, returned by `/api/auth/status` as `pinLength`, and used by the login and workspace-unlock keypads, which submit automatically only when that many digits are entered, so a 6-digit PIN is not cut off at 4. A PIN saved before this was recorded has an unknown length (0): the keypad then waits for Enter or "Log in", and one correct login or unlock records the length.
- **Brute-force protection:** 5 failed logins per remote IP → 60 s lockout (HTTP 429). `X-Forwarded-For` is ignored.
- **Scope:** all of `/api` is gated except `/api/auth/{status,login,logout}`. **Not gated:** `/healthz`, `/metrics`, the static UI, and the mock gateway.
- **Cross-site protection:** state-changing admin calls (POST/PUT/PATCH/DELETE) are refused with `403` when the browser's `Origin` (or `Sec-Fetch-Site`) shows another site, so a web page you visit cannot create or change mocks on a locally running instance, even with login off. Clients that send no `Origin` (curl, scripts, the `airmock` CLI) are unaffected.
- **fetch-url guard:** the server-side import fetcher refuses loopback, link-local (including cloud metadata) and unspecified addresses, checked on every connection and redirect hop. Private (RFC 1918) and public hosts are allowed.
- First-time enable needs no auth and auto-logs in the caller.
- Data-dir and DB files are 0600/0700. SMTP relay password is stored in plaintext in the DB.
- UI deterrents (not security): right-click and F12/DevTools shortcuts are blocked.

## 17. Web UI guide

**Shell.** Hash-routed single page; 17 sidebar pages: Dashboard, Mocks (REST/SOAP/GraphQL), TCP, SMTP, WS, MQTT, FTP, Kafka, SMPP, Diameter, JMS Mocks, Scheduled Events, Collections, Certificates, SMTP Settings, Log History, Settings. Sidebar collapses (224 → 64 px, remembered), is drag-reorderable (mouse or long-press on touch; reset in Settings), and becomes an off-canvas drawer ≤ 768 px.

**Themes.** 9 themes — light: Daylight, Meadow, Rose, Cartoon; dark: Midnight, Ocean, Sunset, Violet, Cocoa — each with 4 accents. Light/dark is a property of the theme. Per-browser density (Comfortable/Compact) and a "confirm before delete" toggle.

**Command palette** (`Ctrl/Cmd+K`): jump to any page, any mock (routed to its protocol page), or any request in the active workspace. Prefix matches rank first; 30 results; arrows/Enter/Esc. Certificates are not indexed.

**Right-click menus** (the browser menu is suppressed globally): mock rows (Edit, Duplicate, Enable/Disable, Copy path, Delete), log rows (Promote to mock, Copy path/target, Copy mock name, Delete), certificates (Renew, Download, Copy common name, Delete), email templates, and the Collections tree and tabs.

**Dashboard.** Clickable stat cards with drill-down popovers: mocks configured (enabled/disabled), connected sessions, hits today, error rate, latency avg/p95, collections (with calls today), certificates (expired/expiring). Panels: traffic and average-latency per minute over the last 30 minutes, by protocol, by direction, top 5 mocks and collections by traffic, recent activity (8), and **Insights** — never-hit enabled mocks, error-prone mocks (≥ 5 hits and ≥ 50 % errors), certs expiring within 14 days. Computed from a sample of up to 500 hits.

**Mocks pages.** Search, project cards (expand, inline edit, enable/disable all, add API, duplicate, delete), an "Ungrouped" section, import buttons (WSDL, SoapUI, GraphQL SDL, OpenAPI, mocks JSON, captured traffic), Export all, bulk bar (enable/disable/move/delete), and an expandable detail view with a copy-ready **usage snippet**, **▶ Send test request**, config summary, **version history with field-level diffs and restore**, and the mock's **own log history with Clear logs**. Usage snippets exist for every protocol (e.g. MQTT needs mosquitto-clients, Kafka needs kcat).

**Log History.** Today/all toggle, Go live, collapsible filter card (search, mock, direction, protocol, method, status, since/until, load limit 200/500/1000), count with "more may exist", CSV/JSON export of the whole filtered set, Delete matching, expandable rows with pretty-printed JSON.

**Collections.** See §11–12. Also Environments panel (add, rename, duplicate, delete, active radio, per-env variables, Postman import).

**Certificates, SMTP Settings, Scheduled Events.** See §10, §8 and §13. SMTP Settings holds relay config (host, port, user, password—blank keeps the existing one, from name/address, STARTTLS), a **Send test email** action, and email templates (`{{.Request.…}}` placeholders; deleting a template makes mocks fall back to inline subject/body).

**Settings.**

| Card | Keys (defaults) |
|---|---|
| Preferences (per browser) | density, confirm-before-delete, reset sidebar order |
| Security | enable/change/turn off admin login; session timeout (24 h); auto-lock after inactivity (off) |
| Hit-log retention | max age 30 d; max rows per mock 10 000; max captured body 65 536 B; load-test history max age 30 d and max 50 runs per request |
| New mock defaults | response delay ms (0); failure rate % (0) — only pre-fill new REST/SOAP mocks |
| Version history | max versions per mock (20; lowering it prunes immediately) |
| Redacted headers | editable chip list, reset to default |
| Backup | Export everything / Import from file with per-category imported/skipped chips |

## 18. Admin REST API map

All under `/api`, behind admin auth when enabled (except `/auth/{status,login,logout}`).

| Group | Highlights |
|---|---|
| `/auth` | `status, login, logout, set-password, clear-password` |
| `/config` | ports, version, Go version, OS/arch |
| `/mocks` | CRUD; `export`, `import`, `bulk`; `{id}/versions` (+ `restore`); `{id}/csv-source`; `{id}/sessions` (list/close/send) |
| `/mock-projects` | CRUD |
| `/wsdl`, `/graphql-schema`, `/openapi`, `/traffic-import` | scaffold mocks (`/wsdl/import-soapui-mocks`, `/traffic-import/har`, `/traffic-import/postman-examples`) |
| `/certificates`, `/cert-bundles`, `/gateway/tls` | §10 |
| `/tools` | `tls-test`, `fetch-url` |
| `/apiclient` | workspaces (+ lock/unlock/remove-lock), collections (CRUD, export, move, imports), environments, `execute`, `loadtest`, `ws-loadtest`, `loadtest-export`, `loadtest-runs`, `curl-import`, `curl-export`, `snippet`, `ws-exchange` |
| `/smtp` | `settings`, `test`, `templates` |
| `/hitlog` | list, delete-by-filter, `export`, `tail` (WebSocket), `{id}`, `{id}/promote-to-mock` |
| `/settings` | GET / PUT |
| `/scheduled-events` | CRUD, `{id}/fire-now`, `{id}/csv-source` |
| `/backup` | `export`, `import` |

## 19. Limits, quirks and known gaps

| Area | Item |
|---|---|
| Size caps | HTTP request body 10 MB (413 beyond); proxy response 5 MB; API client response 5 MB; FTP STOR / SMTP DATA / MQTT packet 10 MB; CSV upload 5 MiB. **No cap** on TCP line length, JMS frame, Kafka frame |
| Auth | Single shared credential, no users/teams/API keys; `/metrics` and the gateway are unauthenticated |
| Redaction | Headers of HTTP-family inbound traffic only |
| Persistence | Sessions, cookie jars, workspace unlocks are in memory; Kafka topics reset on mock re-register |
| TLS | Only HTTP gateway/projects, TCP and SMTP (implicit) support TLS |
| Kafka | Metadata advertises the address the client connected to (loopback if it dialed loopback); no consumer groups |
| FTP | Passive mode only |
| TCP | Replies always end `\r\n`; no Telnet IAC negotiation |
| SMPP | No UDH/long-message handling; messages > 255 bytes overflow the reply encoder |
| JMS | No queueing; replies to consumers without credit are dropped |
| Ports | No up-front duplicate dedicated-port detection |
| WS | A WS mock without a `ws` config object is not guarded against in the API *(code reading)* |
| Load test | Chart.js not installed (CSS stopgap charts; npm registry unreachable) |
| Unverified | Windows installer / macOS bundle / CI release paths; browser behaviour of the UI was read from source, not exercised |
