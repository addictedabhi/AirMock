// Package hitlog is the shared request/response capture log — used by
// proxy-capture mocks (phase 1.11) to record what a real upstream actually
// returned, and extended in phase 1.12 with a live tail, retention policy,
// and dashboard charts on top of the same table. Built now, ahead of its
// official phase, because proxy capture has nothing to promote-to-mock
// without somewhere to record captures into first.
package hitlog

import "time"

// Direction distinguishes what kind of traffic a row represents.
const (
	DirectionInbound        = "inbound"              // a normal mock hit (added in 1.12)
	DirectionProxyCapture   = "proxy-capture"        // a proxy-mode mock's real upstream call
	DirectionOutboundCall   = "outbound-client-call" // the built-in API client calling something
	DirectionCallback       = "callback"             // an async mock's callback delivery (added in 1.12)
	DirectionScheduledEvent = "scheduled-event"      // a scheduledevent firing on its own timer, no triggering request at all
)

type Entry struct {
	ID              string            `json:"id"`
	MockID          string            `json:"mockId,omitempty"`
	ProtocolType    string            `json:"protocolType"`
	Direction       string            `json:"direction"`
	Method          string            `json:"method,omitempty"`
	Path            string            `json:"path,omitempty"`
	TargetURL       string            `json:"targetUrl,omitempty"`
	RequestHeaders  map[string]string `json:"requestHeaders,omitempty"`
	RequestBody     string            `json:"requestBody,omitempty"`
	ResponseStatus  int               `json:"responseStatus,omitempty"`
	ResponseHeaders map[string]string `json:"responseHeaders,omitempty"`
	ResponseBody    string            `json:"responseBody,omitempty"`
	LatencyMs       int64             `json:"latencyMs"`
	// CollectionID/CollectionName/RequestName identify which API-client
	// Collection (and which request within it) triggered a
	// DirectionOutboundCall entry — empty for every other direction, and
	// empty here too for a call sent from an unsaved/draft tab that isn't
	// part of any collection. What the Dashboard's "top collections by
	// traffic" and Log History's collection filter group by.
	CollectionID   string    `json:"collectionId,omitempty"`
	CollectionName string    `json:"collectionName,omitempty"`
	RequestName    string    `json:"requestName,omitempty"`
	CreatedAt      time.Time `json:"createdAt"`
}

// HeadersFromMulti collapses a multi-value header map (as net/http.Header
// naturally is) into first-value-per-key, which is all a captured log
// entry needs to display.
func HeadersFromMulti(h map[string][]string) map[string]string {
	out := make(map[string]string, len(h))
	for k, vv := range h {
		if len(vv) > 0 {
			out[k] = vv[0]
		}
	}
	return out
}
