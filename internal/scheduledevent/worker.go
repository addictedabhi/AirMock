package scheduledevent

import (
	"bytes"
	"context"
	"log"
	"net/http"
	"time"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
)

// HitLogger is the narrow slice of internal/hitlog.Store the worker needs —
// mirrors internal/engine/http.HitLogger's same-shaped interface, accepting
// either the plain store or its broadcasting wrapper so a fire shows up in
// the live tail immediately, the same as every other kind of traffic does,
// rather than only appearing once a client happens to re-query the log.
type HitLogger interface {
	Record(e *hitlog.Entry) error
}

// Worker polls for due events and fires them. One HTTP client shared across
// every fire (not one per event) — these are lightweight periodic pings,
// not a load-testing tool, so connection reuse matters more than isolation.
type Worker struct {
	store     *Store
	hitLog    HitLogger
	client    *http.Client
	pollEvery time.Duration
}

func NewWorker(store *Store, hitLog HitLogger) *Worker {
	return &Worker{
		store:     store,
		hitLog:    hitLog,
		client:    &http.Client{Timeout: 10 * time.Second},
		pollEvery: time.Second,
	}
}

func (w *Worker) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(w.pollEvery)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				due, err := w.store.ClaimDue(20)
				if err != nil || len(due) == 0 {
					continue
				}
				for _, e := range due {
					go w.Fire(e)
				}
			}
		}
	}()
}

// Fire renders e's body and delivers it once, logging the outcome to both
// the hit log (so it shows up in Log History/Dashboard traffic alongside
// every other kind of exchange) and the event's own last-fired/last-status
// fields (so the Scheduled Events list itself shows health without having
// to cross-reference the log). Exported so the admin API's "fire now" test
// action reuses the exact same delivery path a real tick would take,
// instead of a second copy that could drift out of sync.
func (w *Worker) Fire(e *Event) {
	start := time.Now()
	status, fireErr := w.deliver(e)
	latencyMs := time.Since(start).Milliseconds()

	if err := w.store.RecordFireResult(e.ID, status, fireErr); err != nil {
		log.Printf("airmock: failed to record scheduled event %q result: %v", e.ID, err)
	}

	entry := &hitlog.Entry{
		// MockID holds the event's own stable ID here, not a mock's — reused
		// for the same reason DirectionCallback/DirectionProxyCapture rows
		// use it: internal/hitlog.ScheduledEventMetricsSnapshot needs a
		// grouping key that survives a rename, unlike Name/Path (a mutable
		// display value re-editable at any time). Grouping Prometheus
		// metrics by Name directly used to mean renaming an event (or
		// automation creating/deleting uniquely-named one-off events)
		// fragmented its series into a new label combination every time,
		// with the old name's rows still counted by every scrape until
		// retention aged them out — unbounded series churn for a
		// long-running Prometheus target.
		MockID:         e.ID,
		ProtocolType:   "http",
		Direction:      hitlog.DirectionScheduledEvent,
		Method:         e.Method,
		Path:           e.Name,
		TargetURL:      e.TargetURL,
		ResponseStatus: status,
		LatencyMs:      latencyMs,
	}
	if fireErr != nil {
		entry.ResponseBody = fireErr.Error()
	}
	if err := w.hitLog.Record(entry); err != nil {
		log.Printf("airmock: failed to record hit log entry for scheduled event %q: %v", e.ID, err)
	}
}

func (w *Worker) deliver(e *Event) (int, error) {
	body, err := mock.RenderBody(e.BodyTemplate, mock.RequestContext{}, mock.RenderOptions{OwnerID: e.ID, DynamicValues: w.store})
	if err != nil {
		return 0, err
	}
	req, err := http.NewRequest(e.Method, e.TargetURL, bytes.NewReader([]byte(body)))
	if err != nil {
		return 0, err
	}
	for k, v := range e.Headers {
		req.Header.Set(k, v)
	}
	if req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}

	resp, err := w.client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	return resp.StatusCode, nil
}
