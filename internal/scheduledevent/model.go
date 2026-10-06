// Package scheduledevent fires an outbound HTTP callback on a fixed
// interval, independent of any incoming request — the mirror image of an
// async mock's callback (which only fires in response to a hit): this fires
// on a timer alone, for simulating periodic push traffic a real backend
// might generate on its own (IoT sensor readings, subscription-renewal
// webhooks, heartbeat pings) that a consuming application needs to handle
// without ever having to "ask" for it.
package scheduledevent

import "time"

type Event struct {
	ID           string            `json:"id"`
	Name         string            `json:"name"`
	Enabled      bool              `json:"enabled"`
	IntervalSecs int               `json:"intervalSecs"`
	TargetURL    string            `json:"targetUrl"`
	Method       string            `json:"method"`
	Headers      map[string]string `json:"headers,omitempty"`
	// BodyTemplate is rendered via the same text/template+sprig engine as
	// every mock response body — there's no triggering request to bind
	// {{.Request.*}} from, but sprig helpers ({{now}}, {{fake "uuid"}},
	// {{randInt 1 100}}) still work, which covers the common case of
	// wanting each fired payload to look slightly different.
	BodyTemplate string     `json:"bodyTemplate"`
	LastFiredAt  *time.Time `json:"lastFiredAt,omitempty"`
	LastStatus   int        `json:"lastStatus,omitempty"`
	LastError    string     `json:"lastError,omitempty"`
	NextFireAt   time.Time  `json:"nextFireAt"`
	CreatedAt    time.Time  `json:"createdAt"`
	UpdatedAt    time.Time  `json:"updatedAt"`
}
