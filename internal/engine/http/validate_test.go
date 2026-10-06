package httpengine

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/validate"
)

func TestValidationRejectsMissingRequiredField(t *testing.T) {
	e := New()
	def := &mock.Definition{
		ID: "v1", ProtocolType: "rest", Method: http.MethodPost, PathPattern: "/orders", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"ok":true}`},
		Validation: []validate.Rule{
			{Field: "body.orderId", Required: true, Type: "string"},
		},
	}
	e.RegisterMock(def)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18750"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	// Missing orderId -> configured 400 with the validation error list.
	resp, err := http.Post("http://"+addr+"/orders", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("expected 400 for missing required field, got %d", resp.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode error body: %v", err)
	}
	errsRaw, ok := body["errors"].([]any)
	if !ok || len(errsRaw) != 1 {
		t.Fatalf("expected exactly one validation error, got %+v", body)
	}

	// Present orderId -> passes validation, real response returned.
	resp2, err := http.Post("http://"+addr+"/orders", "application/json", strings.NewReader(`{"orderId":"o-1"}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp2.Body.Close()
	if resp2.StatusCode != http.StatusOK {
		t.Fatalf("expected 200 when validation passes, got %d", resp2.StatusCode)
	}
	respBody, _ := io.ReadAll(resp2.Body)
	if string(respBody) != `{"ok":true}` {
		t.Fatalf("unexpected response body: %s", respBody)
	}
}

func TestValidationErrorResponseOverride(t *testing.T) {
	e := New()
	def := &mock.Definition{
		ID: "v2", ProtocolType: "rest", Method: http.MethodPost, PathPattern: "/orders", Enabled: true,
		Response:   mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"ok":true}`},
		Validation: []validate.Rule{{Field: "body.orderId", Required: true}},
		ValidationErrorResponse: &mock.ResponseTemplate{
			StatusCode:   422,
			BodyTemplate: `{"custom":"missing orderId"}`,
		},
	}
	e.RegisterMock(def)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18751"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	resp, err := http.Post("http://"+addr+"/orders", "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 422 {
		t.Fatalf("expected the overridden 422 status, got %d", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if string(body) != `{"custom":"missing orderId"}` {
		t.Fatalf("expected the overridden error body, got %s", body)
	}
}
