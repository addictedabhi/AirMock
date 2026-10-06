# AirMock: How It Is Different

*Positioning and comparison document. Competitor facts were checked against public sources on 2026-10-05 (linked at the end). AirMock facts come from the AirMock source code at commit `8dc249c`. Competitor products change often, so re-check a row before quoting it externally.*

---

## 1. The short version

Most API mocking tools were built to fake an HTTP service. AirMock was built to fake **the whole set of systems an enterprise application talks to**, and to give the tester everything needed to exercise them, in one small binary.

Five things set it apart:

1. **Telecom and messaging protocols next to REST.** SMPP (SMS), Diameter (credit control), AMQP 1.0 (JMS), Kafka, MQTT, FTP, SMTP and TCP/Telnet run beside REST, SOAP, GraphQL and WebSocket, all with the same rules, templates, logs and certificates.
2. **Mock, call and load test in one tool.** A Postman-style API client with collections, a collection runner and a load tester with saved run history live in the same binary as the mock engine, and share one hit log.
3. **Explainable behaviour.** Every REST, SOAP and GraphQL response comes from a fixed, documented order of mechanisms, so you can always say why a given reply was returned.
4. **Operational realism.** Durable callbacks that survive a restart, connected-session control (list, disconnect, push), fault injection, a managed TLS/mTLS certificate store and workspace locks.
5. **Zero-footprint install.** One static Go binary with the web UI embedded and SQLite built in. No JVM, Node, Docker or database to install.

**What AirMock is not:** it is a single-instance, self-hosted tool with one shared admin credential. It has no gRPC support, no multi-user accounts, and a much smaller community than the established tools. Section 6 covers this plainly.

---

## 2. The problem AirMock targets

A realistic integration test environment for a telecom, billing or enterprise system usually needs a pile of separate tools:

| Need | Typical answer today |
|---|---|
| Fake REST/SOAP partner APIs | A mock server (WireMock, Mockoon, MockServer, …) |
| Fake an SMSC | A standalone SMPP simulator such as SMPPSim |
| Fake a charging system | A Diameter test harness, often custom |
| Fake queues and brokers | Docker images of real brokers, or custom stubs |
| Call and debug the APIs | An API client such as Postman |
| Load test | A separate load-testing tool |
| TLS and mTLS certificates | `openssl` scripts |
| Scheduled test traffic | cron plus curl |

Each tool has its own configuration format, its own log, its own install and its own learning curve. AirMock collapses the list into one product with one UI, one log and one backup file.

---

## 3. What makes AirMock unique

Each point lists the evidence in the AirMock code so it can be checked.

### 3.1 Twelve protocols, one engine

| Protocol | What AirMock provides |
|---|---|
| REST, SOAP, GraphQL | Method/path, SOAPAction/operation and operation-name routing; WSDL, GraphQL SDL, OpenAPI, HAR, Postman and SoapUI import |
| WebSocket | Greeting, interaction matching (contains/exact/regex), push to a live session |
| TCP / Telnet | Banner, custom line delimiter, interactive or one-line login gate, idle timeout, TLS |
| SMTP | Accept or reject mail by sender, recipient, subject or body; implicit TLS |
| MQTT | Broker with topic-wildcard rules and reply topics |
| FTP | Passive-mode server with a defined file list and login |
| Kafka | Single-node broker: Produce, Fetch, Metadata, ListOffsets; rules append reply records |
| SMPP | SMSC: bind, `submit_sm`, delivery receipts via `deliver_sm`, push to a session |
| Diameter | CER/CEA and DWR/DWA handshake; Credit-Control answers with a chosen Result-Code and, per rule, Multiple-Services-Credit-Control with Granted-Service-Unit, Validity-Time and Final-Unit-Indication (rating-group and subscription matching, per-rule delay and fault) |
| JMS (AMQP 1.0) | Hand-written AMQP 1.0 endpoint with address-based rules and reply routing |

Evidence: `internal/engine/{http,tcp,smtp,mqtt,ftp,kafka,smpp,diameter,jms}`.

### 3.2 One tool for mocking, calling and load testing

- A Postman-class client: workspaces, collections and folders, environments with `{{variables}}`, bearer/basic/API-key auth, multipart bodies, response extraction for chaining, a collection runner, and code snippets in curl, JavaScript, Python and Go.
- An mTLS client-certificate picker backed by the same certificate store the mocks use.
- A load tester (up to 50 concurrent workers, 2000 requests) that keeps every run, with p50 to p99 latency, requests per second and error rate, downloadable as JSON, CSV or a standalone HTML report.
- Every client call, mock hit, proxy capture, callback and scheduled event lands in **one filterable hit log** with a live tail.

Evidence: `internal/apiclient`, `internal/hitlog`, `ui/src/lib/pages/Collections.svelte`.

### 3.3 A documented order of mechanisms

For REST, SOAP and GraphQL the engine always evaluates in this order and answers at the first step that applies:

**proxy → validation → async callback → scenario → weighted random → conditional rules → static response**

This turns "why did I get that response?" into a question with a fixed answer, instead of an emergent result of overlapping stubs.

Evidence: `internal/engine/http/rest_matcher.go`, `internal/mock/rules.go`, `scenario.go`, `weighted.go`.

### 3.4 Behaviour that survives real operation

| Capability | Detail |
|---|---|
| Durable async callbacks | Webhook or email, fixed or extracted target, retry with backoff (+5 s, then +30 s). Jobs are stored in SQLite with a 60 s claim lease, so a restart does not lose them |
| Persistent scenario state | Per-client step progress is stored in SQLite and survives restarts |
| Connected sessions | List, disconnect and push messages to live TCP, WebSocket, MQTT, SMPP and JMS clients; list and disconnect for SMTP, Kafka and Diameter |
| Fault injection | Latency jitter, error-status rate and timeouts per mock, applied across protocols with protocol-appropriate behaviour |
| Record and replay | Proxy to a real service, capture the exchange, promote a captured hit to an editable mock |
| Version history | Every save is snapshotted with a field-level diff; restoring is itself undoable |
| Templating | Go templates with sprig, fake data, persisted counters and CSV-driven rotation |

### 3.5 Built-in certificate and security tooling

- Generate CAs and server/client certificates (RSA or ECDSA) or import PEM, DER and PKCS#12, including passphrase-encrypted keys and trust-anchor-only CAs.
- Bind certificates to the gateway, a project or a mock, with optional mutual TLS.
- A TLS chain inspector that dials any host and reports the chain, expiry and hostname mismatches.
- Optional admin login (PIN or password) with rolling sessions and brute-force lockout, plus per-workspace locks.

### 3.6 Operations and portability

- **Single static binary**, web UI embedded, SQLite built in. Linux `.deb`/`.rpm` with a systemd service, tarballs, a Windows installer and a macOS archive are built by the release pipeline.
- **Mocks as code:** `airmock mocks export | list | apply` (and the same for scheduled events), idempotent by name, usable from CI. It logs in with `--password` for protected instances.
- **Whole-instance backup** to one JSON file that merges on import without overwriting.
- **Prometheus metrics** at `/metrics` and a dashboard that flags never-hit and error-prone mocks.

---

## 4. How AirMock compares

### 4.1 Protocol coverage

Legend: **Yes** = documented in the sources below. **No** = the source states a limitation or lists a narrower scope. **—** = not found in the sources checked; this is **not** proof the feature is absent, so verify before relying on it.

| Capability | AirMock | WireMock | MockServer | mountebank | Mockoon | Postman mocks | Hoverfly |
|---|---|---|---|---|---|---|---|
| HTTP / HTTPS | Yes | Yes | Yes | Yes | Yes | Yes | Yes |
| SOAP / WSDL import | Yes | — | — | — | — | — | — |
| WebSocket | Yes | Yes | Yes | — | Yes | No | — |
| gRPC | **No** | Yes (extension, 3.2.0+) | Yes | — | — | No | — |
| Raw TCP | Yes | — | Yes | Yes | — | No | — |
| SMTP | Yes | No | — | Yes | — | No | — |
| Kafka / MQTT / AMQP | Yes (all three) | No | "message brokers" (per its repository description) | — | — | No | — |
| SMPP, Diameter, FTP | Yes | — | — | — | — | No | — |

Reading the table honestly:

- **MockServer is broader than "HTTP only".** It advertises HTTP/1.1, HTTP/2, gRPC, WebSockets and raw TCP on a single port, with message-broker support. AirMock's advantage over it is the telecom protocols (SMPP, Diameter), SMTP/FTP servers, the built-in client and load tester, and the zero-dependency install, not raw protocol count.
- **mountebank** is the closest on multi-protocol design (HTTP, HTTPS, TCP, SMTP). AirMock adds WebSocket, SOAP/WSDL tooling, brokers and telecom protocols plus a UI.
- **WireMock** is strongest as an HTTP stubbing library with a large ecosystem. For non-HTTP traffic its own guidance is to use separate tools.
- **Mockoon** is a polished HTTP and WebSocket desktop and CLI tool.
- **Postman mock servers** are limited to collections containing only HTTP requests, and are described as stateless and rate limited.
- **Dedicated simulators** such as SMPPSim cover a single protocol (SMPP) and are separate products.

### 4.2 Beyond the mock

These capabilities are listed for AirMock because they come from its own code. The comparison tools are mostly mock servers; check each vendor before claiming a competitor lacks one.

| Capability | AirMock |
|---|---|
| API client with collections and a runner | Yes, built in |
| Load tester with saved history and HTML report | Yes, built in |
| Certificate generation, import and mTLS binding | Yes, built in |
| One log for inbound, proxy, client, callback and scheduled traffic | Yes |
| Durable async callbacks (webhook or email) | Yes |
| Live session control (list, close, push) | Yes, 8 protocols |
| Mocks-as-code CLI | Yes |
| Single static binary, no runtime | Yes |

---

## 5. Where AirMock is a strong fit

| Scenario | Why AirMock fits |
|---|---|
| Telecom and billing integration testing | SMPP and Diameter mocks sit beside REST and SOAP mocks in one tool |
| Systems with mixed HTTP and messaging dependencies | One UI, one rule and template model, one hit log |
| Teams that currently juggle a mock server, Postman, a load tool and `openssl` | One install replaces four |
| Air-gapped or locked-down networks | A single binary and a local SQLite file; no registry pulls |
| CI pipelines | Headless start, then `airmock mocks apply -f mocks.yaml` |
| Demos and training | Nine themes, a dashboard, and live session control make behaviour visible |

---

## 6. Where AirMock is not the better choice

Being straightforward about this protects credibility.

| Limitation | Implication |
|---|---|
| **No gRPC** | Use WireMock (extension) or MockServer for gRPC services |
| **No explicit HTTP/2 support** | Not verified or documented in the code |
| **Single instance, one shared admin credential** | No user accounts, teams, roles or audit trail. Not a shared multi-tenant service |
| **Self-hosted only** | No hosted or shareable public-URL mode (compare WireMock Cloud, Postman cloud) |
| **Kafka is a single-node, in-memory mock** | No consumer groups or transactions; topic contents reset when a mock is re-registered |
| **MQTT is QoS 0 only** | No retained messages or persistent sessions |
| **Per-mock TLS** | Available for HTTP, TCP and SMTP only |
| **No embedded library mode** | Unlike WireMock or MockServer, it cannot be started from inside a JUnit test; it runs as a separate process |
| **Young project, small community** | Fewer integrations, tutorials and third-party answers than established tools |
| **Platform coverage** | Linux packaging is the best exercised. Windows and macOS packaging and the CI release path have not been verified |
| **No contract-drift checking** | It imports OpenAPI and WSDL to scaffold mocks but does not continuously compare mocks to a schema |

---

## 7. Positioning statement

> **AirMock is the single binary that stands in for every system your application talks to, from REST partners to SMS gateways, billing systems and message brokers, and gives you the client, load tester and certificate tooling to test against them, with nothing else to install.**

Suggested short lines:

- *"Mock every backend you depend on."*
- *"One tool instead of six."*
- *"HTTP, SMS, charging, queues and mail, from one binary."*

Claims to avoid unless re-verified: "the only mock tool with…", "no other tool supports SMPP/Diameter", and "other tools are HTTP-only". The sources show that is not accurate for MockServer, mountebank and WireMock.

---

## 8. Evidence appendix (AirMock)

| Claim | Where to check |
|---|---|
| 12 protocols | `internal/mock/model.go`, `internal/engine/registry.go` |
| Response order | `internal/engine/http/rest_matcher.go` |
| Durable callbacks | `internal/mock/async.go`, `callback_worker.go` |
| Scenario persistence | `internal/mock/scenario.go`, table `mock_state` |
| Session control | `internal/session`, `internal/web/api/sessions.go` |
| Load test history | `internal/apiclient/loadtest*.go`, migration 49 |
| Certificates | `internal/certs` |
| Mocks-as-code | `cmd/airmock/mocks_cli.go` |
| Backup | `internal/web/api/backup.go` |
| No gRPC | no `grpc` reference in `internal/` or `cmd/` |

Full feature detail: [FEATURES.md](FEATURES.md).

---

## 9. Sources for competitor facts

- WireMock gRPC support: [wiremock.org/docs/grpc](https://wiremock.org/docs/grpc/)
- WireMock vs MockServer comparison (HTTP focus, other protocols need separate tools): [speedscale.com](https://speedscale.com/blog/wiremock-vs-mockserver-vs-proxymock/)
- mountebank protocols (HTTP, HTTPS, TCP, SMTP): [mbtest.dev API overview](https://www.mbtest.dev/docs/api/overview)
- Mockoon (HTTP, WebSocket, CLI, proxy, templating): [github.com/mockoon/mockoon](https://github.com/mockoon/mockoon), [release v9.0.0](https://mockoon.com/releases/9.0.0/)
- Postman mock servers (HTTP-only collections): [Postman docs, setting up a mock](https://learning.postman.com/docs/designing-and-developing-your-api/mocking-data/setting-up-mock/)
- MockServer (HTTP/1.1, HTTP/2, gRPC, WebSockets, TCP, chaos): [github.com/mock-server/mockserver](https://github.com/mock-server/mockserver)
- Hoverfly (HTTP/HTTPS capture and simulation): [docs.hoverfly.io](https://docs.hoverfly.io/_/downloads/en/latest/pdf/)
- SMPPSim (standalone SMPP simulator): [github.com/jcaberio/SMPPSim](https://github.com/jcaberio/SMPPSim/blob/master/README.md)
