package apiclient

import (
	"bytes"
	"context"
	"crypto/tls"
	"encoding/base64"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptrace"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Read-timeout bounds for RequestSpec.ReadTimeoutSecs: the previous
// hardcoded 30s applies when left at 0 (unset), and any requested value is
// clamped to MaxReadTimeoutSecs so a mistyped or malicious value (this is a
// public admin API) can't make one request tie up a connection/goroutine
// indefinitely.
const (
	DefaultReadTimeoutSecs = 30
	MaxReadTimeoutSecs     = 300
)

// readTimeout resolves RequestSpec.ReadTimeoutSecs to a clamped Duration.
func readTimeout(spec RequestSpec) time.Duration {
	secs := spec.ReadTimeoutSecs
	if secs <= 0 {
		secs = DefaultReadTimeoutSecs
	}
	if secs > MaxReadTimeoutSecs {
		secs = MaxReadTimeoutSecs
	}
	return time.Duration(secs) * time.Second
}

// TimingBreakdown mirrors what a browser/Postman "timings" panel shows,
// captured via httptrace rather than estimated.
type TimingBreakdown struct {
	DNSMs       int64 `json:"dnsMs"`
	ConnectMs   int64 `json:"connectMs"`
	TLSMs       int64 `json:"tlsMs"`
	FirstByteMs int64 `json:"firstByteMs"`
	TotalMs     int64 `json:"totalMs"`
}

type ExecutionResult struct {
	StatusCode int                 `json:"statusCode"`
	Headers    map[string][]string `json:"headers"`
	Body       string              `json:"body"`
	Timing     TimingBreakdown     `json:"timing"`
	Error      string              `json:"error,omitempty"`
}

// Execute runs a RequestSpec against the real network with no cancellation
// path — a thin wrapper over ExecuteContext for callers that don't have (or
// need) a caller-supplied context, e.g. the load tester, which already has
// its own duration/count caps bounding how long a run can go on.
func Execute(spec RequestSpec, vars map[string]string) *ExecutionResult {
	return ExecuteContext(context.Background(), spec, vars)
}

// ExecuteContext runs a RequestSpec against the real network — this is
// AirMock-as-API-client, calling real (or mocked) endpoints, not serving
// them. {{var}} substitution from the active environment happens here,
// once, before anything is sent. ctx ties the outbound request's lifetime
// to the caller's: internal/web/api's execute handler passes r.Context(),
// so a browser aborting its fetch (the API client's "Force stop" button)
// closes the connection to AirMock's own admin server, which cancels
// r.Context(), which — because it's threaded all the way through via
// http.NewRequestWithContext below — cancels the REAL outbound call too,
// not just the frontend's wait for it.
func ExecuteContext(ctx context.Context, spec RequestSpec, vars map[string]string) *ExecutionResult {
	if msg := checkURLHasHost(spec.URL, vars); msg != "" {
		return &ExecutionResult{Error: msg}
	}
	reqURL := EnsureScheme(SubstituteVars(spec.URL, vars), "http://")
	reqURL = appendQuery(reqURL, spec.Query, vars)
	if spec.Auth != nil && spec.Auth.Type == AuthAPIKey && spec.Auth.AddTo == "query" && spec.Auth.KeyName != "" {
		reqURL = appendQuery(reqURL, []KV{{Key: spec.Auth.KeyName, Value: spec.Auth.KeyValue}}, vars)
	}

	bodyReader, autoContentType, err := buildBody(spec, vars)
	if err != nil {
		return &ExecutionResult{Error: err.Error()}
	}

	req, err := http.NewRequestWithContext(ctx, strings.ToUpper(spec.Method), reqURL, bodyReader)
	if err != nil {
		return &ExecutionResult{Error: err.Error()}
	}
	if autoContentType != "" {
		req.Header.Set("Content-Type", autoContentType)
	}
	for _, h := range spec.Headers {
		if h.Disabled {
			continue
		}
		req.Header.Set(SubstituteVars(h.Key, vars), SubstituteVars(h.Value, vars))
	}
	applyAuth(req, spec.Auth, vars)

	tracker := newTimingTracker()
	req = req.WithContext(httptrace.WithClientTrace(req.Context(), tracker.trace()))

	client := &http.Client{Timeout: readTimeout(spec)}
	// Only given a non-default Transport when TLS actually needs
	// customizing — the common case keeps using http.Client's zero-value
	// DefaultTransport equivalent rather than a fresh one (and its own
	// unshared connection pool) on every single send.
	if spec.Insecure || (spec.ClientCertPEM != "" && spec.ClientKeyPEM != "") {
		tlsConfig := &tls.Config{}
		if spec.Insecure {
			tlsConfig.InsecureSkipVerify = true //nolint:gosec // explicit, per-request opt-in for testing a local/self-signed HTTPS endpoint
		}
		if spec.ClientCertPEM != "" && spec.ClientKeyPEM != "" {
			cert, err := tls.X509KeyPair([]byte(spec.ClientCertPEM), []byte(spec.ClientKeyPEM))
			if err != nil {
				return &ExecutionResult{Error: fmt.Sprintf("client certificate: %v", err)}
			}
			tlsConfig.Certificates = []tls.Certificate{cert}
		}
		client.Transport = &http.Transport{TLSClientConfig: tlsConfig}
	}
	if spec.CookieJarKey != "" {
		client.Jar = jarFor(spec.CookieJarKey)
	}
	tracker.start()
	resp, err := client.Do(req)
	if err != nil {
		return &ExecutionResult{Error: err.Error(), Timing: tracker.snapshot()}
	}
	defer resp.Body.Close()

	bodyBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20)) // 5MB cap on what the viewer holds in memory

	return &ExecutionResult{
		StatusCode: resp.StatusCode,
		Headers:    resp.Header,
		Body:       string(bodyBytes),
		Timing:     tracker.finish(),
	}
}

func msSince(t time.Time) int64 {
	if t.IsZero() {
		return 0
	}
	return time.Since(t).Milliseconds()
}

// timingTracker guards TimingBreakdown's fields (and the start-timestamps
// used to compute them) behind one mutex — httptrace.ClientTrace hooks are
// explicitly documented as possibly being invoked from multiple goroutines,
// including ones that outlive the request itself (e.g. a losing dial's
// cleanup goroutine still calling ConnectDone after a different connection
// already won and the main goroutine has moved on to reading the result).
// Plain local variables here raced under `go test -race` — this session's
// audit caught it via internal/apiclient/loadtest_test.go's concurrent
// worker pool, which fires many Execute calls at once and so reliably
// triggers the overlap a single manual send rarely would.
type timingTracker struct {
	mu                             sync.Mutex
	timing                         TimingBreakdown
	dnsAt, connectAt, tlsAt, reqAt time.Time
}

func newTimingTracker() *timingTracker { return &timingTracker{} }

func (t *timingTracker) trace() *httptrace.ClientTrace {
	return &httptrace.ClientTrace{
		DNSStart:     func(httptrace.DNSStartInfo) { t.mu.Lock(); t.dnsAt = time.Now(); t.mu.Unlock() },
		DNSDone:      func(httptrace.DNSDoneInfo) { t.mu.Lock(); t.timing.DNSMs = msSince(t.dnsAt); t.mu.Unlock() },
		ConnectStart: func(string, string) { t.mu.Lock(); t.connectAt = time.Now(); t.mu.Unlock() },
		ConnectDone: func(string, string, error) {
			t.mu.Lock()
			t.timing.ConnectMs = msSince(t.connectAt)
			t.mu.Unlock()
		},
		TLSHandshakeStart: func() { t.mu.Lock(); t.tlsAt = time.Now(); t.mu.Unlock() },
		TLSHandshakeDone: func(tls.ConnectionState, error) {
			t.mu.Lock()
			t.timing.TLSMs = msSince(t.tlsAt)
			t.mu.Unlock()
		},
		GotFirstResponseByte: func() {
			t.mu.Lock()
			t.timing.FirstByteMs = msSince(t.reqAt)
			t.mu.Unlock()
		},
	}
}

func (t *timingTracker) start() {
	t.mu.Lock()
	t.reqAt = time.Now()
	t.mu.Unlock()
}

// finish records TotalMs and returns the final snapshot — used on the
// success path, after a response was actually received.
func (t *timingTracker) finish() TimingBreakdown {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.timing.TotalMs = msSince(t.reqAt)
	return t.timing
}

// snapshot returns whatever timing was captured so far without setting
// TotalMs — used on the error path (client.Do itself failed), where a
// "total" duration would be misleading since no response was ever received.
func (t *timingTracker) snapshot() TimingBreakdown {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.timing
}

func appendQuery(rawURL string, query []KV, vars map[string]string) string {
	if len(query) == 0 {
		return rawURL
	}
	sep := "?"
	if strings.Contains(rawURL, "?") {
		sep = "&"
	}
	var parts []string
	for _, q := range query {
		if q.Disabled {
			continue
		}
		parts = append(parts, SubstituteVars(q.Key, vars)+"="+SubstituteVars(q.Value, vars))
	}
	if len(parts) == 0 {
		return rawURL
	}
	return rawURL + sep + strings.Join(parts, "&")
}

// buildBody returns the request body reader for spec.BodyMode, plus a
// Content-Type to set automatically (empty if none applies — an explicit
// header the user already set takes precedence, applied after this).
func buildBody(spec RequestSpec, vars map[string]string) (io.Reader, string, error) {
	switch spec.BodyMode {
	case "none":
		return nil, "", nil
	case "urlencoded":
		return buildURLEncodedBody(spec.FormFields, vars), "application/x-www-form-urlencoded", nil
	case "formdata":
		return buildMultipartBody(spec.FormFields, vars)
	default: // "" — raw
		return strings.NewReader(SubstituteVars(spec.Body, vars)), rawContentType(spec.RawContentType), nil
	}
}

func buildURLEncodedBody(fields []KV, vars map[string]string) io.Reader {
	values := url.Values{}
	for _, f := range fields {
		if f.Disabled {
			continue
		}
		values.Set(SubstituteVars(f.Key, vars), SubstituteVars(f.Value, vars))
	}
	return strings.NewReader(values.Encode())
}

func buildMultipartBody(fields []KV, vars map[string]string) (io.Reader, string, error) {
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	for _, f := range fields {
		if f.Disabled {
			continue
		}
		if f.Type == "file" {
			if err := writeMultipartFile(w, f, vars); err != nil {
				return nil, "", err
			}
			continue
		}
		if err := w.WriteField(SubstituteVars(f.Key, vars), SubstituteVars(f.Value, vars)); err != nil {
			return nil, "", err
		}
	}
	if err := w.Close(); err != nil {
		return nil, "", err
	}
	return &buf, w.FormDataContentType(), nil
}

// writeMultipartFile decodes f.Value (the file's content, base64-encoded by
// the browser when it was attached — the request builder runs client-side,
// so this is how the bytes travel here inside the same JSON RequestSpec as
// everything else) and writes it as a real multipart file part. {{var}}
// substitution runs on Key/FileName but deliberately not on the base64
// content itself — a coincidental "{{" inside encoded binary data would
// otherwise corrupt it.
func writeMultipartFile(w *multipart.Writer, f KV, vars map[string]string) error {
	content, err := base64.StdEncoding.DecodeString(f.Value)
	if err != nil {
		return fmt.Errorf("decode file field %q: %w", f.Key, err)
	}
	fw, err := w.CreateFormFile(SubstituteVars(f.Key, vars), SubstituteVars(f.FileName, vars))
	if err != nil {
		return err
	}
	_, err = fw.Write(content)
	return err
}

func rawContentType(hint string) string {
	switch hint {
	case "xml":
		return "application/xml"
	case "html":
		return "text/html"
	case "text":
		return "text/plain"
	default: // "json" or unset — the common case for this app
		return "application/json"
	}
}

// applyAuth sets whatever Headers didn't already cover — deliberately
// applied after the Headers loop so the dedicated Auth tab always wins
// over a manually-typed Authorization header, matching Postman.
func applyAuth(req *http.Request, auth *Auth, vars map[string]string) {
	if auth == nil {
		return
	}
	switch auth.Type {
	case AuthBearer:
		if auth.Token != "" {
			req.Header.Set("Authorization", "Bearer "+SubstituteVars(auth.Token, vars))
		}
	case AuthBasic:
		req.SetBasicAuth(SubstituteVars(auth.Username, vars), SubstituteVars(auth.Password, vars))
	case AuthAPIKey:
		if auth.AddTo == "header" && auth.KeyName != "" {
			req.Header.Set(SubstituteVars(auth.KeyName, vars), SubstituteVars(auth.KeyValue, vars))
		}
		// "query" is applied to the URL before the request is built.
	}
}

// checkURLHasHost returns a plain-language problem with the request URL, or ""
// if it has a host. Without it an empty URL, or one that is only an unresolved
// {{variable}} or a path, reaches net/http and comes back as e.g.
// `Get "http:": http: no Host in request URL`.
func checkURLHasHost(rawURL string, vars map[string]string) string {
	resolved := strings.TrimSpace(SubstituteVars(rawURL, vars))
	if resolved == "" {
		return "Enter a URL to send, for example https://example.com/path"
	}
	candidate := EnsureScheme(resolved, "http://")
	if u, err := url.Parse(candidate); err == nil && u.Host != "" {
		return ""
	}
	// Still containing {{name}} means a variable had no value: say which.
	if i := strings.Index(resolved, "{{"); i >= 0 {
		if j := strings.Index(resolved[i:], "}}"); j > 0 {
			name := strings.TrimSpace(resolved[i+2 : i+j])
			return fmt.Sprintf("The URL %q has no host because the variable {{%s}} has no value. Select an environment that defines it, or type the full URL.", rawURL, name)
		}
	}
	return fmt.Sprintf("The URL %q has no host. Start it with a host or full address, for example https://example.com/path", rawURL)
}
