package apiclient

import (
	"context"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// Load-test safety caps: this endpoint blocks on an HTTP request for the
// whole run and can hammer an arbitrary target (this instance's own mocks,
// or a real external endpoint), so every knob is clamped rather than
// trusted — a caller can't accidentally turn "quick load test" into an
// unbounded stress tool or a request that never returns.
const (
	MaxLoadTestConcurrency  = 50
	MaxLoadTestRequests     = 2000
	MaxLoadTestDurationSecs = 60
)

// LoadTestConfig controls one run: Concurrency workers loop calling Execute
// until either TotalRequests have been issued (across all workers) or
// DurationSecs has elapsed, whichever is reached first — set one or both;
// if neither is positive, a default of 50 requests governs so a caller
// can't accidentally launch an unbounded run.
type LoadTestConfig struct {
	Concurrency   int
	TotalRequests int
	DurationSecs  int
	// Detailed, when true, additionally captures every individual request's
	// full result (latency/status/error/response body+headers), not just
	// the aggregate percentiles/counts — off by default since most runs
	// only need the summary, and per-request capture is extra memory for
	// no benefit until someone actually wants to inspect individual hits.
	// Bounded only by TotalRequests/MaxLoadTestRequests itself — the run's
	// own existing safety cap — not by any separate, smaller sample limit.
	Detailed bool
}

type StatusCounts struct {
	Count2xx int `json:"count2xx"`
	Count3xx int `json:"count3xx"`
	Count4xx int `json:"count4xx"`
	Count5xx int `json:"count5xx"`
	// CountError is a request that never got a response at all (DNS/connect/
	// timeout failure) — apiclient.Execute reports these via ExecutionResult.Error.
	CountError int `json:"countError"`
}

type LoadTestResult struct {
	TotalRequests  int          `json:"totalRequests"`
	Statuses       StatusCounts `json:"statuses"`
	MinMs          int64        `json:"minMs"`
	MaxMs          int64        `json:"maxMs"`
	AvgMs          int64        `json:"avgMs"`
	P50Ms          int64        `json:"p50Ms"`
	P90Ms          int64        `json:"p90Ms"`
	P95Ms          int64        `json:"p95Ms"`
	P99Ms          int64        `json:"p99Ms"`
	DurationMs     int64        `json:"durationMs"`
	RequestsPerSec float64      `json:"requestsPerSec"`
	// Request describes what every iteration actually sent — the same
	// spec is repeated verbatim on every hit, so this is captured once here
	// rather than duplicated onto every sample below.
	Request *LoadTestRequestInfo `json:"request,omitempty"`
	// Samples is always populated (one entry per request actually issued,
	// in issue order, capped at MaxLoadTestRequests) — see Detailed below
	// for what it does and doesn't additionally carry.
	Samples []LoadTestSample `json:"samples,omitempty"`
	// Detailed echoes whether this run's config requested full response
	// body/header capture — false still means every Samples entry has a
	// real latency/status/elapsedMs (lightweight capture is unconditional),
	// just no ResponseBody/ResponseHeaders. The frontend uses this to gate
	// its "inspect full response" table without needing to probe samples
	// for which fields happen to be populated.
	Detailed bool `json:"detailed"`
	// RunID is '' until this result has been persisted (see
	// apiclient.Store.SaveLoadTestRun, which sets it in place right after
	// generating the run's ID) — the frontend uses it to build a download
	// link, and since it's set before the result is either returned to the
	// caller or marshaled into the persisted row, a live result and the
	// same run reloaded later from history always agree on it.
	RunID string `json:"runId,omitempty"`
}

// LoadTestRequestInfo is the one repeated request/exchange a load test
// fires — captured once per run rather than per sample, since it never
// varies between iterations.
type LoadTestRequestInfo struct {
	Method string `json:"method,omitempty"`
	URL    string `json:"url,omitempty"`
}

// LoadTestSample is one individual request's full result, captured only
// when a run's Detailed flag is set. ResponseBody/ResponseHeaders are only
// populated for the HTTP load tester (RunLoadTest) — the WS load tester
// (RunWSLoadTest) deliberately disconnects right after send without
// listening for a reply (see RunWSLoadTest's own comment), so there is no
// response to capture there.
type LoadTestSample struct {
	Index           int                 `json:"index"`
	ElapsedMs       int64               `json:"elapsedMs"`
	LatencyMs       int64               `json:"latencyMs"`
	StatusCode      int                 `json:"statusCode"`
	Error           string              `json:"error,omitempty"`
	ResponseBody    string              `json:"responseBody,omitempty"`
	ResponseHeaders map[string][]string `json:"responseHeaders,omitempty"`
}

// ClampLoadTestConfig applies the safety caps and fills in the "neither
// limit set" default, returning a config guaranteed safe to run as-is.
// Exported so internal/web/api can persist the EXACT config a run actually
// used (see apiclient.LoadTestRun.Config), not just whatever a caller sent.
func ClampLoadTestConfig(cfg LoadTestConfig) LoadTestConfig {
	if cfg.Concurrency <= 0 {
		cfg.Concurrency = 1
	}
	if cfg.Concurrency > MaxLoadTestConcurrency {
		cfg.Concurrency = MaxLoadTestConcurrency
	}
	if cfg.TotalRequests <= 0 && cfg.DurationSecs <= 0 {
		cfg.TotalRequests = 50
	}
	if cfg.TotalRequests > MaxLoadTestRequests {
		cfg.TotalRequests = MaxLoadTestRequests
	}
	if cfg.DurationSecs > MaxLoadTestDurationSecs {
		cfg.DurationSecs = MaxLoadTestDurationSecs
	}
	return cfg
}

// loadTestHit is one iteration's full outcome — hit closures (below) build
// one of these per call so runLoadTestWorker/record have everything
// Detailed capture needs without a long positional-return signature.
type loadTestHit struct {
	latencyMs       int64
	statusCode      int
	errMsg          string
	responseBody    string
	responseHeaders map[string][]string
}

// RunLoadTest fires spec repeatedly through Execute across cfg.Concurrency
// concurrent workers, governed by whichever of cfg.TotalRequests/
// cfg.DurationSecs is reached first, and returns aggregate latency
// percentiles and a status-code breakdown — a lightweight, built-in
// equivalent of pointing vegeta/k6 at a mock or a real endpoint, reusing the
// exact same request-building/auth/templating Execute already does for a
// single manual send.
func RunLoadTest(ctx context.Context, spec RequestSpec, vars map[string]string, cfg LoadTestConfig) *LoadTestResult {
	result := runLoadTest(ctx, cfg, func() loadTestHit {
		res := ExecuteContext(ctx, spec, vars)
		if res.Error != "" {
			return loadTestHit{errMsg: res.Error}
		}
		return loadTestHit{latencyMs: res.Timing.TotalMs, statusCode: res.StatusCode, responseBody: res.Body, responseHeaders: res.Headers}
	})
	result.Request = &LoadTestRequestInfo{Method: spec.Method, URL: spec.URL}
	return result
}

// RunWSLoadTest is RunLoadTest's WebSocket counterpart: each iteration
// dials spec, optionally sends its message, and disconnects immediately
// (via wsLoadTestHit) rather than sitting through a listen window like the
// single-shot "Test/Send" action does — a load test needs to measure
// connect+send throughput across many concurrent connections, not spend
// most of each worker's time blocked waiting for replies that a percentile
// summary has no natural place for anyway. There's no HTTP-style status
// code for a successful WS handshake+send, so success is reported as
// Count2xx (successful) vs CountError (dial/write failure) — the same
// two-bucket shape RunLoadTest already falls back to for network errors.
func RunWSLoadTest(ctx context.Context, spec WSRequestSpec, vars map[string]string, cfg LoadTestConfig) *LoadTestResult {
	result := runLoadTest(ctx, cfg, func() loadTestHit {
		ms, err := wsLoadTestHit(spec, vars)
		if err != nil {
			return loadTestHit{errMsg: err.Error()}
		}
		return loadTestHit{latencyMs: ms, statusCode: 200}
	})
	result.Request = &LoadTestRequestInfo{Method: "WS", URL: spec.URL}
	return result
}

// runLoadTest is the shared worker pool driving both RunLoadTest and
// RunWSLoadTest: hit fires one request/exchange and reports its full
// outcome.
func runLoadTest(ctx context.Context, cfg LoadTestConfig, hit func() loadTestHit) *LoadTestResult {
	cfg = ClampLoadTestConfig(cfg)

	var deadline time.Time
	if cfg.DurationSecs > 0 {
		deadline = time.Now().Add(time.Duration(cfg.DurationSecs) * time.Second)
	}
	totalCap := int64(cfg.TotalRequests) // 0 means "uncapped — DurationSecs alone governs"

	// Shared by every worker goroutine below — must stay a pointer so
	// shouldStop's atomic increment of issued (and ctx/deadline checks)
	// applies against ONE counter, not a separate copy per worker.
	limits := &loadTestLimits{ctx: ctx, deadline: deadline, totalCap: totalCap}
	start := time.Now()
	acc := &loadTestAccumulator{detailed: cfg.Detailed, start: start}

	var wg sync.WaitGroup
	for i := 0; i < cfg.Concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			runLoadTestWorker(limits, hit, acc)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)

	result := summarizeLoadTest(acc, elapsed)
	result.Detailed = cfg.Detailed
	return result
}

// loadTestLimits is every condition that stops a worker from starting
// another iteration — cancellation, deadline, and the shared request-count
// cap all read here rather than as separate parameters threaded everywhere.
type loadTestLimits struct {
	ctx      context.Context
	deadline time.Time
	totalCap int64 // 0 means "uncapped — deadline alone governs"
	issued   int64
}

func (l *loadTestLimits) shouldStop() bool {
	if l.ctx.Err() != nil {
		return true
	}
	if !l.deadline.IsZero() && time.Now().After(l.deadline) {
		return true
	}
	return l.totalCap > 0 && atomic.AddInt64(&l.issued, 1) > l.totalCap
}

// loadTestAccumulator is the one statuses/latencies/samples set every
// worker records into, guarded by mu since all workers share it. samples is
// only appended to when detailed is set.
type loadTestAccumulator struct {
	mu        sync.Mutex
	latencies []int64
	statuses  StatusCounts
	detailed  bool
	samples   []LoadTestSample
	sampleSeq int
	start     time.Time
}

// runLoadTestWorker loops calling hit until told to stop — ctx cancellation
// (the admin UI's "Stop" button, wired to the HTTP request's own context),
// the shared deadline, or the shared request-count cap, whichever comes
// first.
func runLoadTestWorker(limits *loadTestLimits, hit func() loadTestHit, acc *loadTestAccumulator) {
	for {
		if limits.shouldStop() {
			return
		}
		h := hit()
		acc.mu.Lock()
		acc.record(h)
		acc.mu.Unlock()
	}
}

// record must be called with acc.mu held. Lightweight fields (Index,
// ElapsedMs, LatencyMs, StatusCode, Error) are captured for EVERY request
// regardless of acc.detailed — cheap, and what every chart is drawn from
// (see internal/web/api's persist-after-run hook). ResponseBody/
// ResponseHeaders are only ever populated when acc.detailed is set — the
// one thing that flag still gates.
//
// acc.samples itself is capped at MaxLoadTestRequests regardless of mode:
// request-count mode already can't exceed that (ClampLoadTestConfig caps
// TotalRequests), but duration mode leaves TotalRequests at 0 (uncapped),
// so a long/concurrent enough run would otherwise append one sample per
// request with nothing bounding the list — millions of entries, each
// re-marshaled into the /loadtest response and the DB insert. sampleSeq
// and the latencies/statuses aggregates below are still updated for EVERY
// request past the cap, so percentiles/counts stay accurate for the whole
// run; only the lightweight per-request sample list stops growing.
func (acc *loadTestAccumulator) record(h loadTestHit) {
	if len(acc.samples) < MaxLoadTestRequests {
		sample := LoadTestSample{
			Index: acc.sampleSeq, ElapsedMs: time.Since(acc.start).Milliseconds(),
			LatencyMs: h.latencyMs, StatusCode: h.statusCode, Error: h.errMsg,
		}
		if acc.detailed {
			sample.ResponseBody = h.responseBody
			sample.ResponseHeaders = h.responseHeaders
		}
		acc.samples = append(acc.samples, sample)
	}
	acc.sampleSeq++
	if h.errMsg != "" {
		acc.statuses.CountError++
		return
	}
	acc.latencies = append(acc.latencies, h.latencyMs)
	switch {
	case h.statusCode >= 500:
		acc.statuses.Count5xx++
	case h.statusCode >= 400:
		acc.statuses.Count4xx++
	case h.statusCode >= 300:
		acc.statuses.Count3xx++
	default:
		acc.statuses.Count2xx++
	}
}

func summarizeLoadTest(acc *loadTestAccumulator, elapsed time.Duration) *LoadTestResult {
	statuses := acc.statuses
	total := statuses.Count2xx + statuses.Count3xx + statuses.Count4xx + statuses.Count5xx + statuses.CountError
	result := &LoadTestResult{
		TotalRequests: total,
		Statuses:      statuses,
		DurationMs:    elapsed.Milliseconds(),
		Samples:       acc.samples,
	}
	if elapsed > 0 {
		result.RequestsPerSec = float64(total) / elapsed.Seconds()
	}
	latencies := acc.latencies
	if len(latencies) == 0 {
		return result
	}

	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	var sum int64
	for _, v := range latencies {
		sum += v
	}
	result.MinMs = latencies[0]
	result.MaxMs = latencies[len(latencies)-1]
	result.AvgMs = sum / int64(len(latencies))
	result.P50Ms = percentile(latencies, 50)
	result.P90Ms = percentile(latencies, 90)
	result.P95Ms = percentile(latencies, 95)
	result.P99Ms = percentile(latencies, 99)
	return result
}

// percentile picks the value at the given percentile from an
// already-sorted slice using the nearest-rank method — good enough for a
// quick load-test summary without pulling in a stats library.
func percentile(sorted []int64, p int) int64 {
	if len(sorted) == 0 {
		return 0
	}
	idx := (p * len(sorted)) / 100
	if idx >= len(sorted) {
		idx = len(sorted) - 1
	}
	return sorted[idx]
}
