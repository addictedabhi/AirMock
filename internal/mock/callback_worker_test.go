package mock

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/addictedabhi/airmock/internal/hitlog"
)

func TestCallbackJobStatusCountsGroupsByStatus(t *testing.T) {
	s := newTestStore(t)
	def, err := s.Create(&Definition{Name: "m1", ProtocolType: "rest", Method: "GET", PathPattern: "/x", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ScheduleCallback(def.ID, CallbackRequest{TargetURL: "https://example.com/a", Method: "POST"}); err != nil {
		t.Fatalf("ScheduleCallback: %v", err)
	}
	if err := s.ScheduleCallback(def.ID, CallbackRequest{TargetURL: "https://example.com/b", Method: "POST"}); err != nil {
		t.Fatalf("ScheduleCallback: %v", err)
	}
	jobs, err := s.ClaimDueCallbackJobs(1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("ClaimDueCallbackJobs: %v, %+v", err, jobs)
	}
	if err := s.MarkCallbackSucceeded(jobs[0].ID); err != nil {
		t.Fatalf("MarkCallbackSucceeded: %v", err)
	}

	// ClaimDueCallbackJobs leases every due row in one UPDATE regardless of
	// limit (limit only caps how many are returned to this caller), so the
	// second job is 'claimed' (leased, not yet delivered) rather than still
	// 'pending' — this test asserts CallbackJobStatusCounts reflects that
	// real state accurately, not a specific claiming policy.
	counts, err := s.CallbackJobStatusCounts()
	if err != nil {
		t.Fatalf("CallbackJobStatusCounts: %v", err)
	}
	if counts["succeeded"] != 1 || counts["claimed"] != 1 {
		t.Fatalf("expected 1 succeeded and 1 claimed, got %+v", counts)
	}
}

type fakeHitLogger struct {
	entries []*hitlog.Entry
}

func (f *fakeHitLogger) Record(e *hitlog.Entry) error {
	f.entries = append(f.entries, e)
	return nil
}

// TestCallbackWorkerRecordsHitLogEntry guards against a real gap:
// hitlog.DirectionCallback existed but nothing ever wrote a row under it —
// callback deliveries were invisible in Log History, the Dashboard, and
// /metrics alike. A successful HTTP delivery must now log a DirectionCallback
// entry with the real status code and the originating mock's ID.
func TestCallbackWorkerRecordsHitLogEntry(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	s := newTestStore(t)
	def, err := s.Create(&Definition{Name: "m1", ProtocolType: "rest", Method: "GET", PathPattern: "/x", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ScheduleCallback(def.ID, CallbackRequest{TargetURL: srv.URL, Method: "POST", Payload: "{}"}); err != nil {
		t.Fatalf("ScheduleCallback: %v", err)
	}

	logger := &fakeHitLogger{}
	worker := NewCallbackWorker(s, nil, logger)
	jobs, err := s.ClaimDueCallbackJobs(1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("ClaimDueCallbackJobs: %v, %+v", err, jobs)
	}
	worker.deliver(jobs[0])

	if len(logger.entries) != 1 {
		t.Fatalf("expected exactly 1 hit log entry, got %d", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.Direction != hitlog.DirectionCallback || entry.MockID != def.ID || entry.ResponseStatus != http.StatusOK {
		t.Fatalf("unexpected hit log entry: %+v", entry)
	}
}

// TestCallbackWorkerRecordsNoResponseOnDeliveryFailure guards the "never
// got a response at all" case (unreachable target) — ResponseStatus must
// be 0 with the error captured in ResponseBody, matching apiclient's
// existing CountError convention for other non-HTTP-status-shaped outcomes.
func TestCallbackWorkerRecordsNoResponseOnDeliveryFailure(t *testing.T) {
	s := newTestStore(t)
	def, err := s.Create(&Definition{Name: "m1", ProtocolType: "rest", Method: "GET", PathPattern: "/x", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if err := s.ScheduleCallback(def.ID, CallbackRequest{TargetURL: "http://127.0.0.1:1", Method: "POST"}); err != nil {
		t.Fatalf("ScheduleCallback: %v", err)
	}

	logger := &fakeHitLogger{}
	worker := NewCallbackWorker(s, nil, logger)
	jobs, err := s.ClaimDueCallbackJobs(1)
	if err != nil || len(jobs) != 1 {
		t.Fatalf("ClaimDueCallbackJobs: %v, %+v", err, jobs)
	}
	worker.deliver(jobs[0])

	if len(logger.entries) != 1 {
		t.Fatalf("expected exactly 1 hit log entry, got %d", len(logger.entries))
	}
	entry := logger.entries[0]
	if entry.ResponseStatus != 0 || entry.ResponseBody == "" {
		t.Fatalf("expected ResponseStatus=0 and a captured error, got %+v", entry)
	}
}
