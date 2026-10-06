# AirMock

A lightweight, self-contained, cross-protocol API mocking tool. Single static Go
binary, embedded web UI, no external runtime or database to install.

AirMock is free and open source software, released under the MIT licence.

## What it does

AirMock spins up mock servers for **12 protocols** behind one binary and one admin
UI — REST, SOAP, GraphQL, WebSocket, TCP/Telnet, SMTP, MQTT, FTP, Kafka, SMPP,
Diameter, and JMS — plus a Postman-class built-in API client for calling real (or
mocked) endpoints, a managed certificate store for HTTPS/mTLS, and enough response
intelligence (validation, conditional rules, stateful scenarios, weighted/random
responses, fault injection) that a mock can behave like the real system it's
standing in for, not just echo a static body.

## Quick start

```sh
go build ./cmd/airmock
./airmock serve --data-dir ./.airmock-dev
```

Open the admin UI at `http://localhost:8080` (default `--admin-port 8080`); mocks
answer on the gateway port (`--gateway-port 8081` by default). Add `--headless` to
skip the automatic browser launch.

### `serve` flags

| Flag | Default | Meaning |
|---|---|---|
| `--admin-port` | `8080` | Admin UI / REST API port |
| `--gateway-port` | `8081` | Where enabled mocks (REST/SOAP/GraphQL/WS/TCP/SMTP/MQTT/FTP/Kafka/SMPP/Diameter/JMS) answer, unless a Mock Project gives them their own dedicated port |
| `--gateway-tls-port` | `8443` | Used only once gateway TLS is enabled from the Certificates page |
| `--data-dir` | `~/.airmock` | Where the SQLite database and generated certs live |
| `--headless` | `false` | Don't try to open a browser on startup |
| `--admin-password` | *(unset)* | Requires this password to use the admin UI/API (also settable via `AIRMOCK_ADMIN_PASSWORD`) — left unset, there's no login at all, which is fine on a single trusted local machine but not for an instance deployed somewhere reachable by anyone on the network. Doesn't gate the mock gateway itself. |

`AIRMOCK_URL` (or `--url` on the `mocks`/`scheduled-events` CLI, see below) points
the CLI at a non-default or remote instance.

## Installing a release

- **Linux (.deb/.rpm)**: download the package for your arch from the release, then
  `sudo dpkg -i airmock_*.deb` or `sudo rpm -i airmock_*.rpm`. Installs a systemd
  service (`systemctl status airmock`) running as a dedicated `airmock` user, admin
  UI at `http://localhost:8080`. Upgrading in place (installing a newer `.deb` over
  an existing install) restarts the service automatically and never touches
  `/var/lib/airmock` — your mocks, certs, and hit log survive the upgrade.
- **Linux (tarball)**: extract `airmock_*_linux_*.tar.gz`, run `./airmock serve`.
- **Windows**: run `AirMockSetup-<version>.exe`, then launch AirMock from the Start
  Menu (or `airmock serve` from a shell — the installer adds itself to PATH).
- **macOS**: extract `airmock_*_darwin_*.tar.gz`, run `./airmock serve`, or drag
  `AirMock.app` out of the archive and launch it directly.

Building a release locally: `goreleaser release --snapshot --clean` (requires `nfpm`
for the Linux packages, pulled in automatically by `go install`, and `npm` for the UI
build declared as a goreleaser `before` hook). Artifacts land in `dist/`.

## Guide

### Mocking

Every mock belongs to exactly one protocol and, for REST/SOAP/GraphQL/WS, can
optionally belong to a **Mock Project** — a named group with its own base path
and/or dedicated gateway port (and its own TLS/mTLS settings), so a set of related
APIs doesn't have to share the shared default gateway port. Deleting a project
deletes every mock inside it too, along with their version history — there's no
partial/orphaned state to clean up afterward.

A mock's response can come from four distinct, explicitly-ordered mechanisms:

1. **Conditional response rules** — an ordered list of `field operator value`
   conditions (equals/contains/regex/gt/lt/exists/…) against the request body,
   query, headers, or (for SOAP/GraphQL) XPath/variables; first match wins.
2. **Stateful scenarios** — a per-session step sequence (e.g. `pending` →
   `shipped` → `delivered` across successive polls from the same client).
3. **Weighted/random responses** — pick one of several configured responses by
   probability, for exercising a client's handling of an inconsistent backend.
4. **A single static/templated response** — the fallback when nothing above
   applies, built with `text/template` + sprig + gofakeit so a body can embed
   request data, fake data, or simple logic.

On top of whichever response mechanism is used, every mock can also carry:

- **Validation rules** (required/type/pattern/range/allowed-values) that
  short-circuit to a configurable error response before any response logic runs.
- **Fault/chaos injection** — latency jitter, a configurable error rate, or a
  timeout rate, applied probabilistically, for testing how a client degrades
  against an unreliable backend.
- **Async/callback mode** — acknowledge immediately, then call back to a URL
  (fixed, or extracted from the request) over webhook or email, with retry/backoff
  and durable resume across a restart.
- **Proxy/record-replay mode** — forward to a real upstream, capture the real
  exchange into the hit log, and one-click "promote to mock" a captured hit into a
  real, editable sync mock (or a scenario, from several captures of the same
  operation).
- **Version history** — every save snapshots the previous version; restore any
  of them later.

Bulk import: WSDL (one mock per SOAP operation), GraphQL SDL, OpenAPI/Swagger (one
mock + derived validation rules per operation), a Postman collection, or a HAR
capture — each scaffolds real, working mocks instead of empty routes.

### Certificates

The Certificates page is a store, not a one-shot self-signed cert: generate any
number of named CAs and leaf certs (RSA or ECDSA), bind one to a mock/project/the
gateway by reference (swap it later without regenerating anything), renew in
place, and see exactly what's using a cert before deleting it.

Certificates can also be **imported** instead of generated — bring in
externally-issued material as:

- a PEM certificate (a single leaf, or a full chain with intermediates concatenated)
- a PEM or DER private key — PKCS#1, PKCS#8, or SEC1/EC, optionally
  passphrase-encrypted (legacy OpenSSL `Proc-Type` style or modern
  `ENCRYPTED PRIVATE KEY` PKCS#8)
- a PKCS#12/`.pfx` bundle (cert + chain + key, password-protected)
- a CA cert with **no** private key at all, to use purely as a trust anchor (e.g.
  verifying client certs against a corporate root you don't hold the key for)

A cert/key mismatch is rejected at import time (the same check `tls.X509KeyPair`
performs at runtime), not discovered the first time something tries to serve TLS
with it.

Bundles group a CA + server cert + (optional) client cert under one name — generate
all three in one action, or assemble a bundle from certs already in the store — so
binding TLS/mTLS to a mock/project/the gateway is one pick instead of three.

### Built-in API client (Collections)

A Postman-class request builder lives in the same binary: workspaces, a
folder/request tree, environments with `{{var}}` substitution (plus Postman-style
dynamic variables like `{{$randomUUID}}`), every auth type (Bearer/Basic/API key),
every body mode (raw/urlencoded/multipart with file fields), a persistent cookie
jar per environment, and response-body field extraction into environment
variables for chaining requests (e.g. a login request feeding its token into
every later call).

Also included:

- **A TLS client-certificate (mTLS) picker** — present any certificate from the
  store (that has a private key) as this request's client cert, for calling an
  endpoint that itself requires mutual TLS.
- **Skip TLS certificate verification** (curl's `-k`) — a per-request opt-in for
  testing a local/self-signed HTTPS endpoint.
- **A built-in load tester** — concurrency/total-requests/duration-capped, with an
  optional detailed per-request breakdown.
- **curl / Postman v2.1 import-export**, plus "copy as code" in JS/Python/Go/curl.
- **A TLS chain inspector** ("Test TLS/Certificate") — `tls.Dial` any host:port
  and see the presented chain, expiry warnings, and hostname-mismatch flags.

Every client-initiated call is logged through the same hit log as inbound mock
traffic, tagged by direction, so nothing about API-client usage is a second,
separate log to check.

### Mocks-as-code CLI

`airmock mocks` talks to a running instance's admin API over plain HTTP (local or
remote, via `--url`/`AIRMOCK_URL`) — no separate storage access, so it works the
same way against any AirMock instance you can reach:

```sh
airmock mocks export -o mocks.yaml     # every mock, as a mocks-as-code file
airmock mocks list                     # table of what's currently configured
airmock mocks apply -f mocks.yaml      # create-or-update by name — idempotent,
                                        # safe to re-run from CI or a pre-commit hook
```

`airmock scheduled-events` follows the same `export`/`list`/`apply` shape for
scheduled events (fire-on-a-timer mocks with no inbound trigger at all).

### Backup & restore

Beyond mocks-as-code, `GET /api/backup/export` / `POST /api/backup/import` cover a
whole-instance snapshot: certificates and bundles (including private key
material), mock projects and mocks, email templates, the API client's
workspaces/collections/environments, and scheduled events — one JSON file for
migrating to a new instance or disaster recovery. Import resolves name collisions
by suffixing rather than overwriting, and skips (with a reason) anything whose
dependency wasn't itself importable. Instance-wide runtime toggles (gateway TLS
settings, SMTP relay config, retention/redaction settings) are deliberately
excluded — a restore shouldn't silently flip a working gateway's TLS or mail relay
out from under it.

### Observability

- **Log History**: every inbound hit, proxy capture, outbound API-client call,
  async callback, and scheduled-event fire, in one unified, filterable table —
  filter by mock, direction, protocol, method, status class, date range, or free
  text; export the filtered set as CSV/JSON; delete one entry or everything
  matching the current filter in one action; a live WebSocket tail for watching
  traffic as it happens.
- **Dashboard**: hits/error-rate/latency over the last 30 minutes, top mocks by
  traffic, and a "never hit" list (enabled mocks with zero hits in the recent
  sample) for spotting dead configuration.
- **Prometheus `/metrics`** (bare root, not under `/api`, matching where scrapers
  look by convention): per-mock hit/error/latency counters, split by direction
  (inbound, proxy-capture, callback, scheduled-event), plus certificate expiry
  gauges.
- **Settings**: configurable hit-log retention (max age / max rows per mock) and a
  redacted-headers list applied to every captured request/response.

### Everything else worth knowing

- A global command palette (`Ctrl`/`Cmd`+`K`) jumps straight to any mock, request,
  or page.
- Mock-creation quick-start templates for common shapes, so a new mock doesn't
  start from a blank form.
- Import formats beyond WSDL/SDL/OpenAPI: a Postman collection, or a HAR capture,
  both scaffold real mocks from real traffic.

## Status

Phase 1 complete (1.0 through 1.13): core mock engine, REST/SOAP/GraphQL, TLS/mTLS
certificate store, async/callback mocking, validation, conditional response rules,
WSDL/GraphQL-SDL/OpenAPI import, stateful/scenario mocks, fault/chaos injection, a
Postman-like API client with collections/curl/Postman-v2.1 import-export, record &
replay (proxy capture + promote-to-mock), hit logging with live tail + dashboard, and
packaging (goreleaser, Linux `.deb`/`.rpm` + systemd, Windows NSIS installer).

Also complete beyond Phase 1: dedicated TCP/Telnet, SMTP, MQTT, WebSocket, FTP, Kafka,
SMPP, Diameter, and JMS mock engines (each with its own listener port) — 12 protocols
total; Mock Projects (grouping mocks under a shared base path / dedicated gateway port;
deleting a project cascade-deletes its mocks); a certificate store that both generates
(multi-CA, RSA/ECDSA, renew, usage lookup, Prometheus expiry gauge) and imports
externally-issued material (PEM/DER/PKCS#12, including passphrase-encrypted keys, or a
trust-anchor-only CA cert); an API client mTLS client-certificate picker for calling
mTLS-protected endpoints, plus a built-in load tester; mock version history with
restore; whole-instance backup export/import (mocks, certs, collections, templates,
scheduled events) for migration/DR; Prometheus `/metrics`; bulk, filter-driven Log
History deletion; a mocks-as-code CLI (`airmock mocks export/list/apply`); instance-wide
Settings (redacted headers, hit-log retention); a global command palette; and
mock-creation quick-start templates.

### Security & access

Admin login is optional. Enable it from Settings > Security (a 4–6 digit PIN or a
password; one shared credential, no per-user accounts) or start with
`--admin-password`/`AIRMOCK_ADMIN_PASSWORD`. Sessions are in-memory with a rolling
timeout (default 24h), failed logins lock an IP out for 60s after 5 attempts, and an
optional inactivity auto-lock is available in the UI. The mock gateway, `/metrics`
and `/healthz` are never gated. The `airmock mocks` / `scheduled-events` CLI logs in
with `--password` (or `AIRMOCK_ADMIN_PASSWORD`) when the target has login enabled.

**Workspace locks** are a separate, per-workspace PIN/password (works even with admin
login off) that blocks editing the collections, environments and mapped mocks/projects
of a workspace until unlocked in that browser.

### Connected Sessions

Every mock detail page for TCP, WebSocket, SMTP, MQTT, Kafka, SMPP, Diameter and JMS
lists live client connections, lets you disconnect one, and (TCP, WS, MQTT, SMPP, JMS)
push a message to it.

### Load testing

The API client's load tester (HTTP and WebSocket) keeps a per-request run history
(configurable retention), with JSON/CSV download of every run and a self-contained HTML
report. SoapUI project XML can be imported as a collection or as SOAP/REST mocks.

For the complete, detailed feature reference see [docs/FEATURES.md](docs/FEATURES.md).

### Known gaps

There are no user accounts, teams, or API keys — one shared admin credential protects
the admin port. AirMock is a single-instance tool with no hosted/shareable public-URL
mode (self-hosted only). Load-test charts are plain CSS bars until Chart.js can be
installed (the npm registry was unreachable when this was last attempted).
