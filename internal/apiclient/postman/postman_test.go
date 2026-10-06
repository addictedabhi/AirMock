package postman

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"github.com/addictedabhi/airmock/internal/apiclient"
)

// stripIDs zeroes apiclient.Item.ID recursively — Postman's format has no
// concept of AirMock's internal IDs, so a semantic-equivalence diff must
// ignore them rather than treating their absence after import as a
// mismatch.
func stripIDs(items []apiclient.Item) []apiclient.Item {
	out := make([]apiclient.Item, len(items))
	for i, it := range items {
		it.ID = ""
		if len(it.Items) > 0 {
			it.Items = stripIDs(it.Items)
		}
		out[i] = it
	}
	return out
}

func TestImportExportRoundTripIsSemanticallyEquivalent(t *testing.T) {
	original := apiclient.Collection{
		Name: "Orders API",
		Items: []apiclient.Item{
			{
				Type: apiclient.ItemFolder,
				Name: "Orders",
				Items: []apiclient.Item{
					{
						Type: apiclient.ItemRequest,
						Name: "Get Order",
						Request: &apiclient.RequestSpec{
							Method:  "GET",
							URL:     "https://example.com/orders/1",
							Headers: []apiclient.KV{{Key: "X-Api-Key", Value: "{{apiKey}}"}},
							Query:   []apiclient.KV{{Key: "verbose", Value: "true", Disabled: true}},
						},
					},
					{
						Type: apiclient.ItemRequest,
						Name: "Create Order",
						Request: &apiclient.RequestSpec{
							Method:  "POST",
							URL:     "https://example.com/orders",
							Headers: []apiclient.KV{{Key: "Content-Type", Value: "application/json"}},
							Body:    `{"customerName":"Ada"}`,
						},
					},
				},
			},
			{
				Type: apiclient.ItemWSRequest,
				Name: "Order Events",
				WSRequest: &apiclient.WSRequestSpec{
					URL:     "wss://example.com/events",
					Headers: []apiclient.KV{{Key: "Authorization", Value: "Bearer {{token}}"}},
					Message: `{"subscribe":"orders"}`,
				},
			},
		},
	}

	exported, err := Export(original)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	imported, err := Import(exported)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	if imported.Name != original.Name {
		t.Fatalf("expected name %q, got %q", original.Name, imported.Name)
	}

	got := stripIDs(imported.Items)
	want := stripIDs(original.Items)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("semantic diff after import<-export round trip:\n  want: %+v\n  got:  %+v", want, got)
	}
}

func TestImportExportRoundTripPreservesBodyModeAndAuth(t *testing.T) {
	original := apiclient.Collection{
		Name: "c",
		Items: []apiclient.Item{
			{
				Type: apiclient.ItemRequest, Name: "raw with content type",
				Request: &apiclient.RequestSpec{Method: "POST", URL: "https://x/1", Body: "<a/>", RawContentType: "xml"},
			},
			{
				Type: apiclient.ItemRequest, Name: "urlencoded",
				Request: &apiclient.RequestSpec{
					Method: "POST", URL: "https://x/2", BodyMode: "urlencoded",
					FormFields: []apiclient.KV{{Key: "a", Value: "1"}, {Key: "b", Value: "2", Disabled: true}},
				},
			},
			{
				Type: apiclient.ItemRequest, Name: "formdata",
				Request: &apiclient.RequestSpec{
					Method: "POST", URL: "https://x/3", BodyMode: "formdata",
					FormFields: []apiclient.KV{{Key: "file", Value: "ignored-no-upload-support"}},
				},
			},
			{
				Type: apiclient.ItemRequest, Name: "no body",
				Request: &apiclient.RequestSpec{Method: "DELETE", URL: "https://x/4", BodyMode: "none"},
			},
			{
				Type: apiclient.ItemRequest, Name: "bearer auth",
				Request: &apiclient.RequestSpec{Method: "GET", URL: "https://x/5", Auth: &apiclient.Auth{Type: apiclient.AuthBearer, Token: "{{tok}}"}},
			},
			{
				Type: apiclient.ItemRequest, Name: "basic auth",
				Request: &apiclient.RequestSpec{Method: "GET", URL: "https://x/6", Auth: &apiclient.Auth{Type: apiclient.AuthBasic, Username: "u", Password: "p"}},
			},
			{
				Type: apiclient.ItemRequest, Name: "apikey auth in query",
				Request: &apiclient.RequestSpec{Method: "GET", URL: "https://x/7", Auth: &apiclient.Auth{Type: apiclient.AuthAPIKey, KeyName: "api_key", KeyValue: "k1", AddTo: "query"}},
			},
		},
	}

	exported, err := Export(original)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	imported, err := Import(exported)
	if err != nil {
		t.Fatalf("Import: %v", err)
	}

	got := stripIDs(imported.Items)
	want := stripIDs(original.Items)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("semantic diff after import<-export round trip:\n  want: %+v\n  got:  %+v", want, got)
	}
}

func TestExportedWSItemIsPlainPostmanCompatible(t *testing.T) {
	c := apiclient.Collection{
		Name: "c",
		Items: []apiclient.Item{
			{Type: apiclient.ItemWSRequest, Name: "ws", WSRequest: &apiclient.WSRequestSpec{URL: "wss://x"}},
		},
	}
	data, err := Export(c)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}

	// A plain Postman client would unmarshal into the standard schema and
	// simply not recognize x-airmock-ws — it must not choke on the file,
	// and the item must carry no (potentially confusing) "request" field.
	var plain Collection
	if err := json.Unmarshal(data, &plain); err != nil {
		t.Fatalf("a plain Postman-shaped unmarshal failed: %v", err)
	}
	if plain.Item[0].Request != nil {
		t.Fatalf("expected no request field on a WS item, got %+v", plain.Item[0].Request)
	}
}

// TestImportOAuth2WithCachedTokenMapsToBearer guards against a real gap:
// importing a Postman request with OAuth2 auth used to silently drop the
// credentials entirely (falling to the `default` case, same as any
// unrecognized auth type) — a real, common case, since Postman collections
// frequently use OAuth2. A cached access token (present once a user has
// clicked "Get New Access Token" in real Postman) is functionally a bearer
// token once a request is actually sent, so it's imported as one instead of
// being lost.
func TestImportOAuth2WithCachedTokenMapsToBearer(t *testing.T) {
	raw := `{
		"info": {"name": "OAuth2 collection"},
		"item": [{
			"name": "Get profile",
			"request": {
				"method": "GET",
				"url": {"raw": "https://api.example.com/me"},
				"auth": {
					"type": "oauth2",
					"oauth2": [{"key": "accessToken", "value": "cached-token-abc"}]
				}
			}
		}]
	}`
	col, err := Import([]byte(raw))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	auth := col.Items[0].Request.Auth
	if auth == nil || auth.Type != apiclient.AuthBearer || auth.Token != "cached-token-abc" {
		t.Fatalf("expected AuthBearer with the cached access token, got %+v", auth)
	}
}

// TestImportOAuth2WithoutCachedTokenFallsBackToNone confirms an OAuth2 auth
// block with no cached access token (e.g. a client-credentials flow never
// actually run) has nothing usable to carry over, so it falls back to
// AuthNone rather than fabricating credentials.
func TestImportOAuth2WithoutCachedTokenFallsBackToNone(t *testing.T) {
	raw := `{
		"info": {"name": "OAuth2 collection"},
		"item": [{
			"name": "Get profile",
			"request": {
				"method": "GET",
				"url": {"raw": "https://api.example.com/me"},
				"auth": {"type": "oauth2", "oauth2": [{"key": "grantType", "value": "client_credentials"}]}
			}
		}]
	}`
	col, err := Import([]byte(raw))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	auth := col.Items[0].Request.Auth
	if auth == nil || auth.Type != apiclient.AuthNone {
		t.Fatalf("expected AuthNone with no cached token, got %+v", auth)
	}
}

// TestImportPreservesCollectionLevelVariables guards against a real gap:
// a Postman collection's top-level `variable` array (e.g. a base URL every
// request in the collection references) was silently discarded on import —
// there was nowhere on apiclient.Collection to even put it.
func TestImportPreservesCollectionLevelVariables(t *testing.T) {
	raw := `{
		"info": {"name": "Orders API"},
		"item": [],
		"variable": [
			{"key": "baseUrl", "value": "https://api.example.com", "type": "string"},
			{"key": "apiVersion", "value": "v2"}
		]
	}`
	col, err := Import([]byte(raw))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if col.Variables["baseUrl"] != "https://api.example.com" || col.Variables["apiVersion"] != "v2" {
		t.Fatalf("expected both collection variables preserved, got %+v", col.Variables)
	}
}

func TestExportIncludesCollectionLevelVariables(t *testing.T) {
	c := apiclient.Collection{Name: "c", Variables: map[string]string{"baseUrl": "https://x.example.com"}}
	data, err := Export(c)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if !strings.Contains(string(data), `"baseUrl"`) || !strings.Contains(string(data), `"https://x.example.com"`) {
		t.Fatalf("expected the collection variable in the exported JSON, got %s", data)
	}
}

// TestImportParsesSavedExampleResponses guards a real gap: Item had no
// Response field at all, so a real Postman collection's saved example
// responses (often the main reason a collection is worth sharing —
// documented happy-path plus every error case) were silently dropped on
// import, every single one, with no error or warning.
func TestImportParsesSavedExampleResponses(t *testing.T) {
	const raw = `{
		"info": {"name": "X", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
		"item": [
			{
				"name": "Get Order",
				"request": {"method": "GET", "header": [], "url": {"raw": "https://example.com/order"}},
				"response": [
					{
						"name": "200 OK",
						"status": "OK",
						"code": 200,
						"header": [{"key": "Content-Type", "value": "application/json"}],
						"body": "{\"id\": 1}"
					},
					{
						"name": "404 Not Found",
						"code": 404,
						"body": "{\"error\": \"not found\"}"
					}
				]
			}
		]
	}`
	c, err := Import([]byte(raw))
	if err != nil {
		t.Fatalf("Import: %v", err)
	}
	if len(c.Items) != 1 || len(c.Items[0].Examples) != 2 {
		t.Fatalf("expected 1 item with 2 examples, got %+v", c.Items)
	}
	ex := c.Items[0].Examples
	if ex[0].ID == "" || ex[1].ID == "" {
		t.Error("expected a generated ID for both examples")
	}
	if ex[0].Name != "200 OK" || ex[0].StatusCode != 200 || ex[0].Body != `{"id": 1}` {
		t.Errorf("unexpected first example: %+v", ex[0])
	}
	if ex[0].Headers["Content-Type"] != "application/json" {
		t.Errorf("expected the Content-Type header to carry over, got %+v", ex[0].Headers)
	}
	if ex[1].Name != "404 Not Found" || ex[1].StatusCode != 404 || ex[1].Body != `{"error": "not found"}` {
		t.Errorf("unexpected second example: %+v", ex[1])
	}
}

// TestExportIncludesSavedExampleResponses is the reverse direction: an
// AirMock collection's own saved Examples must appear in the exported
// Postman JSON's response[] array, with the item's own request snapshotted
// as each example's originalRequest (matching how a real Postman-saved
// example works).
func TestExportIncludesSavedExampleResponses(t *testing.T) {
	c := apiclient.Collection{
		Name: "X",
		Items: []apiclient.Item{
			{
				Type:    apiclient.ItemRequest,
				Name:    "Get Order",
				Request: &apiclient.RequestSpec{Method: "GET", URL: "https://example.com/order"},
				Examples: []apiclient.Example{
					{ID: "e1", Name: "200 OK", StatusCode: 200, Body: `{"id": 1}`},
				},
			},
		},
	}
	data, err := Export(c)
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	var pc Collection
	if err := json.Unmarshal(data, &pc); err != nil {
		t.Fatalf("decode exported json: %v", err)
	}
	if len(pc.Item) != 1 || len(pc.Item[0].Response) != 1 {
		t.Fatalf("expected 1 item with 1 response, got %+v", pc.Item)
	}
	r := pc.Item[0].Response[0]
	if r.Name != "200 OK" || r.Code != 200 || r.Body != `{"id": 1}` {
		t.Errorf("unexpected exported response: %+v", r)
	}
	if r.OriginalRequest == nil || r.OriginalRequest.URL.Raw != "https://example.com/order" {
		t.Errorf("expected originalRequest to carry the item's own request, got %+v", r.OriginalRequest)
	}
}

// TestImportEnvironmentParsesPostmanEnvironmentFile guards against a real
// gap: there was no way to import a Postman *.postman_environment.json
// export at all, only a collection — a real Postman workflow commonly
// ships these as two separate files.
func TestImportEnvironmentParsesPostmanEnvironmentFile(t *testing.T) {
	raw := `{
		"id": "abc-123",
		"name": "Staging",
		"values": [
			{"key": "baseUrl", "value": "https://staging.example.com", "enabled": true},
			{"key": "apiKey", "value": "secret", "enabled": true},
			{"key": "unused", "value": "old-value", "enabled": false}
		],
		"_postman_variable_scope": "environment"
	}`
	env, err := ImportEnvironment([]byte(raw))
	if err != nil {
		t.Fatalf("ImportEnvironment: %v", err)
	}
	if env.Name != "Staging" {
		t.Fatalf("expected name Staging, got %q", env.Name)
	}
	if env.Variables["baseUrl"] != "https://staging.example.com" || env.Variables["apiKey"] != "secret" {
		t.Fatalf("expected both enabled variables, got %+v", env.Variables)
	}
	if _, ok := env.Variables["unused"]; ok {
		t.Fatalf("expected the disabled variable to be skipped, got %+v", env.Variables)
	}
}
