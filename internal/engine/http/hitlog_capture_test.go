package httpengine

import (
	"context"
	"net/http"
	"testing"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
)

func TestInboundRestHitIsRecordedWithRedactedAuthHeader(t *testing.T) {
	logger := &fakeHitLogger{}
	e := New()
	e.SetHitLogger(logger)
	e.RegisterMock(&mock.Definition{
		ID: "h1", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/orders/{id}", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"status":"pending"}`},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18820"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/orders/1", nil)
	req.Header.Set("Authorization", "Bearer super-secret-token")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if len(logger.entries) != 1 {
		t.Fatalf("expected exactly one recorded inbound hit, got %d", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.Direction != hitlog.DirectionInbound {
		t.Fatalf("expected direction=inbound, got %q", entry.Direction)
	}
	if entry.MockID != "h1" || entry.ResponseStatus != 200 {
		t.Fatalf("unexpected entry: %+v", entry)
	}
	if entry.ResponseBody != `{"status":"pending"}` {
		t.Fatalf("expected the actual rendered response body captured, got %q", entry.ResponseBody)
	}
	if entry.RequestHeaders["Authorization"] != "***REDACTED***" {
		t.Fatalf("expected Authorization to be redacted, got %q", entry.RequestHeaders["Authorization"])
	}
}

func TestSetRedactedHeadersOverridesTheDefaultList(t *testing.T) {
	logger := &fakeHitLogger{}
	e := New()
	e.SetHitLogger(logger)
	// A custom list that does NOT include Authorization, but does include a
	// header the hardcoded default never covered.
	e.SetRedactedHeaders([]string{"X-Custom-Secret"})
	e.RegisterMock(&mock.Definition{
		ID: "h3", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/custom", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{}`},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18822"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/custom", nil)
	req.Header.Set("Authorization", "Bearer should-not-be-redacted-now")
	req.Header.Set("X-Custom-Secret", "shh")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	resp.Body.Close()

	if len(logger.entries) != 1 {
		t.Fatalf("expected exactly one recorded inbound hit, got %d", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.RequestHeaders["Authorization"] != "Bearer should-not-be-redacted-now" {
		t.Fatalf("expected Authorization to no longer be redacted once the list was overridden, got %q", entry.RequestHeaders["Authorization"])
	}
	if entry.RequestHeaders["X-Custom-Secret"] != "***REDACTED***" {
		t.Fatalf("expected the custom-configured header to be redacted, got %q", entry.RequestHeaders["X-Custom-Secret"])
	}
}

func TestInboundHitNotRecordedWhenFaultSimulatesTimeout(t *testing.T) {
	logger := &fakeHitLogger{}
	e := New()
	e.SetHitLogger(logger)
	e.RegisterMock(&mock.Definition{
		ID: "h2", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/dead", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `ok`},
		Fault:    &mock.FaultConfig{TimeoutRatePercent: 100},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18821"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	client := &http.Client{}
	//nolint:bodyclose // the request is expected to fail at the transport level; there is no body to close
	if _, err := client.Get("http://" + addr + "/dead"); err == nil {
		t.Fatal("expected the request to fail (dropped connection)")
	}

	if len(logger.entries) != 0 {
		t.Fatalf("expected no recorded entry for a hijacked/timed-out response, got %+v", logger.entries)
	}
}
