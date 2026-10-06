package apiclient

import (
	"context"
	"net/http"
	"time"

	"nhooyr.io/websocket"
)

// WSExchangeResult is what the "Test/Send" action in the UI shows: whether
// the initial message was sent, and whatever the server sent back within
// the listen window.
type WSExchangeResult struct {
	Connected bool     `json:"connected"`
	Sent      bool     `json:"sent"`
	Messages  []string `json:"messages,omitempty"`
	Error     string   `json:"error,omitempty"`
}

// ExchangeWS connects, optionally sends spec.Message, collects whatever
// arrives within listenFor, then disconnects. This is a practical v1 for
// "test a WS endpoint from the API client" — a connect/send/observe cycle
// — rather than a fully live, bidirectional session relayed through the
// browser indefinitely.
func ExchangeWS(spec WSRequestSpec, vars map[string]string, listenFor time.Duration) *WSExchangeResult {
	if listenFor <= 0 {
		listenFor = 3 * time.Second
	}

	conn, err := dialWS(spec, vars)
	if err != nil {
		return &WSExchangeResult{Error: err.Error()}
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	result := &WSExchangeResult{Connected: true}

	if spec.Message != "" {
		msg := SubstituteVars(spec.Message, vars)
		writeCtx, writeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err := conn.Write(writeCtx, websocket.MessageText, []byte(msg))
		writeCancel()
		if err != nil {
			result.Error = err.Error()
			return result
		}
		result.Sent = true
	}

	readCtx, readCancel := context.WithTimeout(context.Background(), listenFor)
	defer readCancel()
	for {
		_, data, err := conn.Read(readCtx)
		if err != nil {
			break // listen window elapsed or the server closed — either way, we're done collecting
		}
		result.Messages = append(result.Messages, string(data))
	}
	return result
}

// dialWS opens the WebSocket connection described by spec, substituting
// vars into the URL and headers — the connection setup shared by
// ExchangeWS's full connect/send/observe cycle and wsLoadTestHit's
// connect/send-only probe.
func dialWS(spec WSRequestSpec, vars map[string]string) (*websocket.Conn, error) {
	dialCtx, dialCancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer dialCancel()

	url := EnsureScheme(SubstituteVars(spec.URL, vars), "ws://")
	var opts *websocket.DialOptions
	if len(spec.Headers) > 0 {
		h := http.Header{}
		for _, kv := range spec.Headers {
			if !kv.Disabled {
				h.Set(SubstituteVars(kv.Key, vars), SubstituteVars(kv.Value, vars))
			}
		}
		opts = &websocket.DialOptions{HTTPHeader: h}
	}

	conn, _, err := websocket.Dial(dialCtx, url, opts)
	return conn, err
}

// wsLoadTestHit measures one connect(+send) round trip for RunWSLoadTest:
// dial, optionally write spec.Message, then close immediately — it
// deliberately doesn't wait through a listen window the way ExchangeWS
// does, since a load test needs each worker free to fire the next
// connection rather than blocked observing replies.
func wsLoadTestHit(spec WSRequestSpec, vars map[string]string) (int64, error) {
	start := time.Now()
	conn, err := dialWS(spec, vars)
	if err != nil {
		return time.Since(start).Milliseconds(), err
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	if spec.Message != "" {
		msg := SubstituteVars(spec.Message, vars)
		writeCtx, writeCancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = conn.Write(writeCtx, websocket.MessageText, []byte(msg))
		writeCancel()
		if err != nil {
			return time.Since(start).Milliseconds(), err
		}
	}
	return time.Since(start).Milliseconds(), nil
}
