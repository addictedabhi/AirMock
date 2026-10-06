package httpengine

import (
	"bytes"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
)

// proxyTargetURL joins the upstream base and the wildcard remainder without
// inventing a trailing slash: with nothing after the mock's path prefix the
// upstream gets exactly its base URL, and a trailing slash is kept only if
// the client's own request path had one.
func proxyTargetURL(base, wildcard, requestPath string) string {
	base = strings.TrimRight(base, "/")
	switch {
	case wildcard != "":
		return base + "/" + wildcard
	case strings.HasSuffix(requestPath, "/"):
		return base + "/"
	default:
		return base
	}
}

// serveProxy forwards the request to def.Proxy.TargetBaseURL, returns the
// real response unchanged, and (if a HitLogger is configured) captures the
// exchange tagged proxy-capture — "point this at the real API for a bit"
// so real traffic can later be promoted into a hand-authored mock via the
// admin API's promote-to-mock action.
func (e *Engine) serveProxy(w http.ResponseWriter, r *http.Request, bodyBytes []byte, def *mock.Definition) {
	wildcard := chi.URLParam(r, "*")
	targetURL := proxyTargetURL(def.Proxy.TargetBaseURL, wildcard, r.URL.Path)
	if r.URL.RawQuery != "" {
		targetURL += "?" + r.URL.RawQuery
	}

	proxyReq, err := http.NewRequest(r.Method, targetURL, bytes.NewReader(bodyBytes))
	if err != nil {
		http.Error(w, "build proxy request: "+err.Error(), http.StatusInternalServerError)
		return
	}
	for k, vv := range r.Header {
		for _, v := range vv {
			proxyReq.Header.Add(k, v)
		}
	}

	start := time.Now()
	resp, err := http.DefaultClient.Do(proxyReq)
	if err != nil {
		http.Error(w, "proxy upstream error: "+err.Error(), http.StatusBadGateway)
		return
	}
	defer resp.Body.Close()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 5<<20))
	latency := time.Since(start)

	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)
	w.Write(respBody)

	if e.hitLogger == nil {
		return
	}
	err = e.hitLogger.Record(&hitlog.Entry{
		MockID:          def.ID,
		ProtocolType:    "proxy-capture",
		Direction:       hitlog.DirectionProxyCapture,
		Method:          r.Method,
		Path:            r.URL.Path,
		TargetURL:       targetURL,
		RequestHeaders:  e.redactedHeaders(r.Header),
		RequestBody:     string(bodyBytes),
		ResponseStatus:  resp.StatusCode,
		ResponseHeaders: e.redactedHeaders(resp.Header),
		ResponseBody:    string(respBody),
		LatencyMs:       latency.Milliseconds(),
	})
	if err != nil {
		log.Printf("airmock: failed to record proxy capture for mock %q: %v", def.ID, err)
	}
}
