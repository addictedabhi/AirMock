package hitlog

import (
	"path/filepath"
	"testing"

	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func TestRecordGetAndListRoundTrip(t *testing.T) {
	s := newTestStore(t)

	entry := &Entry{
		MockID:          "m1",
		ProtocolType:    "proxy-capture",
		Direction:       DirectionProxyCapture,
		Method:          "GET",
		Path:            "/proxy/orders/1",
		TargetURL:       "https://upstream.example.com/orders/1",
		RequestHeaders:  map[string]string{"X-Test": "1"},
		ResponseStatus:  200,
		ResponseHeaders: map[string]string{"Content-Type": "application/json"},
		ResponseBody:    `{"ok":true}`,
		LatencyMs:       42,
	}
	if err := s.Record(entry); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if entry.ID == "" {
		t.Fatal("expected Record to assign an ID")
	}

	got, err := s.Get(entry.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ResponseBody != entry.ResponseBody || got.MockID != entry.MockID {
		t.Fatalf("unexpected Get result: %+v", got)
	}

	list, err := s.List("m1", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 entry for mock m1, got %d", len(list))
	}

	listOther, err := s.List("nonexistent", 10)
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listOther) != 0 {
		t.Fatalf("expected 0 entries for an unrelated mock, got %d", len(listOther))
	}
}

func TestGetMissingReturnsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Get("missing"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestQueryFiltersProtocolMethodStatusAndSearch locks in the server-side
// filters a log viewer's filter bar needs — these used to be applied only
// client-side over whatever page was already loaded, silently missing any
// match sitting beyond that page.
func TestQueryFiltersProtocolMethodStatusAndSearch(t *testing.T) {
	s := newTestStore(t)

	record := func(protocol, method, direction, path string, status int) *Entry {
		e := &Entry{ProtocolType: protocol, Method: method, Direction: direction, Path: path, ResponseStatus: status}
		if err := s.Record(e); err != nil {
			t.Fatalf("Record: %v", err)
		}
		return e
	}

	restOK := record("rest", "GET", DirectionInbound, "/orders/1", 200)
	restErr := record("rest", "POST", DirectionInbound, "/orders", 500)
	soapOK := record("soap", "POST", DirectionInbound, "/soap/service", 200)
	proxyTimeout := record("rest", "GET", DirectionProxyCapture, "/upstream/flaky", 0)

	cases := []struct {
		name string
		opts QueryOptions
		want []*Entry
	}{
		{"by protocol", QueryOptions{ProtocolType: "soap"}, []*Entry{soapOK}},
		{"by method", QueryOptions{Method: "POST"}, []*Entry{soapOK, restErr}},
		{"by direction", QueryOptions{Direction: DirectionProxyCapture}, []*Entry{proxyTimeout}},
		{"by status class 2xx", QueryOptions{StatusClass: "2xx"}, []*Entry{soapOK, restOK}},
		{"by status class 5xx", QueryOptions{StatusClass: "5xx"}, []*Entry{restErr}},
		{"by status class none", QueryOptions{StatusClass: "none"}, []*Entry{proxyTimeout}},
		{"by search substring", QueryOptions{Search: "flaky"}, []*Entry{proxyTimeout}},
		{"protocol and method combined", QueryOptions{ProtocolType: "rest", Method: "GET"}, []*Entry{proxyTimeout, restOK}},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := s.Query(tc.opts)
			if err != nil {
				t.Fatalf("Query: %v", err)
			}
			if len(got) != len(tc.want) {
				t.Fatalf("expected %d results, got %d: %+v", len(tc.want), len(got), got)
			}
			wantIDs := map[string]bool{}
			for _, w := range tc.want {
				wantIDs[w.ID] = true
			}
			for _, g := range got {
				if !wantIDs[g.ID] {
					t.Fatalf("unexpected entry in results: %+v", g)
				}
			}
		})
	}
}

func TestCallbackMetricsSnapshotGroupsByMockAndExcludesOtherDirections(t *testing.T) {
	s := newTestStore(t)
	must := func(e *Entry) {
		t.Helper()
		if err := s.Record(e); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	must(&Entry{MockID: "m1", ProtocolType: "http", Direction: DirectionCallback, ResponseStatus: 200, LatencyMs: 10})
	must(&Entry{MockID: "m1", ProtocolType: "http", Direction: DirectionCallback, ResponseStatus: 0, LatencyMs: 5})
	must(&Entry{MockID: "m2", ProtocolType: "http", Direction: DirectionCallback, ResponseStatus: 500, LatencyMs: 7})
	must(&Entry{MockID: "m1", ProtocolType: "http", Direction: DirectionInbound, ResponseStatus: 200, LatencyMs: 999})

	got, err := s.CallbackMetricsSnapshot()
	if err != nil {
		t.Fatalf("CallbackMetricsSnapshot: %v", err)
	}
	byLabel := map[string]DirectionMetrics{}
	for _, m := range got {
		byLabel[m.Label] = m
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 grouped rows (m1, m2), got %d: %+v", len(got), got)
	}
	m1 := byLabel["m1"]
	if m1.Total != 2 || m1.Count2xx != 1 || m1.CountNoResponse != 1 || m1.LatencySumMs != 15 {
		t.Fatalf("unexpected m1 callback metrics: %+v", m1)
	}
	if byLabel["m2"].Count5xx != 1 {
		t.Fatalf("unexpected m2 callback metrics: %+v", byLabel["m2"])
	}
}

func TestProxyCaptureMetricsSnapshotExcludesOtherDirections(t *testing.T) {
	s := newTestStore(t)
	if err := s.Record(&Entry{MockID: "m1", ProtocolType: "rest", Direction: DirectionProxyCapture, ResponseStatus: 200, LatencyMs: 999}); err != nil {
		t.Fatalf("Record: %v", err)
	}
	if err := s.Record(&Entry{MockID: "m1", ProtocolType: "rest", Direction: DirectionInbound, ResponseStatus: 200, LatencyMs: 1}); err != nil {
		t.Fatalf("Record: %v", err)
	}

	got, err := s.ProxyCaptureMetricsSnapshot()
	if err != nil {
		t.Fatalf("ProxyCaptureMetricsSnapshot: %v", err)
	}
	if len(got) != 1 || got[0].LatencySumMs != 999 {
		t.Fatalf("expected exactly the proxy-capture row, got %+v", got)
	}
}

// TestScheduledEventMetricsSnapshotGroupsByEventID guards against a real
// cardinality bug: grouping by the event's own mutable Name/Path (recorded
// into Path purely for historical display by scheduledevent.Worker.Fire)
// used to mean renaming an event fragmented its Prometheus series into a
// brand-new label combination, with the old name's rows still counted by
// every scrape until retention aged them out — unbounded series churn for
// a long-running Prometheus target. Grouping by MockID (which holds the
// event's own stable ID here, not a mock's — see worker.go's Fire) instead
// means the SAME event's rows always aggregate together regardless of
// what it happened to be named when each one fired; resolving the ID to a
// current display name is the metrics HANDLER's job (see
// internal/web/api/metrics.go's writeScheduledEventMetrics), not this
// store-level snapshot's.
func TestScheduledEventMetricsSnapshotGroupsByEventID(t *testing.T) {
	s := newTestStore(t)
	must := func(e *Entry) {
		t.Helper()
		if err := s.Record(e); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}
	must(&Entry{MockID: "event-1", ProtocolType: "http", Direction: DirectionScheduledEvent, Path: "Nightly ping", ResponseStatus: 200, LatencyMs: 40})
	// Same event ID, but recorded under a DIFFERENT Path — as if the event
	// was renamed between these two fires — must still aggregate together.
	must(&Entry{MockID: "event-1", ProtocolType: "http", Direction: DirectionScheduledEvent, Path: "Nightly ping (renamed)", ResponseStatus: 500, LatencyMs: 60})
	must(&Entry{MockID: "event-2", ProtocolType: "http", Direction: DirectionScheduledEvent, Path: "Hourly sync", ResponseStatus: 200, LatencyMs: 5})

	got, err := s.ScheduledEventMetricsSnapshot()
	if err != nil {
		t.Fatalf("ScheduledEventMetricsSnapshot: %v", err)
	}
	byLabel := map[string]DirectionMetrics{}
	for _, m := range got {
		byLabel[m.Label] = m
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 grouped rows (by event ID, not by name), got %d: %+v", len(got), got)
	}
	if np := byLabel["event-1"]; np.Total != 2 || np.Count2xx != 1 || np.Count5xx != 1 || np.LatencySumMs != 100 {
		t.Fatalf("expected event-1's two fires (under two different names) to aggregate together, got %+v", np)
	}
	if byLabel["event-2"].Total != 1 {
		t.Fatalf("unexpected event-2 metrics: %+v", byLabel["event-2"])
	}
}
