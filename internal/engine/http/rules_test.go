package httpengine

import (
	"io"
	"net/http"
	"testing"

	"context"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
)

func TestResponseRulesBranchByRequestContentWithDefaultFallback(t *testing.T) {
	e := New()
	def := &mock.Definition{
		ID: "r1", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/quote", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"tier":"default"}`},
		ResponseRules: []mock.ResponseRule{
			{
				Conditions: []mock.Condition{{Field: "query.type", Operator: "equals", Value: "premium"}},
				Response:   mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"tier":"premium"}`},
			},
			{
				Conditions: []mock.Condition{{Field: "query.type", Operator: "equals", Value: "basic"}},
				Response:   mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{"tier":"basic"}`},
			},
		},
	}
	e.RegisterMock(def)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	addr := "127.0.0.1:18760"
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}

	cases := []struct {
		query string
		want  string
	}{
		{"?type=premium", `{"tier":"premium"}`},
		{"?type=basic", `{"tier":"basic"}`},
		{"?type=unknown", `{"tier":"default"}`}, // no rule matches -> falls back to Response
	}
	for _, tc := range cases {
		resp, err := http.Get("http://" + addr + "/quote" + tc.query)
		if err != nil {
			t.Fatalf("GET %s: %v", tc.query, err)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if string(body) != tc.want {
			t.Errorf("query %q: expected body %q, got %q", tc.query, tc.want, body)
		}
	}
}
