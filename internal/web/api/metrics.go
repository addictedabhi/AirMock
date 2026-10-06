package api

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/scheduledevent"
)

// MetricsHandler renders a Prometheus text-exposition-format snapshot at a
// bare /metrics — deliberately hand-rolled rather than pulling in
// prometheus/client_golang (which drags in its own dependency tree) since
// the format itself is just plain "metric{labels} value" lines; a scraper
// doesn't care how the text was produced. Raw counters/sums are exposed
// rather than pre-computed percentiles — idiomatic Prometheus practice is
// to let PromQL (or Grafana) derive rates/averages from counters, the same
// principle the load-tester's /api/apiclient/loadtest result deliberately
// does NOT follow (that one's a one-shot summary a human reads directly,
// not a time series a query engine aggregates over).
type MetricsHandler struct {
	mockStore           *mock.Store
	hitLog              *hitlog.Store
	certStore           *certs.Store
	scheduledEventStore *scheduledevent.Store
}

func NewMetricsHandler(mockStore *mock.Store, hitLog *hitlog.Store, certStore *certs.Store, scheduledEventStore *scheduledevent.Store) *MetricsHandler {
	return &MetricsHandler{mockStore: mockStore, hitLog: hitLog, certStore: certStore, scheduledEventStore: scheduledEventStore}
}

func (h *MetricsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	mocks, err := h.mockStore.List()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	metrics, err := h.hitLog.MetricsSnapshot()
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	nameByID := make(map[string]string, len(mocks))
	var enabledCount, disabledCount int
	for _, m := range mocks {
		nameByID[m.ID] = m.Name
		if m.Enabled {
			enabledCount++
		} else {
			disabledCount++
		}
	}
	// Deterministic output — a scraper doesn't care about ordering, but a
	// human reading it by hand (or a test asserting on it) does.
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].MockID < metrics[j].MockID })

	var b strings.Builder
	writeHeader(&b, "airmock_mocks_configured", "gauge", "Mocks currently configured on this instance")
	writeMetricLine(&b, "airmock_mocks_configured", map[string]string{"enabled": "true"}, float64(enabledCount))
	writeMetricLine(&b, "airmock_mocks_configured", map[string]string{"enabled": "false"}, float64(disabledCount))

	writeHeader(&b, "airmock_hits_total", "counter", "Total hits served per mock, by response status class")
	writeHeader(&b, "airmock_hit_latency_ms_sum", "counter", "Sum of response latency in milliseconds per mock — divide by airmock_hits_total for the mean")
	for _, m := range metrics {
		name := nameByID[m.MockID]
		if name == "" {
			name = m.MockID // a since-deleted mock's historical hits still count
		}
		labels := map[string]string{"mock": name, "protocol": m.ProtocolType}
		writeStatusClassCounters(&b, labels, m)
		writeMetricLine(&b, "airmock_hit_latency_ms_sum", labels, float64(m.LatencySumMs))
	}

	if err := h.writeCallbackMetrics(&b, nameByID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.writeProxyCaptureMetrics(&b, nameByID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.writeScheduledEventMetrics(&b); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.writeCallbackJobStatusGauge(&b); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if err := h.writeCertificateExpiryGauge(&b); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}

	w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
	w.Write([]byte(b.String()))
}

// writeCertificateExpiryGauge is the proactive half of certificate expiry
// handling: AirMock deliberately does NOT auto-renew a cert bound to a
// listener (silently rotating it could break a client that pinned the old
// one), so instead every stored cert's remaining lifetime is exposed as a
// gauge a real alerting pipeline (Prometheus Alertmanager, Grafana) can
// fire on — e.g. `airmock_certificate_expiry_seconds < 86400*7` — rather
// than relying solely on a human happening to open the Certificates page's
// existing expiry banner before something actually breaks.
func (h *MetricsHandler) writeCertificateExpiryGauge(b *strings.Builder) error {
	list, err := h.certStore.List()
	if err != nil {
		return err
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Name < list[j].Name })

	writeHeader(b, "airmock_certificate_expiry_seconds", "gauge", "Seconds until each stored certificate's NotAfter — negative once already expired")
	now := time.Now()
	for _, c := range list {
		labels := map[string]string{"cert": c.Name, "kind": string(c.Kind)}
		writeMetricLine(b, "airmock_certificate_expiry_seconds", labels, c.NotAfter.Sub(now).Seconds())
	}
	return nil
}

// writeDirectionMetrics renders one DirectionMetrics slice as two metric
// families (a status-class counter and a latency sum), sharing the exact
// same rendering shape airmock_hits_total/airmock_hit_latency_ms_sum
// already use for inbound hits — the only difference between callback/
// proxy-capture/scheduled-event metrics and inbound ones is which hit_logs
// rows feed them in, not how they're exposed.
func writeDirectionMetrics(b *strings.Builder, metricPrefix, help, labelKey string, resolveLabel func(hitlog.DirectionMetrics) string, metrics []hitlog.DirectionMetrics) {
	countName := metricPrefix + "_total"
	latencyName := metricPrefix + "_latency_ms_sum"
	writeHeader(b, countName, "counter", help)
	writeHeader(b, latencyName, "counter", "Sum of latency in milliseconds — divide by "+countName+" for the mean")
	for _, m := range metrics {
		labels := map[string]string{labelKey: resolveLabel(m), "protocol": m.ProtocolType}
		classes := []struct {
			class string
			count int64
		}{
			{"2xx", m.Count2xx}, {"3xx", m.Count3xx}, {"4xx", m.Count4xx}, {"5xx", m.Count5xx}, {"no_response", m.CountNoResponse},
		}
		for _, c := range classes {
			lbl := map[string]string{labelKey: labels[labelKey], "protocol": labels["protocol"], "status_class": c.class}
			writeMetricLine(b, countName, lbl, float64(c.count))
		}
		writeMetricLine(b, latencyName, labels, float64(m.LatencySumMs))
	}
}

func (h *MetricsHandler) writeCallbackMetrics(b *strings.Builder, nameByID map[string]string) error {
	metrics, err := h.hitLog.CallbackMetricsSnapshot()
	if err != nil {
		return err
	}
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Label < metrics[j].Label })
	writeDirectionMetrics(b, "airmock_callback_deliveries", "Async-mock callback deliveries, by outcome status class", "mock",
		func(m hitlog.DirectionMetrics) string { return resolveDisplayName(nameByID, m.Label) }, metrics)
	return nil
}

func (h *MetricsHandler) writeProxyCaptureMetrics(b *strings.Builder, nameByID map[string]string) error {
	metrics, err := h.hitLog.ProxyCaptureMetricsSnapshot()
	if err != nil {
		return err
	}
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Label < metrics[j].Label })
	writeDirectionMetrics(b, "airmock_proxy_captures", "Proxy-mode mocks' captured upstream calls, by response status class", "mock",
		func(m hitlog.DirectionMetrics) string { return resolveDisplayName(nameByID, m.Label) }, metrics)
	return nil
}

// writeScheduledEventMetrics resolves each event's CURRENT Name from its
// stable ID (m.Label — see ScheduledEventMetricsSnapshot's doc comment for
// why grouping by ID rather than Name matters) — the same
// resolve-ID-to-current-display-name pattern resolveDisplayName already uses
// for callback/proxy-capture metrics, and for the same reason: a renamed or
// since-deleted event must not fragment its own historical series.
func (h *MetricsHandler) writeScheduledEventMetrics(b *strings.Builder) error {
	metrics, err := h.hitLog.ScheduledEventMetricsSnapshot()
	if err != nil {
		return err
	}
	nameByID := map[string]string{}
	if events, err := h.scheduledEventStore.List(); err == nil {
		for _, e := range events {
			nameByID[e.ID] = e.Name
		}
	}
	sort.Slice(metrics, func(i, j int) bool { return metrics[i].Label < metrics[j].Label })
	writeDirectionMetrics(b, "airmock_scheduled_event_fires", "Scheduled event deliveries, by outcome status class", "event",
		func(m hitlog.DirectionMetrics) string { return resolveDisplayName(nameByID, m.Label) }, metrics)
	return nil
}

// resolveDisplayName mirrors the inbound-hits loop's own fallback: a
// since-deleted mock's historical callback/proxy-capture rows still count,
// labeled by ID rather than dropped.
func resolveDisplayName(nameByID map[string]string, mockID string) string {
	if name := nameByID[mockID]; name != "" {
		return name
	}
	return mockID
}

// writeCallbackJobStatusGauge exposes the callback_jobs queue's current
// state (how many jobs are pending/claimed/succeeded/failed right now) —
// unlike the delivery-outcome counters above (historical, hit_logs-based),
// this is a live gauge: a growing "pending" or "failed" count signals
// callbacks are backing up or a target is down, which no purely historical
// counter can show on its own.
func (h *MetricsHandler) writeCallbackJobStatusGauge(b *strings.Builder) error {
	counts, err := h.mockStore.CallbackJobStatusCounts()
	if err != nil {
		return err
	}
	writeHeader(b, "airmock_callback_jobs", "gauge", "Callback jobs currently in each status")
	statuses := make([]string, 0, len(counts))
	for status := range counts {
		statuses = append(statuses, status)
	}
	sort.Strings(statuses)
	for _, status := range statuses {
		writeMetricLine(b, "airmock_callback_jobs", map[string]string{"status": status}, float64(counts[status]))
	}
	return nil
}

func writeStatusClassCounters(b *strings.Builder, labels map[string]string, m hitlog.MockMetrics) {
	classes := []struct {
		class string
		count int64
	}{
		{"2xx", m.Count2xx}, {"3xx", m.Count3xx}, {"4xx", m.Count4xx}, {"5xx", m.Count5xx},
	}
	for _, c := range classes {
		lbl := map[string]string{"mock": labels["mock"], "protocol": labels["protocol"], "status_class": c.class}
		writeMetricLine(b, "airmock_hits_total", lbl, float64(c.count))
	}
}

func writeHeader(b *strings.Builder, name, metricType, help string) {
	fmt.Fprintf(b, "# HELP %s %s\n# TYPE %s %s\n", name, help, name, metricType)
}

func writeMetricLine(b *strings.Builder, name string, labels map[string]string, value float64) {
	if len(labels) == 0 {
		fmt.Fprintf(b, "%s %g\n", name, value)
		return
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	parts := make([]string, len(keys))
	for i, k := range keys {
		parts[i] = fmt.Sprintf("%s=%q", k, labels[k])
	}
	fmt.Fprintf(b, "%s{%s} %g\n", name, strings.Join(parts, ","), value)
}
