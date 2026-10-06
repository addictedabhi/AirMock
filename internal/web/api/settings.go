package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	httpengine "github.com/addictedabhi/airmock/internal/engine/http"
	"github.com/addictedabhi/airmock/internal/settings"
)

// RedactedHeaderSetter is the narrow slice of httpengine.Engine this
// handler needs — applying a saved redacted-headers list live, without
// requiring a restart, the same way GatewayHandler applies TLS settings.
type RedactedHeaderSetter interface {
	SetRedactedHeaders(keys []string)
}

// BodyCaptureLimitSetter is the narrow slice of httpengine.Engine this
// handler needs — applying a saved hit-log body-capture limit live, same
// reasoning as RedactedHeaderSetter above. A single concrete
// *httpengine.Engine value satisfies both interfaces; SettingsHandler just
// takes two narrow views of it rather than one broader one, matching how
// VersionPruner below is also its own separate narrow interface.
type BodyCaptureLimitSetter interface {
	SetBodyCaptureLimit(limitBytes int)
}

// VersionPruner is the narrow slice of internal/mock.Store this handler
// needs — immediately trimming every mock's already-stored version history
// down to a newly-lowered MaxVersionsPerMock, rather than leaving a mock
// that already has more history than the new limit to wait for its next
// edit before snapshotVersion's own pruning would naturally catch it.
type VersionPruner interface {
	EnforceMaxVersionsForAllMocks(maxVersions int) error
}

// httpEngineSettingsTarget is the combined view of httpengine.Engine this
// handler needs — a single concrete engine value satisfies both, so the
// constructor still takes just one argument for it.
type httpEngineSettingsTarget interface {
	RedactedHeaderSetter
	BodyCaptureLimitSetter
	SetCORS(c httpengine.CORSConfig)
}

// SessionTimeoutSetter is the narrow slice of auth.Manager this handler
// needs — applying a saved session timeout live, same reasoning as
// RedactedHeaderSetter/BodyCaptureLimitSetter above.
type SessionTimeoutSetter interface {
	SetSessionTimeout(d time.Duration)
}

type SettingsHandler struct {
	store          *settings.Store
	httpEngine     httpEngineSettingsTarget
	versionPruner  VersionPruner
	sessionTimeout SessionTimeoutSetter
}

func NewSettingsHandler(store *settings.Store, httpEngine httpEngineSettingsTarget, versionPruner VersionPruner, sessionTimeout SessionTimeoutSetter) *SettingsHandler {
	return &SettingsHandler{store: store, httpEngine: httpEngine, versionPruner: versionPruner, sessionTimeout: sessionTimeout}
}

func (h *SettingsHandler) Routes(r chi.Router) {
	r.Get("/", h.get)
	r.Put("/", h.put)
}

type settingsBody struct {
	HitLogMaxAgeDays          int      `json:"hitLogMaxAgeDays"`
	HitLogMaxRowsPerMock      int      `json:"hitLogMaxRowsPerMock"`
	MaxVersionsPerMock        int      `json:"maxVersionsPerMock"`
	RedactedHeaders           []string `json:"redactedHeaders"`
	DefaultResponseDelayMs    int      `json:"defaultResponseDelayMs"`
	DefaultFailureRatePercent float64  `json:"defaultFailureRatePercent"`
	MaxCapturedBodyBytes      int      `json:"maxCapturedBodyBytes"`
	SessionTimeoutMinutes     int      `json:"sessionTimeoutMinutes"`
	// InactivityLockMinutes is purely a frontend concern (see
	// ui/src/lib/inactivityWatcher.js) — this handler only stores/returns
	// it, no backend component needs to react to it live the way
	// SessionTimeoutMinutes does.
	InactivityLockMinutes     int `json:"inactivityLockMinutes"`
	LoadTestRunMaxAgeDays     int `json:"loadTestRunMaxAgeDays"`
	LoadTestRunMaxRowsPerItem int `json:"loadTestRunMaxRowsPerItem"`
	// CORS is optional on PUT: a client that does not send it leaves the
	// stored CORS settings untouched.
	CORS *corsBody `json:"cors,omitempty"`
}

type corsBody struct {
	Enabled          bool   `json:"enabled"`
	AllowOrigin      string `json:"allowOrigin"`
	AllowMethods     string `json:"allowMethods"`
	AllowHeaders     string `json:"allowHeaders"`
	AllowCredentials bool   `json:"allowCredentials"`
	MaxAgeSecs       int    `json:"maxAgeSecs"`
}

func corsBodyFrom(c settings.CORS) *corsBody {
	return &corsBody{Enabled: c.Enabled, AllowOrigin: c.AllowOrigin, AllowMethods: c.AllowMethods, AllowHeaders: c.AllowHeaders, AllowCredentials: c.AllowCredentials, MaxAgeSecs: c.MaxAgeSecs}
}

func (c *corsBody) settings() settings.CORS {
	return settings.CORS{Enabled: c.Enabled, AllowOrigin: c.AllowOrigin, AllowMethods: c.AllowMethods, AllowHeaders: c.AllowHeaders, AllowCredentials: c.AllowCredentials, MaxAgeSecs: c.MaxAgeSecs}
}

// EngineCORS converts stored CORS settings to the gateway's config.
func EngineCORS(c settings.CORS) httpengine.CORSConfig {
	return httpengine.CORSConfig{Enabled: c.Enabled, AllowOrigin: c.AllowOrigin, AllowMethods: c.AllowMethods, AllowHeaders: c.AllowHeaders, AllowCredentials: c.AllowCredentials, MaxAgeSecs: c.MaxAgeSecs}
}

func settingsBodyFrom(s *settings.Settings) settingsBody {
	return settingsBody{
		HitLogMaxAgeDays:          s.HitLogMaxAgeDays,
		HitLogMaxRowsPerMock:      s.HitLogMaxRowsPerMock,
		MaxVersionsPerMock:        s.MaxVersionsPerMock,
		RedactedHeaders:           s.RedactedHeaders,
		DefaultResponseDelayMs:    s.DefaultResponseDelayMs,
		DefaultFailureRatePercent: s.DefaultFailureRatePercent,
		MaxCapturedBodyBytes:      s.MaxCapturedBodyBytes,
		SessionTimeoutMinutes:     s.SessionTimeoutMinutes,
		InactivityLockMinutes:     s.InactivityLockMinutes,
		LoadTestRunMaxAgeDays:     s.LoadTestRunMaxAgeDays,
		LoadTestRunMaxRowsPerItem: s.LoadTestRunMaxRowsPerItem,
		CORS:                      corsBodyFrom(s.CORS),
	}
}

func (h *SettingsHandler) get(w http.ResponseWriter, r *http.Request) {
	s, err := h.store.Get()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, settingsBodyFrom(s))
}

func (h *SettingsHandler) put(w http.ResponseWriter, r *http.Request) {
	var body settingsBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.HitLogMaxAgeDays <= 0 || body.HitLogMaxRowsPerMock <= 0 || body.MaxVersionsPerMock <= 0 || body.MaxCapturedBodyBytes <= 0 || body.SessionTimeoutMinutes <= 0 ||
		body.LoadTestRunMaxAgeDays <= 0 || body.LoadTestRunMaxRowsPerItem <= 0 {
		writeErr(w, http.StatusBadRequest, errors.New("hitLogMaxAgeDays, hitLogMaxRowsPerMock, maxVersionsPerMock, maxCapturedBodyBytes, sessionTimeoutMinutes, loadTestRunMaxAgeDays, and loadTestRunMaxRowsPerItem must all be positive"))
		return
	}
	if body.DefaultResponseDelayMs < 0 || body.DefaultFailureRatePercent < 0 || body.DefaultFailureRatePercent > 100 {
		writeErr(w, http.StatusBadRequest, errors.New("defaultResponseDelayMs must be non-negative and defaultFailureRatePercent must be 0-100"))
		return
	}
	if body.InactivityLockMinutes < 0 {
		writeErr(w, http.StatusBadRequest, errors.New("inactivityLockMinutes must be 0 (off) or positive"))
		return
	}

	if body.CORS != nil && body.CORS.MaxAgeSecs < 0 {
		writeErr(w, http.StatusBadRequest, errors.New("cors.maxAgeSecs must not be negative"))
		return
	}
	// Keep the stored CORS settings when the client did not send any.
	cors := settings.DefaultCORS()
	if body.CORS != nil {
		cors = body.CORS.settings()
	} else if existing, err := h.store.Get(); err == nil {
		cors = existing.CORS
	}

	s := &settings.Settings{
		CORS:                      cors,
		HitLogMaxAgeDays:          body.HitLogMaxAgeDays,
		HitLogMaxRowsPerMock:      body.HitLogMaxRowsPerMock,
		MaxVersionsPerMock:        body.MaxVersionsPerMock,
		RedactedHeaders:           body.RedactedHeaders,
		DefaultResponseDelayMs:    body.DefaultResponseDelayMs,
		DefaultFailureRatePercent: body.DefaultFailureRatePercent,
		MaxCapturedBodyBytes:      body.MaxCapturedBodyBytes,
		SessionTimeoutMinutes:     body.SessionTimeoutMinutes,
		InactivityLockMinutes:     body.InactivityLockMinutes,
		LoadTestRunMaxAgeDays:     body.LoadTestRunMaxAgeDays,
		LoadTestRunMaxRowsPerItem: body.LoadTestRunMaxRowsPerItem,
	}
	if err := h.store.Save(s); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if h.httpEngine != nil {
		h.httpEngine.SetRedactedHeaders(s.RedactedHeaders)
		h.httpEngine.SetBodyCaptureLimit(s.MaxCapturedBodyBytes)
		h.httpEngine.SetCORS(EngineCORS(s.CORS))
	}
	if h.sessionTimeout != nil {
		h.sessionTimeout.SetSessionTimeout(time.Duration(s.SessionTimeoutMinutes) * time.Minute)
	}
	if h.versionPruner != nil {
		// Best-effort: the setting itself already saved successfully above
		// regardless of what happens here, and any mock this misses will
		// still get pruned to the new limit the next time it's edited (see
		// snapshotVersion) — so a failure here is logged, not surfaced as
		// the PUT itself failing.
		if err := h.versionPruner.EnforceMaxVersionsForAllMocks(s.MaxVersionsPerMock); err != nil {
			log.Printf("airmock: failed to immediately enforce the new max versions per mock: %v", err)
		}
	}
	writeJSON(w, http.StatusOK, settingsBodyFrom(s))
}
