package apiclient

import (
	"strings"
	"testing"
)

func TestParseCurlBasicGet(t *testing.T) {
	spec, err := ParseCurl(`curl https://example.com/orders/1`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.Method != "GET" || spec.URL != "https://example.com/orders/1" {
		t.Fatalf("unexpected spec: %+v", spec)
	}
}

// TestParseCurlHandlesBashLineContinuation is a regression test for the
// exact real-world report that surfaced this: a multi-line curl command
// pasted with a trailing backslash (the standard bash/zsh/sh continuation,
// and what Postman's "Copy as cURL (bash)" / most browser devtools export)
// used to have its URL and every flag after the continued line silently
// destroyed — shlex treats a bare "\<newline>" as an escaped-newline
// character rather than a removed line break, so it merged into the START
// of the next flag's name instead of separating two tokens.
func TestParseCurlHandlesBashLineContinuation(t *testing.T) {
	spec, err := ParseCurl("curl --location 'http://10.121.77.105:9211/ThunderBilling/ScheduledTasks/keepAlive' \\\n--data 'foo=bar'")
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.URL != "http://10.121.77.105:9211/ThunderBilling/ScheduledTasks/keepAlive" {
		t.Fatalf("expected the full URL, got %q", spec.URL)
	}
	if spec.Method != "POST" {
		t.Fatalf("expected POST (the --data flag survived the line continuation and was parsed), got %q", spec.Method)
	}
}

// TestParseCurlEmptyDataDoesNotForcePost is a real-world report: curl's own
// documented behavior is that the mere presence of --data — even --data ”
// with nothing in it — switches curl's default method to POST. That's a
// well-known curl gotcha, but a curl command with a wholly empty --data is
// virtually always a vestigial placeholder some other tool left on an
// otherwise-GET endpoint, not someone deliberately sending an empty POST
// body — so, unlike real curl, this importer leaves an empty --data alone
// and keeps the default GET.
func TestParseCurlEmptyDataDoesNotForcePost(t *testing.T) {
	spec, err := ParseCurl("curl --location 'http://10.121.77.105:9211/ThunderBilling/ScheduledTasks/keepAlive' --data ''")
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.Method != "GET" {
		t.Fatalf("expected GET (an empty --data shouldn't force POST), got %q", spec.Method)
	}
	if spec.Body != "" {
		t.Fatalf("expected no body from an empty --data, got %q", spec.Body)
	}
}

// TestParseCurlMultipleDataOccurrencesWithOneEmptyStillForcesPost guards the
// boundary of the above: --data ” alongside a SECOND --data that actually
// has content is real data (curl joins multiple --data occurrences with
// "&"), so this must still resolve to POST rather than being swept up by
// the "wholly empty" exception.
func TestParseCurlMultipleDataOccurrencesWithOneEmptyStillForcesPost(t *testing.T) {
	spec, err := ParseCurl("curl 'http://example.com/orders' --data '' --data 'foo=bar'")
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.Method != "POST" {
		t.Fatalf("expected POST since one of the --data occurrences has real content, got %q", spec.Method)
	}
	if spec.Body != "&foo=bar" {
		t.Fatalf("expected the empty and non-empty --data values joined with '&', got %q", spec.Body)
	}
}

// TestParseCurlHandlesWindowsLineContinuations covers the other two common
// paste sources: cmd.exe (trailing ^) and PowerShell (trailing backtick)
// curl exports.
func TestParseCurlHandlesWindowsLineContinuations(t *testing.T) {
	cases := []struct {
		name string
		curl string
	}{
		{"cmd.exe caret", "curl --location 'https://example.com/items' ^\n-H 'X-Api-Key: abc'"},
		{"PowerShell backtick", "curl --location 'https://example.com/items' `\n-H 'X-Api-Key: abc'"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			spec, err := ParseCurl(tt.curl)
			if err != nil {
				t.Fatalf("ParseCurl: %v", err)
			}
			if spec.URL != "https://example.com/items" {
				t.Fatalf("expected the full URL, got %q", spec.URL)
			}
			if len(spec.Headers) != 1 || spec.Headers[0].Key != "X-Api-Key" || spec.Headers[0].Value != "abc" {
				t.Fatalf("expected the header from the continued line, got %+v", spec.Headers)
			}
		})
	}
}

// TestParseCurlSplitsQueryParamsOutOfURL is a regression test: a curl URL
// naturally has its query params embedded (e.g. "...?status=pending"), but
// this app treats query params as their own field (RequestSpec.Query, the
// UI's Query tab) distinct from the base URL — ParseCurl used to leave them
// jammed inside URL untouched, so an imported request's Query tab was
// silently empty and the URL field showed the full messy query string.
func TestParseCurlSplitsQueryParamsOutOfURL(t *testing.T) {
	spec, err := ParseCurl(`curl 'https://example.com/orders/123?status=pending&limit=10'`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.URL != "https://example.com/orders/123" {
		t.Fatalf("expected the query string stripped from URL, got %q", spec.URL)
	}
	want := []KV{{Key: "status", Value: "pending"}, {Key: "limit", Value: "10"}}
	if len(spec.Query) != len(want) || spec.Query[0] != want[0] || spec.Query[1] != want[1] {
		t.Fatalf("expected Query %+v, got %+v", want, spec.Query)
	}
}

// TestParseCurlDecodesPercentEncodedQueryValues confirms percent-encoded
// query values are decoded on import (e.g. from a curl command copied out
// of a browser's network inspector), while a literal "{{var}}" template
// placeholder — not valid percent-encoding — passes through unchanged
// rather than being dropped.
func TestParseCurlDecodesPercentEncodedQueryValues(t *testing.T) {
	spec, err := ParseCurl(`curl 'https://example.com/search?q=hello%20world&id={{orderId}}'`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	want := []KV{{Key: "q", Value: "hello world"}, {Key: "id", Value: "{{orderId}}"}}
	if len(spec.Query) != len(want) || spec.Query[0] != want[0] || spec.Query[1] != want[1] {
		t.Fatalf("expected Query %+v, got %+v", want, spec.Query)
	}
}

// TestParseCurlDashGSendsDataAsQueryOnAGet is a regression test: curl's
// -G/--get flag means "send any --data as query params on a GET request,"
// not a body on a POST — a real, commonly-used curl pattern (Postman and
// browser devtools both emit it for GET requests with parameters) that
// ParseCurl previously didn't recognize at all, so --data unconditionally
// forced Method to POST and dumped its value into Body, silently importing
// the wrong method.
func TestParseCurlDashGSendsDataAsQueryOnAGet(t *testing.T) {
	spec, err := ParseCurl(`curl -G 'https://example.com/search' --data 'q=hello' --data 'page=2'`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.Method != "GET" {
		t.Fatalf("expected GET (curl -G forces GET), got %q", spec.Method)
	}
	if spec.Body != "" {
		t.Fatalf("expected no body (data goes to query under -G), got %q", spec.Body)
	}
	want := []KV{{Key: "q", Value: "hello"}, {Key: "page", Value: "2"}}
	if len(spec.Query) != len(want) || spec.Query[0] != want[0] || spec.Query[1] != want[1] {
		t.Fatalf("expected Query %+v, got %+v", want, spec.Query)
	}
}

// TestParseCurlDashGRespectsExplicitMethod confirms -X still wins over -G's
// own method inference, same as it already does over --data's.
func TestParseCurlDashGRespectsExplicitMethod(t *testing.T) {
	spec, err := ParseCurl(`curl -X POST -G 'https://example.com/search' --data 'q=hello'`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.Method != "POST" {
		t.Fatalf("expected explicit -X POST to win over -G's GET inference, got %q", spec.Method)
	}
}

// TestParseCurlConcatenatesMultipleDataFlags matches real curl behavior:
// repeated -d/--data flags join into one body with "&", rather than the
// last one silently overwriting all the others.
func TestParseCurlConcatenatesMultipleDataFlags(t *testing.T) {
	spec, err := ParseCurl(`curl --data 'a=1' --data 'b=2' https://example.com/items`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.Method != "POST" {
		t.Fatalf("expected POST, got %q", spec.Method)
	}
	if spec.Body != "a=1&b=2" {
		t.Fatalf(`expected body "a=1&b=2", got %q`, spec.Body)
	}
}

func TestParseCurlWithHeadersAndBodyInfersPost(t *testing.T) {
	spec, err := ParseCurl(`curl -H "Content-Type: application/json" -H "X-Api-Key: abc123" --data-raw '{"name":"widget"}' https://example.com/items`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.Method != "POST" {
		t.Fatalf("expected method inferred as POST from --data-raw, got %q", spec.Method)
	}
	if spec.Body != `{"name":"widget"}` {
		t.Fatalf("unexpected body: %q", spec.Body)
	}
	if len(spec.Headers) != 2 || spec.Headers[0].Key != "Content-Type" || spec.Headers[1].Key != "X-Api-Key" {
		t.Fatalf("unexpected headers: %+v", spec.Headers)
	}
}

// TestParseCurlFormFieldsSurviveImport guards against a real gap: -F/--form
// had no handler at all, so it fell into ParseCurl's generic "unrecognized
// flag" branch — every multipart field in a pasted curl command (plain
// field or file upload) was silently dropped, with BodyMode left empty and
// no error to explain why the imported request had no body at all.
func TestParseCurlFormFieldsSurviveImport(t *testing.T) {
	spec, err := ParseCurl(`curl -F 'name=widget' -F 'upload=@report.csv' https://example.com/upload`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.BodyMode != "formdata" {
		t.Fatalf("expected BodyMode=formdata, got %q", spec.BodyMode)
	}
	if len(spec.FormFields) != 2 {
		t.Fatalf("expected 2 form fields, got %+v", spec.FormFields)
	}
	text := spec.FormFields[0]
	if text.Key != "name" || text.Value != "widget" || text.Type == "file" {
		t.Fatalf("unexpected text field: %+v", text)
	}
	file := spec.FormFields[1]
	if file.Key != "upload" || file.Type != "file" || file.FileName != "report.csv" {
		t.Fatalf("unexpected file field: %+v", file)
	}
}

// TestParseCurlDataUrlencodeFieldsSurviveImport guards against a real gap:
// --data-urlencode fell through to the generic "unrecognized flag" case, so
// it and its value were both silently skipped — a urlencoded-body request
// (like an OAuth token call built from several --data-urlencode flags)
// imported with BodyMode left as "" (raw) and every field gone.
func TestParseCurlDataUrlencodeFieldsSurviveImport(t *testing.T) {
	spec, err := ParseCurl(`curl -X POST -H 'Content-Type: application/x-www-form-urlencoded' --data-urlencode 'grant_type=password' --data-urlencode 'client_id=3' --data-urlencode 'username=demo-user' --data-urlencode 'password=@qBridGe1' 'https://example.com/auth/token'`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.BodyMode != "urlencoded" {
		t.Fatalf("expected BodyMode=urlencoded, got %q", spec.BodyMode)
	}
	want := []KV{
		{Key: "grant_type", Value: "password"},
		{Key: "client_id", Value: "3"},
		{Key: "username", Value: "demo-user"},
		{Key: "password", Value: "@qBridGe1"},
	}
	if len(spec.FormFields) != len(want) {
		t.Fatalf("expected %d form fields, got %+v", len(want), spec.FormFields)
	}
	for i, w := range want {
		if got := spec.FormFields[i]; got.Key != w.Key || got.Value != w.Value {
			t.Fatalf("field %d: expected %+v, got %+v", i, w, got)
		}
	}
}

// TestParseCurlDataUrlencodeFileFieldReferencesFilename covers curl's
// "name@filename" form (the value is read from a file at send time,
// distinct from -F's own file-upload syntax but simplified the same way
// addFormField already does: a file field with no content).
func TestParseCurlDataUrlencodeFileFieldReferencesFilename(t *testing.T) {
	spec, err := ParseCurl(`curl --data-urlencode 'payload@body.txt' https://example.com/submit`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if len(spec.FormFields) != 1 {
		t.Fatalf("expected 1 form field, got %+v", spec.FormFields)
	}
	f := spec.FormFields[0]
	if f.Key != "payload" || f.Type != "file" || f.FileName != "body.txt" {
		t.Fatalf("unexpected field: %+v", f)
	}
}

func TestParseCurlExplicitMethodOverridesDataInference(t *testing.T) {
	spec, err := ParseCurl(`curl -X PATCH --data-raw '{}' https://example.com/items/1`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	if spec.Method != "PATCH" {
		t.Fatalf("expected explicit PATCH to win over data-implies-POST, got %q", spec.Method)
	}
}

// TestParseCurlInsecureFlagSetsInsecure guards against a real gap: -k/
// --insecure was recognized-but-ignored on import (listed only in
// curlNoValueFlags), so importing a curl command that tested a self-signed
// endpoint lost that fact — the re-created request would then fail with a
// TLS error and no obvious explanation why the original command worked.
func TestParseCurlInsecureFlagSetsInsecure(t *testing.T) {
	for _, flag := range []string{"-k", "--insecure"} {
		spec, err := ParseCurl(`curl ` + flag + ` https://self-signed.example.com/`)
		if err != nil {
			t.Fatalf("ParseCurl(%s): %v", flag, err)
		}
		if !spec.Insecure {
			t.Fatalf("expected Insecure=true for flag %s", flag)
		}
	}
}

func TestToCurlIncludesInsecureFlag(t *testing.T) {
	cmd := ToCurl(RequestSpec{Method: "GET", URL: "https://self-signed.example.com/", Insecure: true})
	if !strings.Contains(cmd, " -k ") && !strings.HasSuffix(cmd, " -k") {
		t.Fatalf("expected -k in the exported command, got: %s", cmd)
	}
}

func TestParseCurlBasicAuthEncodesHeader(t *testing.T) {
	spec, err := ParseCurl(`curl -u admin:secret https://example.com/`)
	if err != nil {
		t.Fatalf("ParseCurl: %v", err)
	}
	want := "Basic YWRtaW46c2VjcmV0" // base64("admin:secret")
	found := false
	for _, h := range spec.Headers {
		if h.Key == "Authorization" && h.Value == want {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected a correctly base64-encoded Authorization header, got %+v", spec.Headers)
	}
}

// TestCurlRoundTrip is the plan's verify criterion: curl -> RequestSpec ->
// curl, on a fixed table of representative commands.
func TestCurlRoundTrip(t *testing.T) {
	tests := []struct {
		name string
		curl string
	}{
		{"simple GET", `curl -X GET 'https://example.com/orders/1'`},
		{"POST with header and body", `curl -X POST -H 'Content-Type: application/json' --data-raw '{"a":1}' 'https://example.com/orders'`},
		{"body with an embedded single quote", `curl -X POST --data-raw 'it''s a test' 'https://example.com/echo'`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			spec1, err := ParseCurl(tt.curl)
			if err != nil {
				t.Fatalf("first parse: %v", err)
			}
			roundTripped := ToCurl(*spec1)
			spec2, err := ParseCurl(roundTripped)
			if err != nil {
				t.Fatalf("second parse (of %q): %v", roundTripped, err)
			}
			if spec1.Method != spec2.Method || spec1.URL != spec2.URL || spec1.Body != spec2.Body {
				t.Fatalf("round trip mismatch:\n  first:  %+v\n  second: %+v\n  via: %s", spec1, spec2, roundTripped)
			}
		})
	}
}

// TestToCurlUrlencodedBodyRoundTrips guards the round trip ToCurl relies on
// for urlencoded bodies specifically: it renders each form field as its own
// --data-urlencode, which — before addURLEncodeField existed — reimported
// as an empty raw body with every field gone.
func TestToCurlUrlencodedBodyRoundTrips(t *testing.T) {
	spec := RequestSpec{
		Method:   "POST",
		URL:      "https://example.com/auth/token",
		BodyMode: "urlencoded",
		FormFields: []KV{
			{Key: "grant_type", Value: "password"},
			{Key: "username", Value: "demo-user"},
		},
	}
	out := ToCurl(spec)
	reparsed, err := ParseCurl(out)
	if err != nil {
		t.Fatalf("ParseCurl(ToCurl(...)): %v", err)
	}
	if reparsed.BodyMode != "urlencoded" {
		t.Fatalf("expected BodyMode=urlencoded to survive the round trip, got %q (curl: %s)", reparsed.BodyMode, out)
	}
	if len(reparsed.FormFields) != len(spec.FormFields) {
		t.Fatalf("expected %d form fields to survive, got %+v (curl: %s)", len(spec.FormFields), reparsed.FormFields, out)
	}
	for i, want := range spec.FormFields {
		if got := reparsed.FormFields[i]; got.Key != want.Key || got.Value != want.Value {
			t.Fatalf("field %d: expected %+v, got %+v (curl: %s)", i, want, got, out)
		}
	}
}

func TestToCurlProducesShellSafeQuoting(t *testing.T) {
	spec := RequestSpec{Method: "POST", URL: "https://example.com", Body: `it's a "test"`}
	out := ToCurl(spec)
	// Re-parsing our own output must recover the exact original body —
	// the real test of "shell-safe," not just eyeballing the string.
	reparsed, err := ParseCurl(out)
	if err != nil {
		t.Fatalf("ParseCurl(ToCurl(...)): %v", err)
	}
	if reparsed.Body != spec.Body {
		t.Fatalf("expected body %q to survive quoting, got %q (curl: %s)", spec.Body, reparsed.Body, out)
	}
}

// TestToCurlIncludesQueryParams is a regression test: ToCurl used to
// silently drop Query entirely, producing a curl command that didn't
// reproduce the actual request being copied.
func TestToCurlIncludesQueryParams(t *testing.T) {
	spec := RequestSpec{
		Method: "GET",
		URL:    "https://example.com/items",
		Query:  []KV{{Key: "limit", Value: "10"}, {Key: "skip", Value: "x", Disabled: true}},
	}
	out := ToCurl(spec)
	if !strings.Contains(out, "https://example.com/items?limit=10") {
		t.Fatalf("expected the query param in the URL, got: %s", out)
	}
	if strings.Contains(out, "skip") {
		t.Fatalf("expected the disabled query param to be omitted, got: %s", out)
	}
}

func TestToCurlReflectsBearerAuth(t *testing.T) {
	spec := RequestSpec{Method: "GET", URL: "https://example.com", Auth: &Auth{Type: AuthBearer, Token: "tok123"}}
	out := ToCurl(spec)
	if !strings.Contains(out, "Authorization: Bearer tok123") {
		t.Fatalf("expected a Bearer Authorization header, got: %s", out)
	}
}

func TestToCurlReflectsBasicAuthAsUFlag(t *testing.T) {
	spec := RequestSpec{Method: "GET", URL: "https://example.com", Auth: &Auth{Type: AuthBasic, Username: "alice", Password: "hunter2"}}
	out := ToCurl(spec)
	if !strings.Contains(out, "-u 'alice:hunter2'") {
		t.Fatalf("expected -u alice:hunter2, got: %s", out)
	}
}

func TestToCurlReflectsAPIKeyInQuery(t *testing.T) {
	spec := RequestSpec{
		Method: "GET",
		URL:    "https://example.com",
		Auth:   &Auth{Type: AuthAPIKey, KeyName: "api_key", KeyValue: "k1", AddTo: "query"},
	}
	out := ToCurl(spec)
	if !strings.Contains(out, "https://example.com?api_key=k1") {
		t.Fatalf("expected the api key appended to the URL, got: %s", out)
	}
}

func TestToCurlReflectsFormDataFields(t *testing.T) {
	spec := RequestSpec{
		Method:     "POST",
		URL:        "https://example.com/upload",
		BodyMode:   "formdata",
		FormFields: []KV{{Key: "name", Value: "widget"}},
	}
	out := ToCurl(spec)
	if !strings.Contains(out, "-F 'name=widget'") {
		t.Fatalf("expected a -F name=widget flag, got: %s", out)
	}
}
