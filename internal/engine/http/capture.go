package httpengine

import (
	"bufio"
	"bytes"
	"fmt"
	"log"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
)

// defaultBodyCaptureLimit is the fallback used until SetBodyCaptureLimit is
// called — the exact value this engine hardcoded before it became a
// configurable Settings-page option, so an instance that never touches the
// setting keeps behaving exactly as before. Caps how much of a response
// body a capturingWriter buffers for logging — unbounded buffering would
// let a large mock response blow up memory just to populate a log viewer.
const defaultBodyCaptureLimit = 64 << 10 // 64KB

// bodyCaptureLimit holds the live, possibly Settings-configured capture
// limit — an atomic int64 (not a plain field protected by a mutex) for the
// same reason redactedHeaderKeys uses atomic.Pointer: read on every single
// response write, written rarely (a Settings-page save), so a lock-free
// read matters more here than anywhere else in this file.
var bodyCaptureLimit atomic.Int64

// SetBodyCaptureLimit changes how much of a response body future requests
// capture for the hit log — safe to call concurrently with in-flight
// requests (e.g. from a live Settings-page save), same as
// SetRedactedHeaders. A non-positive value is ignored (falls back to
// defaultBodyCaptureLimit via captureLimit below) rather than disabling
// capture entirely, which would silently blank every future hit log entry.
func (e *Engine) SetBodyCaptureLimit(limitBytes int) {
	bodyCaptureLimit.Store(int64(limitBytes))
}

func captureLimit() int {
	if n := bodyCaptureLimit.Load(); n > 0 {
		return int(n)
	}
	return defaultBodyCaptureLimit
}

// capturingWriter wraps a real http.ResponseWriter, recording the status
// and a capped prefix of the body as they're written, while still writing
// everything through to the real client unchanged. Every inbound-mock
// response path is wrapped in one of these so hit logging is a single
// deferred call at the top of each matcher rather than threaded through
// every response-writing helper.
type capturingWriter struct {
	http.ResponseWriter
	status   int
	body     bytes.Buffer
	hijacked bool
}

func newCapturingWriter(w http.ResponseWriter) *capturingWriter {
	return &capturingWriter{ResponseWriter: w, status: http.StatusOK}
}

func (c *capturingWriter) WriteHeader(status int) {
	c.status = status
	c.ResponseWriter.WriteHeader(status)
}

func (c *capturingWriter) Write(b []byte) (int, error) {
	limit := captureLimit()
	if c.body.Len() < limit {
		remaining := limit - c.body.Len()
		if remaining > len(b) {
			c.body.Write(b)
		} else {
			c.body.Write(b[:remaining])
		}
	}
	return c.ResponseWriter.Write(b)
}

// Hijack must be implemented explicitly: embedding http.ResponseWriter only
// promotes methods that interface declares, and Hijack isn't one of them,
// so without this a hijacked (fault-injected timeout) response would
// panic instead of falling through to the real Hijacker.
func (c *capturingWriter) Hijack() (net.Conn, *bufio.ReadWriter, error) {
	c.hijacked = true
	hj, ok := c.ResponseWriter.(http.Hijacker)
	if !ok {
		return nil, nil, fmt.Errorf("underlying ResponseWriter does not support hijacking")
	}
	return hj.Hijack()
}

// defaultRedactedHeaderKeys is the fallback list used until SetRedactedHeaders
// is called — the exact set this engine hardcoded before header redaction
// became a configurable Settings-page option, so an instance that never
// touches the setting keeps behaving exactly as before.
var defaultRedactedHeaderKeys = []string{"Authorization", "Cookie", "Set-Cookie", "X-Api-Key", "X-Auth-Token"}

// SetRedactedHeaders replaces the set of header names whose values are
// shown as "***REDACTED***" in the hit log instead of their real value —
// a mock's own configured values, not the runtime request/response, so
// this is about not leaking whatever real-looking secret a test happened
// to pass through the gateway. Safe to call concurrently with in-flight
// requests (e.g. from a live Settings-page save) since it's read via an
// atomic pointer rather than a plain map.
func (e *Engine) SetRedactedHeaders(keys []string) {
	set := make(map[string]bool, len(keys))
	for _, k := range keys {
		set[http.CanonicalHeaderKey(k)] = true
	}
	e.redactedHeaderKeys.Store(&set)
}

func (e *Engine) redactedHeaderSet() map[string]bool {
	if p := e.redactedHeaderKeys.Load(); p != nil {
		return *p
	}
	set := make(map[string]bool, len(defaultRedactedHeaderKeys))
	for _, k := range defaultRedactedHeaderKeys {
		set[http.CanonicalHeaderKey(k)] = true
	}
	return set
}

func (e *Engine) redactedHeaders(h map[string][]string) map[string]string {
	out := hitlog.HeadersFromMulti(h)
	set := e.redactedHeaderSet()
	for k := range out {
		if set[http.CanonicalHeaderKey(k)] {
			out[k] = "***REDACTED***"
		}
	}
	return out
}

// logInboundHit records one inbound mock hit (REST/SOAP/GraphQL, whatever
// mode/response path it took) if a HitLogger is configured. Skips hijacked
// responses (a simulated timeout never produced a real status/body to log).
func (e *Engine) logInboundHit(cw *capturingWriter, r *http.Request, def *mock.Definition, bodyBytes []byte, start time.Time) {
	if e.hitLogger == nil || cw.hijacked {
		return
	}
	err := e.hitLogger.Record(&hitlog.Entry{
		MockID:          def.ID,
		ProtocolType:    def.ProtocolType,
		Direction:       hitlog.DirectionInbound,
		Method:          r.Method,
		Path:            r.URL.Path,
		RequestHeaders:  e.redactedHeaders(r.Header),
		RequestBody:     string(bodyBytes),
		ResponseStatus:  cw.status,
		ResponseHeaders: e.redactedHeaders(cw.Header()),
		ResponseBody:    cw.body.String(),
		LatencyMs:       time.Since(start).Milliseconds(),
	})
	if err != nil {
		// Logging must never break the actual mock response — the
		// response has already been written by the time this runs.
		log.Printf("airmock: failed to record inbound hit for mock %q: %v", def.ID, err)
	}
}
