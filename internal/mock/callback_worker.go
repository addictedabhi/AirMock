package mock

import (
	"bytes"
	"context"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/smtp"
)

// SMTPSettingsProvider is the narrow slice of internal/smtp.Store the
// callback worker needs — a separate interface so this package doesn't
// have to depend on the concrete store type for what is, from here, just
// "get the current outgoing relay settings" for an email-channel callback.
type SMTPSettingsProvider interface {
	GetSettings() (smtp.Settings, error)
}

// HitLogger is the narrow slice of internal/hitlog.Store the callback
// worker needs — mirrors internal/engine/http.HitLogger's same-shaped
// interface, accepting either the plain store or its broadcasting wrapper.
type HitLogger interface {
	Record(e *hitlog.Entry) error
}

// CallbackWorker polls for due callback_jobs rows and delivers them. A
// worker pool bounded by concurrency, not one goroutine per job, since a
// misbehaving target shouldn't be able to spawn unbounded outbound calls.
type CallbackWorker struct {
	store        *Store
	client       *http.Client
	pollEvery    time.Duration
	concurrency  int
	smtpSettings SMTPSettingsProvider
	hitLog       HitLogger
}

func NewCallbackWorker(store *Store, smtpSettings SMTPSettingsProvider, hitLog HitLogger) *CallbackWorker {
	return &CallbackWorker{
		store:        store,
		client:       &http.Client{Timeout: 10 * time.Second},
		pollEvery:    500 * time.Millisecond,
		concurrency:  5,
		smtpSettings: smtpSettings,
		hitLog:       hitLog,
	}
}

// Start runs the poll loop until ctx is cancelled. Safe to call once per
// worker; the loop itself resumes any jobs already pending from a prior
// process (nothing extra needed — ClaimDueCallbackJobs just finds them).
func (w *CallbackWorker) Start(ctx context.Context) {
	go func() {
		ticker := time.NewTicker(w.pollEvery)
		defer ticker.Stop()
		sem := make(chan struct{}, w.concurrency)

		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				jobs, err := w.store.ClaimDueCallbackJobs(w.concurrency)
				if err != nil || len(jobs) == 0 {
					continue
				}
				for _, j := range jobs {
					sem <- struct{}{}
					go func(job *CallbackJob) {
						defer func() { <-sem }()
						w.deliver(job)
					}(j)
				}
			}
		}
	}()
}

// deliver attempts j once, records the outcome to the store (attempt
// count/status/next-retry) as before, and — new — logs the delivery to the
// hit log under hitlog.DirectionCallback, the same way every other kind of
// traffic this instance handles is logged. Previously that constant existed
// but nothing ever wrote a DirectionCallback row: callback deliveries were
// invisible in Log History, the Dashboard, and /metrics alike.
func (w *CallbackWorker) deliver(j *CallbackJob) {
	start := time.Now()
	status, err := w.attempt(j)
	latencyMs := time.Since(start).Milliseconds()

	if err != nil {
		w.markFailed(j, err)
	} else {
		w.markSucceeded(j)
	}
	w.recordHitLog(j, status, err, latencyMs)
}

func (w *CallbackWorker) attempt(j *CallbackJob) (int, error) {
	if j.Channel == "email" {
		if err := w.deliverEmail(j); err != nil {
			return 0, err
		}
		// Email has no HTTP status of its own — 200 signals "delivered",
		// the same "success bucket" convention RunWSLoadTest uses for
		// WebSocket connects, another non-HTTP-status-shaped outcome.
		return http.StatusOK, nil
	}
	return w.deliverHTTP(j)
}

func (w *CallbackWorker) recordHitLog(j *CallbackJob, status int, deliverErr error, latencyMs int64) {
	if w.hitLog == nil {
		return
	}
	protocol := "http"
	if j.Channel == "email" {
		protocol = "smtp"
	}
	entry := &hitlog.Entry{
		MockID:         j.MockID,
		ProtocolType:   protocol,
		Direction:      hitlog.DirectionCallback,
		Method:         j.Method,
		TargetURL:      j.TargetURL,
		ResponseStatus: status,
		LatencyMs:      latencyMs,
	}
	if deliverErr != nil {
		entry.ResponseBody = deliverErr.Error()
	}
	if err := w.hitLog.Record(entry); err != nil {
		log.Printf("airmock: failed to record hit log entry for callback job %q: %v", j.ID, err)
	}
}

// markFailed/markSucceeded wrap the store's own status-update calls with
// logging on failure — previously any of the 8 call sites across this file
// silently discarded a write error, meaning a callback job's real outcome
// (succeeded, or failed and awaiting retry) could vanish with no trace at
// all if the DB write itself failed, unlike every other background worker
// in this codebase which logs on this exact class of error.
func (w *CallbackWorker) markFailed(j *CallbackJob, deliveryErr error) {
	if err := w.store.MarkCallbackAttemptFailed(j, deliveryErr); err != nil {
		log.Printf("airmock: failed to record callback attempt failure for job %q: %v", j.ID, err)
	}
}

func (w *CallbackWorker) markSucceeded(j *CallbackJob) {
	if err := w.store.MarkCallbackSucceeded(j.ID); err != nil {
		log.Printf("airmock: failed to record callback success for job %q: %v", j.ID, err)
	}
}

func (w *CallbackWorker) deliverHTTP(j *CallbackJob) (int, error) {
	if j.Payload == "" && (j.Method == http.MethodPost || j.Method == http.MethodPut || j.Method == http.MethodPatch) {
		log.Printf("airmock: warning: %s callback for job %q to %s has an empty body (the mock has no callbackBodyTemplate)", j.Method, j.ID, j.TargetURL)
	}
	req, err := http.NewRequest(j.Method, j.TargetURL, bytes.NewReader([]byte(j.Payload)))
	if err != nil {
		return 0, fmt.Errorf("build request: %w", err)
	}
	for k, v := range j.Headers {
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

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		return resp.StatusCode, nil
	}
	return resp.StatusCode, fmt.Errorf("callback target returned status %d", resp.StatusCode)
}

// deliverEmail sends j.Payload as the HTML body of an email to j.TargetURL
// (an email address here, not a URL — see AsyncConfig.CallbackChannel)
// through whatever SMTP relay is currently configured, resolved fresh on
// every attempt rather than baked into the job at schedule time, the same
// way the gateway's TLS cert binding is resolved fresh rather than copied
// into each request.
func (w *CallbackWorker) deliverEmail(j *CallbackJob) error {
	if w.smtpSettings == nil {
		return fmt.Errorf("SMTP is not configured on this instance")
	}
	settings, err := w.smtpSettings.GetSettings()
	if err != nil {
		return fmt.Errorf("load smtp settings: %w", err)
	}
	return smtp.Send(settings, j.TargetURL, j.Subject, j.Payload)
}
