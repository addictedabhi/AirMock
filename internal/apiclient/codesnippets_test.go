package apiclient

import (
	"encoding/base64"
	"strings"
	"testing"
)

func TestToJSFetchBasicGet(t *testing.T) {
	got := ToJSFetch(RequestSpec{Method: "GET", URL: "https://example.com/orders/1"})
	if !strings.Contains(got, `fetch("https://example.com/orders/1"`) {
		t.Fatalf("expected the URL in the fetch call, got %q", got)
	}
	if !strings.Contains(got, `method: "GET"`) {
		t.Fatalf("expected the method set, got %q", got)
	}
}

func TestToJSFetchIncludesQueryParams(t *testing.T) {
	got := ToJSFetch(RequestSpec{Method: "GET", URL: "https://example.com/orders", Query: []KV{{Key: "status", Value: "open"}}})
	if !strings.Contains(got, "status=open") {
		t.Fatalf("expected the query param appended to the URL, got %q", got)
	}
}

func TestToJSFetchIncludesHeaders(t *testing.T) {
	got := ToJSFetch(RequestSpec{
		Method: "GET", URL: "https://example.com",
		Headers: []KV{{Key: "X-Api-Key", Value: "abc"}, {Key: "X-Disabled", Value: "skip", Disabled: true}},
	})
	if !strings.Contains(got, `"X-Api-Key": "abc"`) {
		t.Fatalf("expected the enabled header, got %q", got)
	}
	if strings.Contains(got, "X-Disabled") {
		t.Fatalf("expected the disabled header to be omitted, got %q", got)
	}
}

func TestToJSFetchRawJSONBodyGetsDefaultContentType(t *testing.T) {
	got := ToJSFetch(RequestSpec{Method: "POST", URL: "https://example.com", Body: `{"foo":"bar"}`})
	if !strings.Contains(got, `"Content-Type": "application/json"`) {
		t.Fatalf("expected an inferred JSON content type, got %q", got)
	}
	if !strings.Contains(got, `body: "{\"foo\":\"bar\"}"`) {
		t.Fatalf("expected the raw body as an escaped string literal, got %q", got)
	}
}

func TestToJSFetchExplicitContentTypeWinsOverInferredOne(t *testing.T) {
	got := ToJSFetch(RequestSpec{
		Method: "POST", URL: "https://example.com", Body: "<a/>", RawContentType: "xml",
		Headers: []KV{{Key: "Content-Type", Value: "application/xml; charset=utf-8"}},
	})
	if strings.Count(got, "Content-Type") != 1 {
		t.Fatalf("expected exactly one Content-Type header (the explicit one), got %q", got)
	}
}

func TestToJSFetchBearerAuthAddsAuthorizationHeader(t *testing.T) {
	got := ToJSFetch(RequestSpec{Method: "GET", URL: "https://example.com", Auth: &Auth{Type: AuthBearer, Token: "xyz"}})
	if !strings.Contains(got, `"Authorization": "Bearer xyz"`) {
		t.Fatalf("expected a bearer Authorization header, got %q", got)
	}
}

func TestToJSFetchBasicAuthPreEncodesCredentials(t *testing.T) {
	got := ToJSFetch(RequestSpec{Method: "GET", URL: "https://example.com", Auth: &Auth{Type: AuthBasic, Username: "user", Password: "pass"}})
	want := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:pass"))
	if !strings.Contains(got, want) {
		t.Fatalf("expected pre-encoded basic auth %q, got %q", want, got)
	}
}

func TestToJSFetchAPIKeyInQueryAppendsToURL(t *testing.T) {
	got := ToJSFetch(RequestSpec{
		Method: "GET", URL: "https://example.com",
		Auth: &Auth{Type: AuthAPIKey, AddTo: "query", KeyName: "api_key", KeyValue: "secret"},
	})
	if !strings.Contains(got, "api_key=secret") {
		t.Fatalf("expected the api key appended as a query param, got %q", got)
	}
}

func TestToJSFetchURLEncodedBodyUsesURLSearchParams(t *testing.T) {
	got := ToJSFetch(RequestSpec{
		Method: "POST", URL: "https://example.com", BodyMode: "urlencoded",
		FormFields: []KV{{Key: "a", Value: "1"}},
	})
	if !strings.Contains(got, "new URLSearchParams({") {
		t.Fatalf("expected URLSearchParams for urlencoded body, got %q", got)
	}
}

func TestToJSFetchFormDataBodyUsesFormDataAPI(t *testing.T) {
	got := ToJSFetch(RequestSpec{
		Method: "POST", URL: "https://example.com", BodyMode: "formdata",
		FormFields: []KV{{Key: "a", Value: "1"}},
	})
	if !strings.Contains(got, "new FormData()") || !strings.Contains(got, `fd.append("a", "1")`) {
		t.Fatalf("expected a FormData construction, got %q", got)
	}
}

func TestToJSFetchNoneBodyModeOmitsBody(t *testing.T) {
	got := ToJSFetch(RequestSpec{Method: "POST", URL: "https://example.com", BodyMode: "none", Body: "should be ignored"})
	if strings.Contains(got, "body:") {
		t.Fatalf("expected no body key for bodyMode=none, got %q", got)
	}
}

func TestToPythonRequestsBasicGet(t *testing.T) {
	got := ToPythonRequests(RequestSpec{Method: "GET", URL: "https://example.com/orders/1"})
	if !strings.Contains(got, "import requests") {
		t.Fatalf("expected the requests import, got %q", got)
	}
	if !strings.Contains(got, "requests.get(url") {
		t.Fatalf("expected requests.get(url, got %q", got)
	}
}

func TestToPythonRequestsCustomMethodUsesRequestFunction(t *testing.T) {
	got := ToPythonRequests(RequestSpec{Method: "PURGE", URL: "https://example.com"})
	if !strings.Contains(got, "requests.request('PURGE', url") {
		t.Fatalf("expected requests.request('PURGE', url for a non-standard method, got %q", got)
	}
}

func TestToPythonRequestsQueryParamsBuildParamsDict(t *testing.T) {
	got := ToPythonRequests(RequestSpec{Method: "GET", URL: "https://example.com", Query: []KV{{Key: "status", Value: "open"}}})
	if !strings.Contains(got, "params = {") || !strings.Contains(got, "params=params") {
		t.Fatalf("expected a params dict passed to the call, got %q", got)
	}
}

// TestToPythonRequestsAPIKeyInQueryWithNoOtherQueryParamsStillDeclaresParamsDict
// guards a real ordering bug hit during development: building the params
// dict only from spec.Query (before folding in an API-key-in-query auth)
// meant a request with ONLY an api-key-in-query auth and no other query
// params emitted a bare `params['key'] = 'value'` referencing a `params`
// dict that was never declared — a NameError in the generated script.
func TestToPythonRequestsAPIKeyInQueryWithNoOtherQueryParamsStillDeclaresParamsDict(t *testing.T) {
	got := ToPythonRequests(RequestSpec{
		Method: "GET", URL: "https://example.com",
		Auth: &Auth{Type: AuthAPIKey, AddTo: "query", KeyName: "api_key", KeyValue: "secret"},
	})
	if !strings.Contains(got, "params = {") {
		t.Fatalf("expected the params dict to be declared, got %q", got)
	}
	if !strings.Contains(got, "'api_key': 'secret'") {
		t.Fatalf("expected the api key inside the declared params dict, got %q", got)
	}
	if !strings.Contains(got, "params=params") {
		t.Fatalf("expected params passed to the requests call, got %q", got)
	}
}

func TestToPythonRequestsBasicAuthUsesAuthTuple(t *testing.T) {
	got := ToPythonRequests(RequestSpec{Method: "GET", URL: "https://example.com", Auth: &Auth{Type: AuthBasic, Username: "user", Password: "pass"}})
	if !strings.Contains(got, "auth = ('user', 'pass')") {
		t.Fatalf("expected an auth tuple, got %q", got)
	}
	if !strings.Contains(got, "auth=auth") {
		t.Fatalf("expected auth passed to the requests call, got %q", got)
	}
}

func TestToPythonRequestsBearerAuthAddsAuthorizationHeader(t *testing.T) {
	got := ToPythonRequests(RequestSpec{Method: "GET", URL: "https://example.com", Auth: &Auth{Type: AuthBearer, Token: "xyz"}})
	if !strings.Contains(got, "'Authorization': 'Bearer xyz'") {
		t.Fatalf("expected a bearer Authorization header, got %q", got)
	}
}

func TestToPythonRequestsRawBodyGetsDefaultContentTypeAndDataVar(t *testing.T) {
	got := ToPythonRequests(RequestSpec{Method: "POST", URL: "https://example.com", Body: `{"foo":"bar"}`})
	if !strings.Contains(got, "'Content-Type': 'application/json'") {
		t.Fatalf("expected an inferred JSON content type, got %q", got)
	}
	if !strings.Contains(got, `data = '{"foo":"bar"}'`) || !strings.Contains(got, "data=data") {
		t.Fatalf("expected the raw body assigned to data and passed to the call, got %q", got)
	}
}

func TestToPythonRequestsFormFieldsBuildDataDict(t *testing.T) {
	got := ToPythonRequests(RequestSpec{
		Method: "POST", URL: "https://example.com", BodyMode: "urlencoded",
		FormFields: []KV{{Key: "a", Value: "1"}},
	})
	if !strings.Contains(got, "data = {") || !strings.Contains(got, "'a': '1'") {
		t.Fatalf("expected a data dict with the form field, got %q", got)
	}
}

func TestToPythonRequestsNoneBodyModeOmitsData(t *testing.T) {
	got := ToPythonRequests(RequestSpec{Method: "POST", URL: "https://example.com", BodyMode: "none", Body: "should be ignored"})
	if strings.Contains(got, "data") {
		t.Fatalf("expected no data variable for bodyMode=none, got %q", got)
	}
}

func TestToJSFetchAndToPythonRequestsPreserveVarPlaceholders(t *testing.T) {
	spec := RequestSpec{Method: "GET", URL: "{{baseUrl}}/orders", Headers: []KV{{Key: "Authorization", Value: "Bearer {{token}}"}}}
	if got := ToJSFetch(spec); !strings.Contains(got, "{{baseUrl}}") || !strings.Contains(got, "{{token}}") {
		t.Fatalf("expected {{var}} placeholders preserved verbatim in the JS snippet, got %q", got)
	}
	if got := ToPythonRequests(spec); !strings.Contains(got, "{{baseUrl}}") || !strings.Contains(got, "{{token}}") {
		t.Fatalf("expected {{var}} placeholders preserved verbatim in the Python snippet, got %q", got)
	}
}

func TestToGoBasicGet(t *testing.T) {
	got := ToGo(RequestSpec{Method: "GET", URL: "https://example.com/orders/1"})
	if !strings.Contains(got, `http.NewRequest("GET", "https://example.com/orders/1", nil)`) {
		t.Fatalf("expected the method/URL/nil-body in the NewRequest call, got %q", got)
	}
	if !strings.Contains(got, "package main") || !strings.Contains(got, `"net/http"`) {
		t.Fatalf("expected a valid Go program shell, got %q", got)
	}
	if strings.Contains(got, `"strings"`) {
		t.Fatalf("expected no unused strings import for a bodyless GET, got %q", got)
	}
}

func TestToGoIncludesQueryParams(t *testing.T) {
	got := ToGo(RequestSpec{Method: "GET", URL: "https://example.com/orders", Query: []KV{{Key: "status", Value: "open"}}})
	if !strings.Contains(got, "status=open") {
		t.Fatalf("expected the query param appended to the URL, got %q", got)
	}
}

func TestToGoIncludesHeaders(t *testing.T) {
	got := ToGo(RequestSpec{
		Method: "GET", URL: "https://example.com",
		Headers: []KV{{Key: "X-Api-Key", Value: "abc"}, {Key: "X-Disabled", Value: "skip", Disabled: true}},
	})
	if !strings.Contains(got, `req.Header.Set("X-Api-Key", "abc")`) {
		t.Fatalf("expected the enabled header set, got %q", got)
	}
	if strings.Contains(got, "X-Disabled") {
		t.Fatalf("expected the disabled header to be omitted, got %q", got)
	}
}

func TestToGoRawJSONBodyGetsDefaultContentTypeAndImport(t *testing.T) {
	got := ToGo(RequestSpec{Method: "POST", URL: "https://example.com", Body: `{"foo":"bar"}`})
	if !strings.Contains(got, `req.Header.Set("Content-Type", "application/json")`) {
		t.Fatalf("expected an inferred JSON content type, got %q", got)
	}
	if !strings.Contains(got, `strings.NewReader("{\"foo\":\"bar\"}")`) {
		t.Fatalf("expected the raw body wrapped in strings.NewReader, got %q", got)
	}
	if !strings.Contains(got, `"strings"`) {
		t.Fatalf("expected the strings import when a raw body is present, got %q", got)
	}
}

func TestToGoBasicAuthUsesSetBasicAuth(t *testing.T) {
	got := ToGo(RequestSpec{Method: "GET", URL: "https://example.com", Auth: &Auth{Type: AuthBasic, Username: "u", Password: "p"}})
	if !strings.Contains(got, `req.SetBasicAuth("u", "p")`) {
		t.Fatalf("expected req.SetBasicAuth with the credentials, got %q", got)
	}
}

func TestToGoBearerAuthAddsAuthorizationHeader(t *testing.T) {
	got := ToGo(RequestSpec{Method: "GET", URL: "https://example.com", Auth: &Auth{Type: AuthBearer, Token: "tok"}})
	if !strings.Contains(got, `req.Header.Set("Authorization", "Bearer tok")`) {
		t.Fatalf("expected an Authorization header with the bearer token, got %q", got)
	}
}

func TestToGoURLEncodedBodyUsesURLValuesAndImportsNetURL(t *testing.T) {
	got := ToGo(RequestSpec{Method: "POST", URL: "https://example.com", BodyMode: "urlencoded", FormFields: []KV{{Key: "a", Value: "1"}}})
	if !strings.Contains(got, `form.Set("a", "1")`) || !strings.Contains(got, "strings.NewReader(form.Encode())") {
		t.Fatalf("expected url.Values-based form building, got %q", got)
	}
	if !strings.Contains(got, `"net/url"`) {
		t.Fatalf("expected the net/url import for a urlencoded body, got %q", got)
	}
}

func TestToGoFormDataBodyUsesMultipartWriter(t *testing.T) {
	got := ToGo(RequestSpec{Method: "POST", URL: "https://example.com", BodyMode: "formdata", FormFields: []KV{{Key: "a", Value: "1"}}})
	if !strings.Contains(got, "multipart.NewWriter(&buf)") || !strings.Contains(got, `mw.WriteField("a", "1")`) {
		t.Fatalf("expected a multipart.Writer building the form, got %q", got)
	}
	if !strings.Contains(got, "mw.FormDataContentType()") {
		t.Fatalf("expected the Content-Type to come from the multipart writer itself, got %q", got)
	}
	if !strings.Contains(got, `"bytes"`) || !strings.Contains(got, `"mime/multipart"`) {
		t.Fatalf("expected bytes and mime/multipart imports, got %q", got)
	}
}

// TestToGoFormDataBodyDropsUserSuppliedContentTypeHeader guards against a
// real gap: goBodyParts' formdata branch always unconditionally emitted its
// own Content-Type line (correctly — the multipart boundary can only come
// from mw.FormDataContentType() at runtime), but ToGo's header loop ALSO
// emitted the user's own stray Content-Type header verbatim beforehand,
// producing generated code with two conflicting-looking
// req.Header.Set("Content-Type", ...) calls. Go's Header.Set means the
// request itself was never actually broken (last one wins), but the
// generated snippet must show exactly one Content-Type line, not dead code
// that reads like a bug.
func TestToGoFormDataBodyDropsUserSuppliedContentTypeHeader(t *testing.T) {
	got := ToGo(RequestSpec{
		Method: "POST", URL: "https://example.com", BodyMode: "formdata",
		Headers:    []KV{{Key: "Content-Type", Value: "multipart/form-data"}, {Key: "X-Api-Key", Value: "abc"}},
		FormFields: []KV{{Key: "a", Value: "1"}},
	})
	if strings.Count(got, `Header.Set("Content-Type"`) != 1 {
		t.Fatalf("expected exactly one Content-Type header line, got %q", got)
	}
	if !strings.Contains(got, "mw.FormDataContentType()") {
		t.Fatalf("expected the surviving Content-Type line to come from the multipart writer, got %q", got)
	}
	if !strings.Contains(got, `req.Header.Set("X-Api-Key", "abc")`) {
		t.Fatalf("expected the other, unrelated header to be preserved, got %q", got)
	}
}

func TestToGoNoneBodyModeOmitsBodyAndStringsImport(t *testing.T) {
	got := ToGo(RequestSpec{Method: "POST", URL: "https://example.com", BodyMode: "none", Body: "should be ignored"})
	if !strings.Contains(got, `http.NewRequest("POST", "https://example.com", nil)`) {
		t.Fatalf("expected a nil body for bodyMode=none, got %q", got)
	}
	if strings.Contains(got, `"strings"`) {
		t.Fatalf("expected no unused strings import for bodyMode=none, got %q", got)
	}
}

func TestToGoPreservesVarPlaceholders(t *testing.T) {
	spec := RequestSpec{Method: "GET", URL: "{{baseUrl}}/orders", Headers: []KV{{Key: "Authorization", Value: "Bearer {{token}}"}}}
	got := ToGo(spec)
	if !strings.Contains(got, "{{baseUrl}}") || !strings.Contains(got, "{{token}}") {
		t.Fatalf("expected {{var}} placeholders preserved verbatim, got %q", got)
	}
}
