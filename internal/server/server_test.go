package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/apiclient"
	"github.com/addictedabhi/airmock/internal/config"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
)

// TestCreateMockViaAdminAPIServesOnGateway is the phase 1.1 end-to-end
// verification: create a mock through the admin REST API, then curl the
// gateway port and assert the rendered templated response.
func TestCreateMockViaAdminAPIServesOnGateway(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	cfg := &config.Config{AdminPort: 18720, GatewayPort: 18721, DataDir: dir}
	srv, err := New(cfg, db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		if err := srv.Run(ctx); err != nil && err != context.Canceled {
			t.Logf("server run: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond) // let both listeners come up

	def := mock.Definition{
		Name:         "greeting",
		ProtocolType: "rest",
		Method:       http.MethodGet,
		PathPattern:  "/greet/{name}",
		Enabled:      true,
		Response: mock.ResponseTemplate{
			StatusCode:   200,
			BodyTemplate: `{"msg":"hi {{.Request.PathParams.name}}"}`,
		},
	}
	body, _ := json.Marshal(def)

	adminBase := fmt.Sprintf("http://localhost:%d", cfg.AdminPort)
	resp, err := http.Post(adminBase+"/api/mocks", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("create mock: %v", err)
	}
	if resp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(resp.Body)
		t.Fatalf("expected 201, got %d: %s", resp.StatusCode, b)
	}
	resp.Body.Close()

	lresp, err := http.Get(adminBase + "/api/mocks")
	if err != nil {
		t.Fatalf("list mocks: %v", err)
	}
	defer lresp.Body.Close()
	if lresp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(lresp.Body)
		t.Fatalf("expected 200 listing mocks, got %d: %s", lresp.StatusCode, b)
	}

	gatewayBase := fmt.Sprintf("http://localhost:%d", cfg.GatewayPort)
	gresp, err := http.Get(gatewayBase + "/greet/alice")
	if err != nil {
		t.Fatalf("GET gateway: %v", err)
	}
	defer gresp.Body.Close()
	gbody, _ := io.ReadAll(gresp.Body)

	want := `{"msg":"hi alice"}`
	if string(gbody) != want {
		t.Fatalf("expected gateway body %q, got %q (status %d)", want, string(gbody), gresp.StatusCode)
	}
}

// TestOpenAPIImportScaffoldsAndServesTheSpecsExampleResponse is phase 1.7's
// end-to-end verification: import an OpenAPI doc through the admin API,
// then hit the scaffolded mock on the gateway and assert the spec's own
// example response comes back.
func TestOpenAPIImportScaffoldsAndServesTheSpecsExampleResponse(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	cfg := &config.Config{AdminPort: 18722, GatewayPort: 18723, DataDir: dir}
	srv, err := New(cfg, db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := srv.Run(ctx); err != nil && err != context.Canceled {
			t.Logf("server run: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	const spec = `{
		"openapi": "3.0.0",
		"info": {"title": "t", "version": "1"},
		"paths": {
			"/widgets/{id}": {
				"get": {
					"operationId": "getWidget",
					"parameters": [{"name":"id","in":"path","required":true,"schema":{"type":"string"}}],
					"responses": {
						"200": {
							"description": "OK",
							"content": {"application/json": {"example": {"id": "w1", "color": "blue"}}}
						}
					}
				}
			}
		}
	}`

	importBody, _ := json.Marshal(map[string]string{"openapiContent": spec})
	adminBase := fmt.Sprintf("http://localhost:%d", cfg.AdminPort)
	iresp, err := http.Post(adminBase+"/api/openapi/import", "application/json", bytes.NewReader(importBody))
	if err != nil {
		t.Fatalf("import OpenAPI: %v", err)
	}
	defer iresp.Body.Close()
	if iresp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(iresp.Body)
		t.Fatalf("expected 201 importing OpenAPI, got %d: %s", iresp.StatusCode, b)
	}

	gatewayBase := fmt.Sprintf("http://localhost:%d", cfg.GatewayPort)
	gresp, err := http.Get(gatewayBase + "/widgets/w1")
	if err != nil {
		t.Fatalf("GET scaffolded mock: %v", err)
	}
	defer gresp.Body.Close()
	if gresp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", gresp.StatusCode)
	}
	var result map[string]any
	if err := json.NewDecoder(gresp.Body).Decode(&result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result["color"] != "blue" {
		t.Fatalf("expected the spec's own example response, got %+v", result)
	}
}

// TestAPIClientCreatesCollectionAndExecutesRequest is phase 1.10's
// end-to-end verification: create a collection through the admin API, then
// execute one of its requests (against a real httptest.Server standing in
// for "some external API") through /api/apiclient/execute and assert the
// response-viewer data comes back correctly. (The plan's verify step also
// mentions asserting a resulting hit-log row; hit logging is phase 1.12,
// not yet built, so that half is deferred until then.)
func TestAPIClientCreatesCollectionAndExecutesRequest(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	cfg := &config.Config{AdminPort: 18724, GatewayPort: 18725, DataDir: dir}
	srv, err := New(cfg, db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := srv.Run(ctx); err != nil && err != context.Canceled {
			t.Logf("server run: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Api-Key") != "topsecret" {
			t.Errorf("expected substituted header, got %q", r.Header.Get("X-Api-Key"))
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"created":true}`))
	}))
	defer target.Close()

	adminBase := fmt.Sprintf("http://localhost:%d", cfg.AdminPort)

	collection := apiclient.Collection{
		Name: "Widgets",
		Items: []apiclient.Item{
			{
				Type: apiclient.ItemRequest,
				Name: "Create widget",
				Request: &apiclient.RequestSpec{
					Method:  "POST",
					URL:     target.URL + "/widgets",
					Headers: []apiclient.KV{{Key: "X-Api-Key", Value: "{{apiKey}}"}},
				},
			},
		},
	}
	collBody, _ := json.Marshal(collection)
	cresp, err := http.Post(adminBase+"/api/apiclient/collections", "application/json", bytes.NewReader(collBody))
	if err != nil {
		t.Fatalf("create collection: %v", err)
	}
	defer cresp.Body.Close()
	if cresp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(cresp.Body)
		t.Fatalf("expected 201 creating collection, got %d: %s", cresp.StatusCode, b)
	}
	var created apiclient.Collection
	if err := json.NewDecoder(cresp.Body).Decode(&created); err != nil {
		t.Fatalf("decode created collection: %v", err)
	}

	execBody, _ := json.Marshal(map[string]any{
		"spec":      created.Items[0].Request,
		"variables": map[string]string{"apiKey": "topsecret"},
	})
	eresp, err := http.Post(adminBase+"/api/apiclient/execute", "application/json", bytes.NewReader(execBody))
	if err != nil {
		t.Fatalf("execute request: %v", err)
	}
	defer eresp.Body.Close()
	var result apiclient.ExecutionResult
	if err := json.NewDecoder(eresp.Body).Decode(&result); err != nil {
		t.Fatalf("decode execution result: %v", err)
	}
	if result.Error != "" {
		t.Fatalf("unexpected execution error: %s", result.Error)
	}
	if result.StatusCode != 201 {
		t.Fatalf("expected 201, got %d", result.StatusCode)
	}
	if result.Body != `{"created":true}` {
		t.Fatalf("unexpected response-viewer body: %q", result.Body)
	}
}

// TestProxyCaptureAndPromoteToMock is phase 1.11's end-to-end verification:
// a proxy-mode mock forwards to a real httptest.Server and returns its
// real response; the capture is then promoted into a real mock via the
// admin API, and hitting that new mock replays the exact captured
// response with no upstream involved.
func TestProxyCaptureAndPromoteToMock(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	cfg := &config.Config{AdminPort: 18726, GatewayPort: 18727, DataDir: dir}
	srv, err := New(cfg, db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() {
		if err := srv.Run(ctx); err != nil && err != context.Canceled {
			t.Logf("server run: %v", err)
		}
	}()
	time.Sleep(200 * time.Millisecond)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
		w.Write([]byte(`{"id":"1","status":"pending"}`))
	}))
	defer upstream.Close()

	adminBase := fmt.Sprintf("http://localhost:%d", cfg.AdminPort)
	gatewayBase := fmt.Sprintf("http://localhost:%d", cfg.GatewayPort)

	proxyDef := mock.Definition{
		Name: "passthrough", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/proxy/*", Enabled: true,
		Mode:  "proxy",
		Proxy: &mock.ProxyConfig{TargetBaseURL: upstream.URL},
	}
	pbody, _ := json.Marshal(proxyDef)
	presp, err := http.Post(adminBase+"/api/mocks", "application/json", bytes.NewReader(pbody))
	if err != nil {
		t.Fatalf("create proxy mock: %v", err)
	}
	presp.Body.Close()

	gresp, err := http.Get(gatewayBase + "/proxy/orders/1")
	if err != nil {
		t.Fatalf("GET proxied path: %v", err)
	}
	gbody, _ := io.ReadAll(gresp.Body)
	gresp.Body.Close()
	if string(gbody) != `{"id":"1","status":"pending"}` {
		t.Fatalf("expected the real upstream response, got %q", gbody)
	}

	lresp, err := http.Get(adminBase + "/api/hitlog?mockId=")
	if err != nil {
		t.Fatalf("list hit logs: %v", err)
	}
	var entries []map[string]any
	if err := json.NewDecoder(lresp.Body).Decode(&entries); err != nil {
		t.Fatalf("decode hit logs: %v", err)
	}
	lresp.Body.Close()
	if len(entries) != 1 {
		t.Fatalf("expected exactly one captured hit-log entry, got %d: %+v", len(entries), entries)
	}
	capturedID := entries[0]["id"].(string)

	promoteResp, err := http.Post(adminBase+"/api/hitlog/"+capturedID+"/promote-to-mock", "application/json", nil)
	if err != nil {
		t.Fatalf("promote to mock: %v", err)
	}
	defer promoteResp.Body.Close()
	if promoteResp.StatusCode != http.StatusCreated {
		b, _ := io.ReadAll(promoteResp.Body)
		t.Fatalf("expected 201 promoting to mock, got %d: %s", promoteResp.StatusCode, b)
	}
	var promoted mock.Definition
	if err := json.NewDecoder(promoteResp.Body).Decode(&promoted); err != nil {
		t.Fatalf("decode promoted mock: %v", err)
	}

	// The promoted mock's PathPattern is the exact client-facing path that
	// was captured (/proxy/orders/1, the same URL a caller already used
	// against the wildcard proxy mock) — chi matches the more specific
	// exact route ahead of the wildcard, so this now replays the captured
	// response directly with no upstream involved, while any other path
	// under /proxy/* still falls through to the live proxy.
	if promoted.PathPattern != "/proxy/orders/1" {
		t.Fatalf("expected the promoted mock's path to be the captured client-facing path, got %q", promoted.PathPattern)
	}
	replayResp, err := http.Get(gatewayBase + "/proxy/orders/1")
	if err != nil {
		t.Fatalf("GET promoted mock: %v", err)
	}
	defer replayResp.Body.Close()
	replayBody, _ := io.ReadAll(replayResp.Body)
	if string(replayBody) != `{"id":"1","status":"pending"}` {
		t.Fatalf("expected the promoted mock to replay the captured response, got %q", replayBody)
	}
}

// TestLoadExistingMocksSurvivesAPanickingRow is a regression test for a real
// crash: chi.Mux.MethodFunc panics (not just errors) on a Method it doesn't
// recognize. The admin API's own create/update/import handlers now reject
// an invalid Method before it's ever persisted (see validateMockShape), but
// a row written before that validation existed — or inserted by any other
// means — must not be able to crash every future server startup. Here a
// bad row is written directly through the store (bypassing the API's
// validation entirely, simulating exactly that "already in the database"
// scenario), alongside a perfectly valid one, and startup must both survive
// and still bring the valid mock up.
func TestLoadExistingMocksSurvivesAPanickingRow(t *testing.T) {
	dir := t.TempDir()
	db, err := storage.Open(filepath.Join(dir, "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()

	mockStore := mock.NewStore(db)
	if _, err := mockStore.Create(&mock.Definition{
		Name: "bad-method", ProtocolType: "rest", Method: "BOGUS", PathPattern: "/bad", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: "{}"},
	}); err != nil {
		t.Fatalf("seed bad mock directly via the store: %v", err)
	}
	if _, err := mockStore.Create(&mock.Definition{
		Name: "good", ProtocolType: "rest", Method: "GET", PathPattern: "/good", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"ok":true}`},
	}); err != nil {
		t.Fatalf("seed good mock directly via the store: %v", err)
	}

	cfg := &config.Config{AdminPort: 18728, GatewayPort: 18729, DataDir: dir}
	srv, err := New(cfg, db)
	if err != nil {
		t.Fatalf("New: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runErr := make(chan error, 1)
	go func() { runErr <- srv.Run(ctx) }()
	time.Sleep(200 * time.Millisecond)

	select {
	case err := <-runErr:
		t.Fatalf("server exited unexpectedly (should still be running): %v", err)
	default:
	}

	resp, err := http.Get(fmt.Sprintf("http://localhost:%d/good", cfg.GatewayPort))
	if err != nil {
		t.Fatalf("GET /good: %v", err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"ok":true}` {
		t.Fatalf("expected the valid mock to still be registered and serving, got %q", body)
	}
}
