package httpengine

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"testing"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/validate"
)

func startGraphQLArgsMock(t *testing.T) string {
	t.Helper()
	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "gq-args", ProtocolType: "graphql", Method: http.MethodPost, PathPattern: "/graphql", Enabled: true,
		OperationName: "user",
		Validation:    []validate.Rule{{Field: "body.variables.id", Required: true}},
		Response: mock.ResponseTemplate{StatusCode: 200,
			BodyTemplate: `{"data":{"user":{"id":"{{ .Request.Body.variables.id }}","active":"{{ .Request.Body.variables.active }}"}}}`},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addr := freeAddr(t)
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return "http://" + addr + "/graphql"
}

func gqlPost(t *testing.T, url string, payload map[string]any) string {
	t.Helper()
	b, _ := json.Marshal(payload)
	resp, err := http.Post(url, "application/json", bytes.NewReader(b))
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return string(out)
}

// `user(id: "1")` used to fail validation with "body.variables.id required
// field is missing" unless the id was also sent in a variables object.
func TestGraphQLInlineArgumentsAreVisibleAsVariables(t *testing.T) {
	url := startGraphQLArgsMock(t)

	got := gqlPost(t, url, map[string]any{"query": `{ user(id: "1", active: true) { id } }`})
	want := `{"data":{"user":{"id":"1","active":"true"}}}`
	if got != want {
		t.Fatalf("inline arguments should satisfy body.variables.* rules and templates:\n got %s\nwant %s", got, want)
	}
}

func TestGraphQLVariableReferencesAndExplicitVariablesStillWin(t *testing.T) {
	url := startGraphQLArgsMock(t)

	// A variable reference resolves through the variables object.
	got := gqlPost(t, url, map[string]any{"query": `query($uid: ID!) { user(id: $uid) { id } }`, "variables": map[string]any{"uid": "7"}})
	if want := `{"data":{"user":{"id":"7","active":"<no value>"}}}`; got != want {
		t.Fatalf("variable reference: got %s want %s", got, want)
	}
	// An explicitly sent variable of the same name is not overridden by the inline value.
	got = gqlPost(t, url, map[string]any{"query": `{ user(id: "inline") { id } }`, "variables": map[string]any{"id": "explicit"}})
	if want := `{"data":{"user":{"id":"explicit","active":"<no value>"}}}`; got != want {
		t.Fatalf("explicit variables must win: got %s want %s", got, want)
	}
}

func TestGraphQLMissingRequiredArgumentGivesAHelpfulMessage(t *testing.T) {
	url := startGraphQLArgsMock(t)
	got := gqlPost(t, url, map[string]any{"query": `{ user { id } }`})
	if !bytes.Contains([]byte(got), []byte("variables")) || !bytes.Contains([]byte(got), []byte("inline argument")) {
		t.Fatalf("expected a hint about variables or an inline argument, got %s", got)
	}
}
