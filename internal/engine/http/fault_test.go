package httpengine

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
)

// TestFaultErrorRateIsWithinToleranceOverManyRequests is the plan's
// statistical verify step: fire N requests at a mock configured with a
// known ErrorRatePercent and assert the observed error rate lands within a
// generous tolerance band — random by nature, so the band is wide enough
// to avoid flaking while still catching a badly broken RollFault.
func TestFaultErrorRateIsWithinToleranceOverManyRequests(t *testing.T) {
	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "f1", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/flaky", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `ok`},
		Fault:    &mock.FaultConfig{ErrorRatePercent: 30, ErrorStatusCodes: []int{500, 503}},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18800"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	const n = 500
	errors := 0
	for i := 0; i < n; i++ {
		resp, err := http.Get("http://" + addr + "/flaky")
		if err != nil {
			t.Fatalf("GET #%d: %v", i, err)
		}
		if resp.StatusCode == 500 || resp.StatusCode == 503 {
			errors++
		} else if resp.StatusCode != 200 {
			t.Fatalf("unexpected status %d", resp.StatusCode)
		}
		resp.Body.Close()
	}

	rate := float64(errors) / float64(n) * 100
	if rate < 15 || rate > 45 {
		t.Fatalf("expected observed error rate near 30%% (tolerance 15-45%%) over %d requests, got %.1f%% (%d errors)", n, rate, errors)
	}
}

// TestFaultTimeoutDropsTheConnection is the qualitative counterpart: a
// TimeoutRatePercent of 100 must make every request fail at the transport
// level (connection reset/EOF), never return a normal HTTP response.
func TestFaultTimeoutDropsTheConnection(t *testing.T) {
	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "f2", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/dead", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `ok`},
		Fault:    &mock.FaultConfig{TimeoutRatePercent: 100},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18801"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Get("http://" + addr + "/dead")
	if err == nil {
		resp.Body.Close()
		t.Fatal("expected the request to fail (dropped connection), got a normal response")
	}
}
