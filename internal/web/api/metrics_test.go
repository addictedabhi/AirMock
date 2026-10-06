package api

import (
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/scheduledevent"
	"github.com/addictedabhi/airmock/internal/storage"
)

func TestMetricsHandler(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mockStore := mock.NewStore(db)
	hitStore := hitlog.NewStore(db)
	certStore := certs.NewStore(db)
	scheduledEventStore := scheduledevent.NewStore(db)

	created, err := mockStore.Create(&mock.Definition{
		Name: "orders-api", ProtocolType: "rest", Method: "GET", PathPattern: "/orders", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create mock: %v", err)
	}
	if _, err := mockStore.Create(&mock.Definition{
		Name: "disabled-mock", ProtocolType: "rest", Method: "GET", PathPattern: "/off", Enabled: false,
		Response: mock.ResponseTemplate{StatusCode: 200},
	}); err != nil {
		t.Fatalf("Create disabled mock: %v", err)
	}

	mustRecordEntry(t, hitStore, &hitlog.Entry{MockID: created.ID, ProtocolType: "rest", Direction: "inbound", ResponseStatus: 200, LatencyMs: 10})
	mustRecordEntry(t, hitStore, &hitlog.Entry{MockID: created.ID, ProtocolType: "rest", Direction: "inbound", ResponseStatus: 200, LatencyMs: 20})
	mustRecordEntry(t, hitStore, &hitlog.Entry{MockID: created.ID, ProtocolType: "rest", Direction: "inbound", ResponseStatus: 500, LatencyMs: 30})
	// A proxy-capture entry must NOT count toward the inbound hit total.
	mustRecordEntry(t, hitStore, &hitlog.Entry{MockID: created.ID, ProtocolType: "rest", Direction: "proxy-capture", ResponseStatus: 200, LatencyMs: 999})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	NewMetricsHandler(mockStore, hitStore, certStore, scheduledEventStore).ServeHTTP(rec, req)

	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Fatalf("expected text/plain content type, got %q", ct)
	}
	body := rec.Body.String()

	mustContain(t, body, `airmock_mocks_configured{enabled="true"} 1`)
	mustContain(t, body, `airmock_mocks_configured{enabled="false"} 1`)
	mustContain(t, body, `airmock_hits_total{mock="orders-api",protocol="rest",status_class="2xx"} 2`)
	mustContain(t, body, `airmock_hits_total{mock="orders-api",protocol="rest",status_class="5xx"} 1`)
	mustContain(t, body, `airmock_hit_latency_ms_sum{mock="orders-api",protocol="rest"} 60`)
	if strings.Contains(body, "airmock_hit_latency_ms_sum{mock=\"orders-api\",protocol=\"rest\"} 999") {
		t.Fatalf("proxy-capture latency leaked into the inbound-only metrics:\n%s", body)
	}
	// The proxy-capture entry has its own metric family instead — visible,
	// just not blended into the inbound-only counters above.
	mustContain(t, body, `airmock_proxy_captures_total{mock="orders-api",protocol="rest",status_class="2xx"} 1`)
	mustContain(t, body, `airmock_proxy_captures_latency_ms_sum{mock="orders-api",protocol="rest"} 999`)

	// HELP/TYPE for a repeated-label metric name must appear exactly once.
	if n := strings.Count(body, "# TYPE airmock_mocks_configured"); n != 1 {
		t.Fatalf("expected exactly one TYPE line for airmock_mocks_configured, got %d:\n%s", n, body)
	}
}

// TestMetricsHandlerExposesCallbackAndScheduledEventOutcomes guards against
// the real gap this closes: DirectionCallback/DirectionScheduledEvent rows
// existed in hit_logs (once the callback worker/scheduled-event worker fix
// elsewhere in this pass started actually recording them) but /metrics
// never rendered them at all — a scrape had no way to see async-callback or
// scheduled-event health.
func TestMetricsHandlerExposesCallbackAndScheduledEventOutcomes(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mockStore := mock.NewStore(db)
	hitStore := hitlog.NewStore(db)
	certStore := certs.NewStore(db)
	scheduledEventStore := scheduledevent.NewStore(db)

	created, err := mockStore.Create(&mock.Definition{
		Name: "orders-api", ProtocolType: "rest", Method: "GET", PathPattern: "/orders", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create mock: %v", err)
	}
	event, err := scheduledEventStore.Create(&scheduledevent.Event{
		Name: "Nightly ping", Enabled: true, IntervalSecs: 3600, TargetURL: "https://example.com/webhook", Method: "POST",
	})
	if err != nil {
		t.Fatalf("Create scheduled event: %v", err)
	}

	mustRecordEntry(t, hitStore, &hitlog.Entry{MockID: created.ID, ProtocolType: "http", Direction: hitlog.DirectionCallback, ResponseStatus: 200, LatencyMs: 15})
	mustRecordEntry(t, hitStore, &hitlog.Entry{MockID: created.ID, ProtocolType: "http", Direction: hitlog.DirectionCallback, ResponseStatus: 0, LatencyMs: 5})
	// MockID here holds the scheduled event's own stable ID (see
	// internal/scheduledevent/worker.go's Fire) — Path still carries the
	// event's Name for historical display, but grouping/labeling now
	// resolves through the ID so a later rename doesn't fragment the series.
	mustRecordEntry(t, hitStore, &hitlog.Entry{MockID: event.ID, ProtocolType: "http", Direction: hitlog.DirectionScheduledEvent, Path: event.Name, ResponseStatus: 200, LatencyMs: 40})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	NewMetricsHandler(mockStore, hitStore, certStore, scheduledEventStore).ServeHTTP(rec, req)
	body := rec.Body.String()

	mustContain(t, body, `airmock_callback_deliveries_total{mock="orders-api",protocol="http",status_class="2xx"} 1`)
	mustContain(t, body, `airmock_callback_deliveries_total{mock="orders-api",protocol="http",status_class="no_response"} 1`)
	mustContain(t, body, `airmock_callback_deliveries_latency_ms_sum{mock="orders-api",protocol="http"} 20`)
	mustContain(t, body, `airmock_scheduled_event_fires_total{event="Nightly ping",protocol="http",status_class="2xx"} 1`)
	mustContain(t, body, `airmock_scheduled_event_fires_latency_ms_sum{event="Nightly ping",protocol="http"} 40`)
	mustContain(t, body, `# TYPE airmock_callback_jobs gauge`)
}

// TestMetricsHandlerScheduledEventSeriesSurvivesRename guards against the
// real cardinality gap this closes: metrics used to group by the event's
// mutable Name/Path directly, so renaming an event fragmented its
// Prometheus series into a brand-new label combination, with the old
// name's historical rows still counted by every scrape until retention
// aged them out. Grouping by the event's own stable ID and resolving the
// CURRENT name at render time means a rename must show ALL of an event's
// hits (both before and after the rename) under its one current name.
func TestMetricsHandlerScheduledEventSeriesSurvivesRename(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mockStore := mock.NewStore(db)
	hitStore := hitlog.NewStore(db)
	certStore := certs.NewStore(db)
	scheduledEventStore := scheduledevent.NewStore(db)

	event, err := scheduledEventStore.Create(&scheduledevent.Event{
		Name: "Old Name", Enabled: true, IntervalSecs: 3600, TargetURL: "https://example.com/webhook", Method: "POST",
	})
	if err != nil {
		t.Fatalf("Create scheduled event: %v", err)
	}
	// Fired once under the old name...
	mustRecordEntry(t, hitStore, &hitlog.Entry{MockID: event.ID, ProtocolType: "http", Direction: hitlog.DirectionScheduledEvent, Path: "Old Name", ResponseStatus: 200, LatencyMs: 10})

	// ...then renamed...
	event.Name = "New Name"
	if _, err := scheduledEventStore.Update(event); err != nil {
		t.Fatalf("Update scheduled event: %v", err)
	}
	// ...and fired again under the new name.
	mustRecordEntry(t, hitStore, &hitlog.Entry{MockID: event.ID, ProtocolType: "http", Direction: hitlog.DirectionScheduledEvent, Path: "New Name", ResponseStatus: 200, LatencyMs: 20})

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	NewMetricsHandler(mockStore, hitStore, certStore, scheduledEventStore).ServeHTTP(rec, req)
	body := rec.Body.String()

	if strings.Contains(body, `event="Old Name"`) {
		t.Fatalf("expected no series under the stale pre-rename name, got:\n%s", body)
	}
	mustContain(t, body, `airmock_scheduled_event_fires_total{event="New Name",protocol="http",status_class="2xx"} 2`)
	mustContain(t, body, `airmock_scheduled_event_fires_latency_ms_sum{event="New Name",protocol="http"} 30`)
}

// TestMetricsHandlerExposesCertificateExpiryGauge guards against a real
// gap: certificate expiry was checkable only by a human opening the
// Certificates page's expiry banner — nothing exposed it to an actual
// alerting pipeline. A real Prometheus Alertmanager rule like
// `airmock_certificate_expiry_seconds < 86400*7` needs this as a scrapeable
// gauge, not a UI-only warning.
func TestMetricsHandlerExposesCertificateExpiryGauge(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	mockStore := mock.NewStore(db)
	hitStore := hitlog.NewStore(db)
	certStore := certs.NewStore(db)
	scheduledEventStore := scheduledevent.NewStore(db)

	cert, err := certs.Generate(certs.GenerateRequest{
		Name: "test-ca", Kind: certs.KindCA, CommonName: "Test CA", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := certStore.Save(cert); err != nil {
		t.Fatalf("Save: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/metrics", nil)
	NewMetricsHandler(mockStore, hitStore, certStore, scheduledEventStore).ServeHTTP(rec, req)
	body := rec.Body.String()

	mustContain(t, body, `# TYPE airmock_certificate_expiry_seconds gauge`)
	mustContain(t, body, `cert="test-ca"`)
	mustContain(t, body, `kind="ca"`)
}

func mustContain(t *testing.T, haystack, needle string) {
	t.Helper()
	if !strings.Contains(haystack, needle) {
		t.Fatalf("expected output to contain %q, got:\n%s", needle, haystack)
	}
}
