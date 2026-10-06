package apiclient

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRunLoadTest_countBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	result := RunLoadTest(context.Background(), RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 4, TotalRequests: 20})
	if result.TotalRequests != 20 {
		t.Fatalf("expected exactly 20 requests, got %d", result.TotalRequests)
	}
	if result.Statuses.Count2xx != 20 {
		t.Fatalf("expected 20 2xx responses, got %+v", result.Statuses)
	}
	if result.Statuses.CountError != 0 {
		t.Fatalf("expected no errors, got %+v", result.Statuses)
	}
	if result.MaxMs < result.MinMs || result.P50Ms < result.MinMs || result.P99Ms < result.P50Ms {
		t.Fatalf("percentiles look inconsistent: %+v", result)
	}
}

func TestRunLoadTest_mixedStatuses(t *testing.T) {
	var n int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n++
		if n%2 == 0 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// Single-worker (no concurrency) so the handler's counter increments
	// deterministically — with concurrent workers the 50/50 split still
	// holds overall but not per-request-index.
	result := RunLoadTest(context.Background(), RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 1, TotalRequests: 10})
	if result.Statuses.Count2xx != 5 || result.Statuses.Count5xx != 5 {
		t.Fatalf("expected an even 2xx/5xx split, got %+v", result.Statuses)
	}
}

func TestRunLoadTest_networkError(t *testing.T) {
	result := RunLoadTest(context.Background(), RequestSpec{Method: "GET", URL: "http://127.0.0.1:1"}, nil, LoadTestConfig{Concurrency: 1, TotalRequests: 3})
	if result.Statuses.CountError != 3 {
		t.Fatalf("expected all 3 requests to fail at the network level, got %+v", result.Statuses)
	}
	if result.MinMs != 0 || result.MaxMs != 0 {
		t.Fatalf("expected no latency samples when every request errored, got %+v", result)
	}
}

func TestRunLoadTest_lightweightSamplesAlwaysCaptured(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Test-Header", "yes")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"hello":"world"}`))
	}))
	defer srv.Close()

	plain := RunLoadTest(context.Background(), RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 2, TotalRequests: 10})
	if plain.Detailed {
		t.Fatal("expected Detailed=false when the config didn't request it")
	}
	if len(plain.Samples) != 10 {
		t.Fatalf("expected lightweight samples captured even without Detailed, got %d", len(plain.Samples))
	}
	for _, s := range plain.Samples {
		if s.ResponseBody != "" || s.ResponseHeaders != nil {
			t.Fatalf("expected no response body/headers without Detailed, got %+v", s)
		}
		if s.StatusCode != 200 {
			t.Fatalf("expected every lightweight sample to still have its status code, got %+v", s)
		}
	}

	const n = 300
	detailed := RunLoadTest(context.Background(), RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 4, TotalRequests: n, Detailed: true})
	if !detailed.Detailed {
		t.Fatal("expected Detailed=true when the config requested it")
	}
	if len(detailed.Samples) != n {
		t.Fatalf("expected every one of %d requests captured, got %d", n, len(detailed.Samples))
	}
	for _, s := range detailed.Samples {
		if s.ResponseBody != `{"hello":"world"}` {
			t.Fatalf("expected the actual response body captured per sample when Detailed, got %q", s.ResponseBody)
		}
		if got := s.ResponseHeaders["X-Test-Header"]; len(got) != 1 || got[0] != "yes" {
			t.Fatalf("expected the actual response headers captured per sample when Detailed, got %+v", s.ResponseHeaders)
		}
	}
}

// TestRunLoadTest_cancelledContextStillReturnsAValidResult guards the
// existing "Stop" button's behavior (aborting the fetch cancels this
// run's context, checked by loadTestLimits.shouldStop between iterations)
// — a run stopped before any iteration completes must still return a
// real, persistable *LoadTestResult (zero requests is a valid outcome,
// nil is not), since internal/web/api's saveLoadTestRun persists whatever
// RunLoadTest returns regardless of why the run ended early.
func TestRunLoadTest_cancelledContextStillReturnsAValidResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // already cancelled before RunLoadTest's first iteration
	result := RunLoadTest(ctx, RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 2, TotalRequests: 50})
	if result == nil {
		t.Fatal("expected a non-nil result even for an immediately-cancelled run")
	}
	if result.TotalRequests != 0 {
		t.Fatalf("expected 0 requests for a run cancelled before it started, got %d", result.TotalRequests)
	}
	if result.Samples != nil {
		t.Fatalf("expected no samples for a run with zero requests, got %d", len(result.Samples))
	}
}

func TestRunLoadTest_elapsedMsIsMonotonicWithIndex(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	// A single worker (Concurrency: 1) issues requests strictly in order,
	// so ElapsedMs must be non-decreasing alongside Index for this run —
	// the x-axis every chart plots samples against.
	result := RunLoadTest(context.Background(), RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 1, TotalRequests: 20})
	if len(result.Samples) != 20 {
		t.Fatalf("expected 20 samples, got %d", len(result.Samples))
	}
	for i := 1; i < len(result.Samples); i++ {
		if result.Samples[i].ElapsedMs < result.Samples[i-1].ElapsedMs {
			t.Fatalf("expected non-decreasing ElapsedMs with a single worker, got %d then %d at index %d",
				result.Samples[i-1].ElapsedMs, result.Samples[i].ElapsedMs, i)
		}
	}
}

func TestClampLoadTestConfig(t *testing.T) {
	cfg := ClampLoadTestConfig(LoadTestConfig{Concurrency: 9999, TotalRequests: 999999, DurationSecs: 9999})
	if cfg.Concurrency != MaxLoadTestConcurrency {
		t.Errorf("concurrency not clamped: %d", cfg.Concurrency)
	}
	if cfg.TotalRequests != MaxLoadTestRequests {
		t.Errorf("totalRequests not clamped: %d", cfg.TotalRequests)
	}
	if cfg.DurationSecs != MaxLoadTestDurationSecs {
		t.Errorf("durationSecs not clamped: %d", cfg.DurationSecs)
	}

	def := ClampLoadTestConfig(LoadTestConfig{})
	if def.Concurrency != 1 || def.TotalRequests != 50 {
		t.Errorf("expected safe defaults, got %+v", def)
	}
}

func TestRunLoadTest_durationBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	start := time.Now()
	result := RunLoadTest(context.Background(), RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 4, DurationSecs: 1})
	elapsed := time.Since(start)
	if elapsed > 2*time.Second {
		t.Fatalf("duration-bounded run took too long: %s", elapsed)
	}
	if result.TotalRequests == 0 {
		t.Fatal("expected at least one request in a 1s duration-bounded run")
	}
}

// TestRunWSLoadTest_countBounded guards against the real gap this closes:
// RunLoadTest only ever called the HTTP Execute path, so load-testing a WS
// mock/endpoint wasn't possible at all. Each successful connect+send is
// counted as Count2xx (there's no HTTP-style status code for a WS
// handshake) and must complete well under the 3s ExchangeWS-style listen
// window, since wsLoadTestHit deliberately skips waiting for a reply.
func TestRunWSLoadTest_countBounded(t *testing.T) {
	srv := echoWSServer(t)
	defer srv.Close()
	wsURL := "ws" + srv.URL[len("http"):]

	start := time.Now()
	result := RunWSLoadTest(context.Background(), WSRequestSpec{URL: wsURL, Message: "ping"}, nil, LoadTestConfig{Concurrency: 4, TotalRequests: 20})
	elapsed := time.Since(start)

	if result.TotalRequests != 20 {
		t.Fatalf("expected exactly 20 exchanges, got %d", result.TotalRequests)
	}
	if result.Statuses.Count2xx != 20 || result.Statuses.CountError != 0 {
		t.Fatalf("expected 20 successful exchanges and no errors, got %+v", result.Statuses)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("expected the run to skip waiting for replies and finish quickly, took %s", elapsed)
	}
}

// TestRunLoadTest_contextCancellation guards the "Stop" button's actual
// mechanism: cancelling ctx (what the admin UI does when the caller aborts
// the HTTP request) must stop the run well short of its configured cap,
// not just get ignored until TotalRequests/DurationSecs is reached anyway.
func TestRunLoadTest_contextCancellation(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(20 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	result := RunLoadTest(ctx, RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 4, TotalRequests: MaxLoadTestRequests})
	elapsed := time.Since(start)

	if elapsed > 1*time.Second {
		t.Fatalf("cancellation didn't stop the run promptly, took %s", elapsed)
	}
	if result.TotalRequests == 0 || result.TotalRequests >= MaxLoadTestRequests {
		t.Fatalf("expected a partial run cut short by cancellation, got %d requests", result.TotalRequests)
	}
}

// TestRunLoadTest_durationModeSamplesCapped guards the real gap this fix
// closes: duration mode leaves TotalRequests at 0 (uncapped — see
// ClampLoadTestConfig), so without record()'s own cap, a long/concurrent
// enough run could append one lightweight sample per request with nothing
// bounding the list, blowing up the DB insert and the HTTP response. A high
// concurrency against a fast local server for just ~1s is enough to exceed
// MaxLoadTestRequests if record() isn't capping — this keeps the test's own
// runtime short while still exercising the cap.
func TestRunLoadTest_durationModeSamplesCapped(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	result := RunLoadTest(context.Background(), RequestSpec{Method: "GET", URL: srv.URL}, nil, LoadTestConfig{Concurrency: 20, DurationSecs: 1})
	if len(result.Samples) > MaxLoadTestRequests {
		t.Fatalf("expected samples capped at %d, got %d", MaxLoadTestRequests, len(result.Samples))
	}
	// The aggregates/status counts must still reflect the FULL run, not just
	// the capped sample list — record() keeps updating acc.latencies/
	// acc.statuses for every request regardless of the sample cap.
	if result.TotalRequests == 0 {
		t.Fatal("expected at least one request in a 1s duration-bounded run")
	}
	if result.Statuses.Count2xx != result.TotalRequests {
		t.Fatalf("expected every request counted as 2xx regardless of the sample cap, got %+v (total %d)", result.Statuses, result.TotalRequests)
	}
}

func TestRunWSLoadTest_networkError(t *testing.T) {
	result := RunWSLoadTest(context.Background(), WSRequestSpec{URL: "ws://127.0.0.1:1"}, nil, LoadTestConfig{Concurrency: 1, TotalRequests: 3})
	if result.Statuses.CountError != 3 {
		t.Fatalf("expected all 3 exchanges to fail at the dial level, got %+v", result.Statuses)
	}
}
