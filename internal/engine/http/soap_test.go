package httpengine

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
)

func TestSoapDispatchesBySOAPActionHeader(t *testing.T) {
	e := New()
	getOrder := &mock.Definition{
		ID: "s1", ProtocolType: "soap", Method: "POST", PathPattern: "/soap/orders", Enabled: true,
		SOAPAction: "urn:orders#GetOrder", OperationName: "GetOrder",
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: soapEnvelope("GetOrderResponse", "<status>pending</status>")},
	}
	createOrder := &mock.Definition{
		ID: "s2", ProtocolType: "soap", Method: "POST", PathPattern: "/soap/orders", Enabled: true,
		SOAPAction: "urn:orders#CreateOrder", OperationName: "CreateOrder",
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: soapEnvelope("CreateOrderResponse", "<id>new-1</id>")},
	}
	e.RegisterMock(getOrder)
	e.RegisterMock(createOrder)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18770"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	body := soapEnvelope("GetOrder", "<id>42</id>")
	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/soap/orders", strings.NewReader(body))
	req.Header.Set("SOAPAction", `"urn:orders#GetOrder"`)
	req.Header.Set("Content-Type", "text/xml")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/xml") {
		t.Fatalf("expected text/xml content type, got %q", ct)
	}
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "<status>pending</status>") {
		t.Fatalf("expected the GetOrder response envelope, got: %s", respBody)
	}
}

func TestSoapDispatchesByOperationNameWhenHeaderAbsent(t *testing.T) {
	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "s3", ProtocolType: "soap", Method: "POST", PathPattern: "/soap/orders2", Enabled: true,
		OperationName: "Ping",
		Response:      mock.ResponseTemplate{StatusCode: 200, BodyTemplate: soapEnvelope("PingResponse", "<ok>true</ok>")},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18771"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	body := soapEnvelope("Ping", "")
	resp, err := http.Post("http://"+addr+"/soap/orders2", "text/xml", strings.NewReader(body))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	respBody, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(respBody), "<ok>true</ok>") {
		t.Fatalf("expected the Ping response envelope, got: %s", respBody)
	}
}

func TestSoapUnmatchedOperationReturnsFault(t *testing.T) {
	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "s4", ProtocolType: "soap", Method: "POST", PathPattern: "/soap/orders3", Enabled: true,
		SOAPAction: "urn:a#OpA", OperationName: "OpA",
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: soapEnvelope("OpAResponse", "")},
	})
	e.RegisterMock(&mock.Definition{
		ID: "s5", ProtocolType: "soap", Method: "POST", PathPattern: "/soap/orders3", Enabled: true,
		SOAPAction: "urn:a#OpB", OperationName: "OpB",
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: soapEnvelope("OpBResponse", "")},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18772"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/soap/orders3", strings.NewReader(soapEnvelope("Unknown", "")))
	req.Header.Set("SOAPAction", `"urn:a#Unknown"`)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 500 {
		t.Fatalf("expected a 500 SOAP fault for an unmatched operation, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "soap:Fault") {
		t.Fatalf("expected a SOAP fault envelope, got: %s", body)
	}
}

// TestSoapAsyncModeAcksThenDeliversCallback guards against a real bug: SOAP
// (and GraphQL) mocks silently ignored Mode=="async" entirely — only REST's
// matcher ever checked it — so a SOAP async mock just answered synchronously
// forever with no ack/callback and no error telling anyone why.
func TestSoapAsyncModeAcksThenDeliversCallback(t *testing.T) {
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
		ID: "soap-async", ProtocolType: "soap", Method: "POST", PathPattern: "/soap/async", Enabled: true, Mode: "async",
		OperationName: "PlaceOrder",
		AsyncConfig: &mock.AsyncConfig{
			AckResponse:          mock.ResponseTemplate{StatusCode: 202, BodyTemplate: soapEnvelope("PlaceOrderAck", "<accepted>true</accepted>")},
			CallbackTargetMode:   "fixed",
			CallbackFixedURL:     callbackSrv.URL + "/webhook",
			CallbackBodyTemplate: `{}`,
		},
	}
	e.RegisterMock(def)

	addr := "127.0.0.1:18773"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	resp, err := http.Post("http://"+addr+"/soap/async", "text/xml", strings.NewReader(soapEnvelope("PlaceOrder", "")))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 202 {
		t.Fatalf("expected the immediate 202 ack, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.Contains(ct, "text/xml") {
		t.Fatalf("expected the SOAP ack to default to text/xml, got %q", ct)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "<accepted>true</accepted>") {
		t.Fatalf("expected the ack envelope, got: %s", body)
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

// TestSoapScenarioAdvancesPerSession guards against Scenario silently being
// REST-only — a SOAP mock's Scenario field used to just never be checked,
// so it always fell through to the plain default response forever.
func TestSoapScenarioAdvancesPerSession(t *testing.T) {
	db := newTestMockStore(t)
	e := New()
	e.SetScenarioStepper(db)

	def := &mock.Definition{
		ID: "soap-scenario", ProtocolType: "soap", Method: "POST", PathPattern: "/soap/scenario", Enabled: true,
		OperationName: "GetStatus",
		Scenario: &mock.ScenarioConfig{
			Steps: []mock.ResponseTemplate{
				{StatusCode: 200, BodyTemplate: soapEnvelope("StatusResponse", "<status>pending</status>")},
				{StatusCode: 200, BodyTemplate: soapEnvelope("StatusResponse", "<status>shipped</status>")},
			},
			SessionKeyMode:  "header",
			SessionKeyField: "X-Session-Id",
		},
	}
	e.RegisterMock(def)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18774"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	hit := func() string {
		req, _ := http.NewRequest(http.MethodPost, "http://"+addr+"/soap/scenario", strings.NewReader(soapEnvelope("GetStatus", "")))
		req.Header.Set("X-Session-Id", "s1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("POST: %v", err)
		}
		defer resp.Body.Close()
		b, _ := io.ReadAll(resp.Body)
		return string(b)
	}

	if got := hit(); !strings.Contains(got, "<status>pending</status>") {
		t.Fatalf("expected step 0 (pending), got: %s", got)
	}
	if got := hit(); !strings.Contains(got, "<status>shipped</status>") {
		t.Fatalf("expected the scenario to have advanced to step 1 (shipped), got: %s", got)
	}
}

func soapEnvelope(rootElement, innerXML string) string {
	return `<?xml version="1.0"?><soap:Envelope xmlns:soap="http://schemas.xmlsoap.org/soap/envelope/"><soap:Body><` + rootElement + `>` + innerXML + `</` + rootElement + `></soap:Body></soap:Envelope>`
}
