package apiclient

import (
	"context"
	"encoding/base64"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestExecuteWithoutSchemeDefaultsToHTTP is a regression test: a URL typed
// as just "localhost:PORT/path" (no "http://") used to fail with
// `unsupported protocol scheme "localhost"` — Go's URL parsing reads
// "localhost" itself as the scheme because of the colon, rather than
// treating the whole thing as a schemeless host:port.
func TestExecuteWithoutSchemeDefaultsToHTTP(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	schemeless := strings.TrimPrefix(srv.URL, "http://")
	result := Execute(RequestSpec{Method: "GET", URL: schemeless}, nil)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", result.StatusCode)
	}
}

func TestExecuteAgainstRealServerSubstitutesVarsAndCapturesResponse(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "secret123" {
			t.Errorf("expected substituted header value, got %q", r.Header.Get("X-Api-Key"))
		}
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"id":"widget"}` {
			t.Errorf("expected substituted body, got %q", body)
		}
		w.Header().Set("X-Custom", "yes")
		w.WriteHeader(201)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	spec := RequestSpec{
		Method:  "POST",
		URL:     srv.URL + "/items",
		Headers: []KV{{Key: "X-Api-Key", Value: "{{apiKey}}"}},
		Body:    `{"id":"{{itemId}}"}`,
	}
	vars := map[string]string{"apiKey": "secret123", "itemId": "widget"}

	result := Execute(spec, vars)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if result.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", result.StatusCode)
	}
	if result.Body != `{"ok":true}` {
		t.Fatalf("unexpected body: %q", result.Body)
	}
	if len(result.Headers["X-Custom"]) == 0 || result.Headers["X-Custom"][0] != "yes" {
		t.Fatalf("expected X-Custom response header captured, got %+v", result.Headers)
	}
}

func TestExecuteDisabledHeaderIsNotSent(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Skip") != "" {
			t.Errorf("expected disabled header to be omitted, got %q", r.Header.Get("X-Skip"))
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{
		Method:  "GET",
		URL:     srv.URL,
		Headers: []KV{{Key: "X-Skip", Value: "nope", Disabled: true}},
	}, nil)
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

func TestExecuteAppendsQueryParams(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("limit") != "10" {
			t.Errorf("expected query param limit=10, got %q", r.URL.RawQuery)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{
		Method: "GET",
		URL:    srv.URL + "/items",
		Query:  []KV{{Key: "limit", Value: "10"}},
	}, nil)
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

func TestExecuteAuthBearerSetsAuthorizationHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer secret-token" {
			t.Errorf("expected Bearer auth header, got %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{
		Method: "GET",
		URL:    srv.URL,
		Auth:   &Auth{Type: AuthBearer, Token: "{{token}}"},
	}, map[string]string{"token": "secret-token"})
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

func TestExecuteAuthBearerOverridesManualAuthorizationHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer from-auth-tab" {
			t.Errorf("expected the Auth tab to win over the manual header, got %q", r.Header.Get("Authorization"))
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{
		Method:  "GET",
		URL:     srv.URL,
		Headers: []KV{{Key: "Authorization", Value: "Bearer from-manual-header"}},
		Auth:    &Auth{Type: AuthBearer, Token: "from-auth-tab"},
	}, nil)
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

func TestExecuteAuthBasic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		user, pass, ok := r.BasicAuth()
		if !ok || user != "alice" || pass != "hunter2" {
			t.Errorf("expected basic auth alice:hunter2, got ok=%v user=%q pass=%q", ok, user, pass)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{
		Method: "GET",
		URL:    srv.URL,
		Auth:   &Auth{Type: AuthBasic, Username: "alice", Password: "hunter2"},
	}, nil)
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

func TestExecuteAuthAPIKeyInHeader(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "k123" {
			t.Errorf("expected X-Api-Key header, got %q", r.Header.Get("X-Api-Key"))
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{
		Method: "GET",
		URL:    srv.URL,
		Auth:   &Auth{Type: AuthAPIKey, KeyName: "X-Api-Key", KeyValue: "k123", AddTo: "header"},
	}, nil)
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

func TestExecuteAuthAPIKeyInQuery(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("api_key") != "k123" {
			t.Errorf("expected api_key query param, got %q", r.URL.RawQuery)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{
		Method: "GET",
		URL:    srv.URL,
		Auth:   &Auth{Type: AuthAPIKey, KeyName: "api_key", KeyValue: "k123", AddTo: "query"},
	}, nil)
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

func TestExecuteBodyModeNoneSendsNoBody(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if len(body) != 0 {
			t.Errorf("expected an empty body, got %q", body)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{
		Method:   "POST",
		URL:      srv.URL,
		Body:     "this should be ignored",
		BodyMode: "none",
	}, nil)
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

func TestExecuteBodyModeURLEncoded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/x-www-form-urlencoded" {
			t.Errorf("expected urlencoded content-type, got %q", ct)
		}
		if err := r.ParseForm(); err != nil {
			t.Fatalf("ParseForm: %v", err)
		}
		if r.PostForm.Get("name") != "widget" {
			t.Errorf("expected form field name=widget, got %q", r.PostForm.Get("name"))
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{
		Method:     "POST",
		URL:        srv.URL,
		BodyMode:   "urlencoded",
		FormFields: []KV{{Key: "name", Value: "{{itemName}}"}, {Key: "skip", Value: "x", Disabled: true}},
	}, map[string]string{"itemName": "widget"})
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

func TestExecuteBodyModeFormData(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if r.FormValue("name") != "widget" {
			t.Errorf("expected multipart field name=widget, got %q", r.FormValue("name"))
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{
		Method:     "POST",
		URL:        srv.URL,
		BodyMode:   "formdata",
		FormFields: []KV{{Key: "name", Value: "widget"}},
	}, nil)
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

// TestExecuteBodyModeFormDataWithFileField guards against a real gap: form
// data fields were plain text only — no way to attach a file at all, one of
// the most common real-world API testing needs.
// TestExecuteCookieJarPersistsAcrossCalls guards against a real gap: every
// Execute call built a fresh cookie-less http.Client, so a login response's
// Set-Cookie could never carry into a later request — a session-cookie auth
// flow had no way to work at all.
func TestExecuteCookieJarPersistsAcrossCalls(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/login" {
			http.SetCookie(w, &http.Cookie{Name: "session", Value: "abc123"})
			w.WriteHeader(200)
			return
		}
		cookie, err := r.Cookie("session")
		if err != nil || cookie.Value != "abc123" {
			w.WriteHeader(401)
			return
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	login := Execute(RequestSpec{Method: "GET", URL: srv.URL + "/login", CookieJarKey: "test-session"}, nil)
	if login.StatusCode != 200 {
		t.Fatalf("expected login to succeed, got %d", login.StatusCode)
	}

	// Without the jar key, the session cookie must NOT carry over.
	withoutJar := Execute(RequestSpec{Method: "GET", URL: srv.URL + "/profile"}, nil)
	if withoutJar.StatusCode != 401 {
		t.Fatalf("expected 401 without a shared cookie jar, got %d", withoutJar.StatusCode)
	}

	// With the same jar key, the cookie set by /login must be sent automatically.
	withJar := Execute(RequestSpec{Method: "GET", URL: srv.URL + "/profile", CookieJarKey: "test-session"}, nil)
	if withJar.StatusCode != 200 {
		t.Fatalf("expected the session cookie to carry over via the shared jar, got %d", withJar.StatusCode)
	}
}

// TestExecuteInsecureSkipsCertVerification guards against a real gap:
// Execute always used a plain http.Client with no way to skip TLS
// verification, so testing a local/self-signed HTTPS endpoint (very common
// for the exact kind of dev/test targets this tool is built around) failed
// outright with no workaround.
func TestExecuteInsecureSkipsCertVerification(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	withoutFlag := Execute(RequestSpec{Method: "GET", URL: srv.URL}, nil)
	if withoutFlag.Error == "" {
		t.Fatal("expected a TLS verification error against the self-signed test server without Insecure set")
	}

	withFlag := Execute(RequestSpec{Method: "GET", URL: srv.URL, Insecure: true}, nil)
	if withFlag.Error != "" || withFlag.StatusCode != 200 {
		t.Fatalf("expected Insecure:true to succeed against the self-signed server, got status=%d err=%q", withFlag.StatusCode, withFlag.Error)
	}
}

func TestExecuteBodyModeFormDataWithFileField(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Fatalf("ParseMultipartForm: %v", err)
		}
		if r.FormValue("name") != "widget" {
			t.Errorf("expected text field name=widget, got %q", r.FormValue("name"))
		}
		file, header, err := r.FormFile("upload")
		if err != nil {
			t.Fatalf("FormFile: %v", err)
		}
		defer file.Close()
		if header.Filename != "report.csv" {
			t.Errorf("expected filename report.csv, got %q", header.Filename)
		}
		content, _ := io.ReadAll(file)
		if string(content) != "col1,col2\n1,2\n" {
			t.Errorf("expected the decoded file content, got %q", content)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	fileContent := base64.StdEncoding.EncodeToString([]byte("col1,col2\n1,2\n"))
	result := Execute(RequestSpec{
		Method:   "POST",
		URL:      srv.URL,
		BodyMode: "formdata",
		FormFields: []KV{
			{Key: "name", Value: "widget"},
			{Key: "upload", Value: fileContent, Type: "file", FileName: "report.csv"},
		},
	}, nil)
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

func TestExecuteRawBodyDefaultsToJSONContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "application/json" {
			t.Errorf("expected application/json, got %q", ct)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{Method: "POST", URL: srv.URL, Body: `{"a":1}`}, nil)
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

func TestExecuteRawBodyExplicitContentTypeWins(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if ct := r.Header.Get("Content-Type"); ct != "text/csv" {
			t.Errorf("expected the manual header to win, got %q", ct)
		}
		w.WriteHeader(200)
	}))
	defer srv.Close()

	result := Execute(RequestSpec{
		Method:  "POST",
		URL:     srv.URL,
		Body:    "a,b,c",
		Headers: []KV{{Key: "Content-Type", Value: "text/csv"}},
	}, nil)
	if result.StatusCode != 200 {
		t.Fatalf("expected 200, got %d (err=%s)", result.StatusCode, result.Error)
	}
}

// TestExecuteReadTimeoutSecsTimesOutFasterThanDefault guards against a real
// gap: RequestSpec had no per-request timeout at all — every request used
// a hardcoded 30s http.Client timeout with no way to shorten (fail fast
// against a known-slow/hung endpoint instead of waiting out the full 30s)
// or lengthen it (a legitimately slow endpoint that takes longer than 30s
// always failed with no workaround).
func TestExecuteReadTimeoutSecsTimesOutFasterThanDefault(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(500 * time.Millisecond)
		w.WriteHeader(200)
	}))
	defer srv.Close()

	start := time.Now()
	result := Execute(RequestSpec{Method: "GET", URL: srv.URL, ReadTimeoutSecs: 1}, nil)
	elapsed := time.Since(start)

	if result.StatusCode != 200 || result.Error != "" {
		t.Fatalf("expected the request to complete within its 1s timeout, got %+v", result)
	}
	if elapsed >= 30*time.Second {
		t.Fatalf("expected ReadTimeoutSecs to override the 30s default, took %s", elapsed)
	}
}

// TestExecuteReadTimeoutSecsActuallyTimesOut proves ReadTimeoutSecs is
// really wired into http.Client{Timeout: ...}, not just plumbed through and
// ignored: a server slower than the configured timeout must fail, and
// fail close to that timeout, not the old hardcoded 30s.
func TestExecuteReadTimeoutSecsActuallyTimesOut(t *testing.T) {
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock
	}))
	// Deferred in this order (LIFO) so unblock closes BEFORE srv.Close() —
	// httptest.Server.Close() blocks until every in-flight handler returns,
	// and this handler only returns once unblock is closed; the reverse
	// order deadlocks the test on shutdown.
	defer srv.Close()
	defer close(unblock)

	start := time.Now()
	result := Execute(RequestSpec{Method: "GET", URL: srv.URL, ReadTimeoutSecs: 1}, nil)
	elapsed := time.Since(start)

	if result.Error == "" {
		t.Fatalf("expected a timeout error, got %+v", result)
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("expected the 1s ReadTimeoutSecs to fire, took %s", elapsed)
	}
}

// TestReadTimeoutClampsToMax guards the safety clamp: an absurdly large
// requested timeout doesn't let one request tie up a connection forever —
// mirrors apiclient.clampLoadTestConfig's own philosophy for load-test
// parameters.
func TestReadTimeoutClampsToMax(t *testing.T) {
	got := readTimeout(RequestSpec{ReadTimeoutSecs: 999999})
	if got != MaxReadTimeoutSecs*time.Second {
		t.Fatalf("expected the timeout clamped to %ds, got %s", MaxReadTimeoutSecs, got)
	}
}

func TestReadTimeoutDefaultsWhenUnset(t *testing.T) {
	got := readTimeout(RequestSpec{})
	if got != DefaultReadTimeoutSecs*time.Second {
		t.Fatalf("expected the default %ds timeout, got %s", DefaultReadTimeoutSecs, got)
	}
}

// TestExecuteContextCancellationAbortsTheRealOutboundCall guards against a
// real gap: there was no way to cancel an in-flight Execute call at all —
// the API client's "Force stop" button needs the SERVER's outbound network
// call to actually stop, not just the browser giving up waiting for it.
// Cancelling ctx here must make ExecuteContext return promptly (well
// before the slow server would ever respond), proving the outbound
// request's lifetime is genuinely tied to ctx.
func TestExecuteContextCancellationAbortsTheRealOutboundCall(t *testing.T) {
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-unblock // never responds until the test explicitly lets it
	}))
	// Deferred in this order (LIFO) so unblock closes BEFORE srv.Close() —
	// see TestExecuteReadTimeoutSecsActuallyTimesOut's identical comment.
	defer srv.Close()
	defer close(unblock)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(100 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result := ExecuteContext(ctx, RequestSpec{Method: "GET", URL: srv.URL}, nil)
	elapsed := time.Since(start)

	if result.Error == "" {
		t.Fatal("expected an error from the cancelled context")
	}
	if elapsed >= 5*time.Second {
		t.Fatalf("expected ExecuteContext to return promptly on cancellation, took %s", elapsed)
	}
}

// An empty or host-less URL used to surface Go's raw net/http text, e.g.
// `Get "http:": http: no Host in request URL`.
func TestExecuteGivesAFriendlyErrorForAnEmptyOrHostlessURL(t *testing.T) {
	for _, u := range []string{"", "   ", "http://", "https://", "/just/a/path", "{{baseUrl}}/orders"} {
		res := Execute(RequestSpec{Method: "GET", URL: u}, nil)
		if res.Error == "" {
			t.Errorf("%q: expected an error", u)
			continue
		}
		if strings.Contains(res.Error, "no Host in request URL") || strings.Contains(res.Error, `Get "http:"`) {
			t.Errorf("%q: raw Go error leaked to the user: %s", u, res.Error)
		}
		if !strings.Contains(res.Error, "URL") {
			t.Errorf("%q: the error should say what is wrong with the URL, got %q", u, res.Error)
		}
	}
	// An unresolved variable is called out by name, since that is the usual cause.
	res := Execute(RequestSpec{Method: "GET", URL: "{{baseUrl}}/orders"}, nil)
	if !strings.Contains(res.Error, "baseUrl") {
		t.Errorf("an unresolved {{variable}} should be named in the error, got %q", res.Error)
	}
	// A real URL is unaffected by the check.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("ok")) }))
	defer srv.Close()
	if res := Execute(RequestSpec{Method: "GET", URL: srv.URL}, nil); res.Error != "" || res.StatusCode != 200 {
		t.Errorf("a valid URL should still work: %+v", res)
	}
}
