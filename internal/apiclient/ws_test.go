package apiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"nhooyr.io/websocket"
)

func echoWSServer(t *testing.T) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close(websocket.StatusNormalClosure, "")

		ctx, cancel := context.WithTimeout(r.Context(), 5*time.Second)
		defer cancel()
		typ, data, err := conn.Read(ctx)
		if err != nil {
			return
		}
		conn.Write(ctx, typ, append([]byte("echo:"), data...))
	}))
}

func TestExchangeWSSendsAndReceivesEcho(t *testing.T) {
	srv := echoWSServer(t)
	defer srv.Close()

	wsURL := "ws" + srv.URL[len("http"):]
	result := ExchangeWS(WSRequestSpec{URL: wsURL, Message: "hello {{name}}"}, map[string]string{"name": "world"}, 2*time.Second)

	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if !result.Connected || !result.Sent {
		t.Fatalf("expected Connected and Sent, got %+v", result)
	}
	if len(result.Messages) != 1 || result.Messages[0] != "echo:hello world" {
		t.Fatalf("expected one echoed message, got %+v", result.Messages)
	}
}

// TestExchangeWSWithoutSchemeDefaultsToWS is a regression test for the same
// class of bug as Execute's: "localhost:PORT/path" with no scheme at all
// must still work, defaulting to ws:// rather than failing outright.
func TestExchangeWSWithoutSchemeDefaultsToWS(t *testing.T) {
	srv := echoWSServer(t)
	defer srv.Close()

	schemeless := srv.URL[len("http://"):]
	result := ExchangeWS(WSRequestSpec{URL: schemeless, Message: "hi"}, nil, 2*time.Second)
	if result.Error != "" {
		t.Fatalf("unexpected error: %s", result.Error)
	}
	if !result.Connected {
		t.Fatalf("expected Connected, got %+v", result)
	}
}

func TestExchangeWSReportsDialErrorForBadURL(t *testing.T) {
	result := ExchangeWS(WSRequestSpec{URL: "ws://127.0.0.1:1"}, nil, 500*time.Millisecond)
	if result.Error == "" {
		t.Fatal("expected a dial error for an unreachable address")
	}
	if result.Connected {
		t.Fatal("expected Connected=false on dial failure")
	}
}
