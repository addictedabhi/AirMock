package httpengine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
)

func TestGraphQLDispatchesByOperationName(t *testing.T) {
	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "g1", ProtocolType: "graphql", Method: http.MethodPost, PathPattern: "/graphql", Enabled: true,
		OperationName: "GetOrder",
		Response:      mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"data":{"order":{"status":"pending"}}}`},
	})
	e.RegisterMock(&mock.Definition{
		ID: "g2", ProtocolType: "graphql", Method: http.MethodPost, PathPattern: "/graphql", Enabled: true,
		OperationName: "CreateOrder",
		Response:      mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"data":{"createOrder":{"id":"new-1"}}}`},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18780"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"query":         `query GetOrder { order(id: 1) { status } }`,
		"operationName": "GetOrder",
	})
	resp, err := http.Post("http://"+addr+"/graphql", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	want := `{"data":{"order":{"status":"pending"}}}`
	if string(respBody) != want {
		t.Fatalf("expected %q, got %q", want, respBody)
	}
}

func TestGraphQLFallsBackToAnonymousOperationFieldName(t *testing.T) {
	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "g3", ProtocolType: "graphql", Method: http.MethodPost, PathPattern: "/graphql2", Enabled: true,
		OperationName: "order",
		Response:      mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"data":{"order":{"status":"shipped"}}}`},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18781"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// No operationName in the request at all — an anonymous query shorthand.
	body, _ := json.Marshal(map[string]any{"query": `{ order(id: 1) { status } }`})
	resp, err := http.Post("http://"+addr+"/graphql2", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	want := `{"data":{"order":{"status":"shipped"}}}`
	if string(respBody) != want {
		t.Fatalf("expected %q, got %q", want, respBody)
	}
}

func TestGraphQLMismatchedOperationNameFallsThroughToNoMatch(t *testing.T) {
	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "g4", ProtocolType: "graphql", Method: http.MethodPost, PathPattern: "/graphql3", Enabled: true,
		OperationName: "GetOrder",
		Response:      mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"data":{}}`},
	})
	e.RegisterMock(&mock.Definition{
		ID: "g5", ProtocolType: "graphql", Method: http.MethodPost, PathPattern: "/graphql3", Enabled: true,
		OperationName: "CreateOrder",
		Response:      mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"data":{}}`},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18782"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	body, _ := json.Marshal(map[string]any{
		"query":         `query DeleteOrder { deleteOrder(id: 1) }`,
		"operationName": "DeleteOrder",
	})
	resp, err := http.Post("http://"+addr+"/graphql3", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()

	var result map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, ok := result["errors"]; !ok {
		t.Fatalf("expected a GraphQL errors response for a mismatched operation name, got %+v", result)
	}
}

// TestGraphQLAsyncModeAcksThenDeliversCallback guards against the same bug
// as SOAP's equivalent test: GraphQL's matcher used to never check
// Mode=="async" at all, so an async GraphQL mock just answered
// synchronously forever with the ack/callback silently never firing.
func TestGraphQLAsyncModeAcksThenDeliversCallback(t *testing.T) {
	var callbackHits int32
	callbackSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&callbackHits, 1)
		w.WriteHeader(http.StatusOK)
	}))
	defer callbackSrv.Close()

	store := newTestMockStore(t)
	e := New()
	e.SetCallbackScheduler(store)
	worker := mock.NewCallbackWorker(store, nil, nil)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	worker.Start(ctx)

	def := &mock.Definition{
		ID: "gql-async", ProtocolType: "graphql", Method: http.MethodPost, PathPattern: "/graphql", Enabled: true, Mode: "async",
		OperationName: "PlaceOrder",
		AsyncConfig: &mock.AsyncConfig{
			AckResponse:          mock.ResponseTemplate{StatusCode: 202, BodyTemplate: `{"data":{"placeOrder":{"accepted":true}}}`},
			CallbackTargetMode:   "fixed",
			CallbackFixedURL:     callbackSrv.URL + "/webhook",
			CallbackBodyTemplate: `{}`,
		},
	}
	e.RegisterMock(def)

	addr := "127.0.0.1:18783"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	body, _ := json.Marshal(map[string]any{"query": `mutation PlaceOrder { placeOrder { accepted } }`, "operationName": "PlaceOrder"})
	resp, err := http.Post("http://"+addr+"/graphql", "application/json", bytes.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("expected the immediate 202 ack, got %d", resp.StatusCode)
	}
	respBody, _ := io.ReadAll(resp.Body)
	want := `{"data":{"placeOrder":{"accepted":true}}}`
	if string(respBody) != want {
		t.Fatalf("expected ack body %q, got %q", want, respBody)
	}

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&callbackHits) > 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatal("expected the async callback to have been delivered within the retry window")
}

// TestGraphQLScenarioAdvancesPerSession guards against Scenario silently
// being REST-only for GraphQL too — see TestSoapScenarioAdvancesPerSession.
func TestGraphQLScenarioAdvancesPerSession(t *testing.T) {
	db := newTestMockStore(t)
	e := New()
	e.SetScenarioStepper(db)

	def := &mock.Definition{
		ID: "gql-scenario", ProtocolType: "graphql", Method: http.MethodPost, PathPattern: "/graphql", Enabled: true,
		OperationName: "GetStatus",
		Scenario: &mock.ScenarioConfig{
			Steps: []mock.ResponseTemplate{
				{StatusCode: 200, BodyTemplate: `{"data":{"status":"pending"}}`},
				{StatusCode: 200, BodyTemplate: `{"data":{"status":"shipped"}}`},
			},
			SessionKeyMode:  "header",
			SessionKeyField: "X-Session-Id",
		},
	}
	e.RegisterMock(def)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18784"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	hit := func() string {
		body, _ := json.Marshal(map[string]any{"query": `query GetStatus { status }`, "operationName": "GetStatus"})
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/graphql", bytes.NewReader(body))
		req.Header.Set("X-Session-Id", "s1")
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	if got := hit(); got != `{"data":{"status":"pending"}}` {
		t.Fatalf("expected step 0 (pending), got: %s", got)
	}
	if got := hit(); got != `{"data":{"status":"shipped"}}` {
		t.Fatalf("expected the scenario to have advanced to step 1 (shipped), got: %s", got)
	}
}
