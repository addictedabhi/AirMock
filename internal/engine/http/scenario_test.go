package httpengine

import (
	"context"
	"io"
	"net/http"
	"path/filepath"
	"testing"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
)

func TestScenarioAdvancesPerSessionAndResetsForADifferentSession(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	store := mock.NewStore(db)

	e := New()
	e.SetScenarioStepper(store)

	def := &mock.Definition{
		ID: "scn1", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/orders/1", Enabled: true,
		Scenario: &mock.ScenarioConfig{
			Steps: []mock.ResponseTemplate{
				{StatusCode: 200, BodyTemplate: `{"status":"pending"}`},
				{StatusCode: 200, BodyTemplate: `{"status":"shipped"}`},
				{StatusCode: 200, BodyTemplate: `{"status":"delivered"}`},
			},
			SessionKeyMode:  "header",
			SessionKeyField: "X-Session-Id",
		},
	}
	e.RegisterMock(def)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18790"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	hit := func(sessionID string) string {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/orders/1", nil)
		req.Header.Set("X-Session-Id", sessionID)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	want := []string{`{"status":"pending"}`, `{"status":"shipped"}`, `{"status":"delivered"}`}
	for i, w := range want {
		if got := hit("session-A"); got != w {
			t.Fatalf("session-A hit #%d: expected %q, got %q", i+1, w, got)
		}
	}

	// A 4th hit from the same session clamps at the last step (Loop=false).
	if got := hit("session-A"); got != `{"status":"delivered"}` {
		t.Fatalf("session-A hit #4: expected clamped last step, got %q", got)
	}

	// A different session starts over from step 0, independent of session-A.
	if got := hit("session-B"); got != `{"status":"pending"}` {
		t.Fatalf("session-B hit #1: expected reset to step 0, got %q", got)
	}
}

func TestScenarioLoopsBackToStartWhenConfigured(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	defer db.Close()
	store := mock.NewStore(db)

	e := New()
	e.SetScenarioStepper(store)

	def := &mock.Definition{
		ID: "scn2", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/loop", Enabled: true,
		Scenario: &mock.ScenarioConfig{
			Steps: []mock.ResponseTemplate{
				{StatusCode: 200, BodyTemplate: `1`},
				{StatusCode: 200, BodyTemplate: `2`},
			},
			SessionKeyMode:  "header",
			SessionKeyField: "X-Session-Id",
			Loop:            true,
		},
	}
	e.RegisterMock(def)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18791"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	hit := func() string {
		req, _ := http.NewRequest(http.MethodGet, "http://"+addr+"/loop", nil)
		req.Header.Set("X-Session-Id", "s1")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("GET: %v", err)
		}
		defer resp.Body.Close()
		body, _ := io.ReadAll(resp.Body)
		return string(body)
	}

	want := []string{"1", "2", "1", "2"}
	for i, w := range want {
		if got := hit(); got != w {
			t.Fatalf("hit #%d: expected %q, got %q", i+1, w, got)
		}
	}
}
