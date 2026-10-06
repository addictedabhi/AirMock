package api

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	httpengine "github.com/addictedabhi/airmock/internal/engine/http"
	"github.com/addictedabhi/airmock/internal/settings"
	"github.com/addictedabhi/airmock/internal/storage"
)

// fakeRedactedHeaderSetter records every call so a test can assert the
// handler actually applies a saved list live, not just persists it. Also
// implements BodyCaptureLimitSetter (recording that call too) since
// SettingsHandler now needs both from one httpEngine value.
type fakeRedactedHeaderSetter struct {
	lastCall          []string
	calls             int
	lastBodyLimitCall int
	bodyLimitCalls    int
	lastCORS          httpengine.CORSConfig
	corsCalls         int
}

func (f *fakeRedactedHeaderSetter) SetRedactedHeaders(keys []string) {
	f.lastCall = keys
	f.calls++
}

func (f *fakeRedactedHeaderSetter) SetCORS(c httpengine.CORSConfig) {
	f.lastCORS = c
	f.corsCalls++
}

func (f *fakeRedactedHeaderSetter) SetBodyCaptureLimit(limitBytes int) {
	f.lastBodyLimitCall = limitBytes
	f.bodyLimitCalls++
}

// fakeVersionPruner records every call so a test can assert the handler
// immediately enforces a newly-saved MaxVersionsPerMock, not just persists
// the setting.
type fakeVersionPruner struct {
	lastMax int
	calls   int
}

func (f *fakeVersionPruner) EnforceMaxVersionsForAllMocks(maxVersions int) error {
	f.lastMax = maxVersions
	f.calls++
	return nil
}

// fakeSessionTimeoutSetter records every call so a test can assert the
// handler actually applies a saved session timeout live, same reasoning as
// fakeRedactedHeaderSetter above.
type fakeSessionTimeoutSetter struct {
	lastCall time.Duration
	calls    int
}

func (f *fakeSessionTimeoutSetter) SetSessionTimeout(d time.Duration) {
	f.lastCall = d
	f.calls++
}

func newTestSettingsRouter(t *testing.T) (chi.Router, *fakeRedactedHeaderSetter) {
	t.Helper()
	r, fake, _ := newTestSettingsRouterWithVersionPruner(t)
	return r, fake
}

func newTestSettingsRouterWithVersionPruner(t *testing.T) (chi.Router, *fakeRedactedHeaderSetter, *fakeVersionPruner) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	fake := &fakeRedactedHeaderSetter{}
	pruner := &fakeVersionPruner{}
	r := chi.NewRouter()
	r.Route("/api/settings", NewSettingsHandler(settings.NewStore(db), fake, pruner, &fakeSessionTimeoutSetter{}).Routes)
	return r, fake, pruner
}

func TestSettingsGetReturnsDefaults(t *testing.T) {
	r, _ := newTestSettingsRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/settings/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var body settingsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.HitLogMaxAgeDays != settings.DefaultHitLogMaxAgeDays || body.HitLogMaxRowsPerMock != settings.DefaultHitLogMaxRowsPerMock {
		t.Fatalf("expected defaults, got %+v", body)
	}
	if body.MaxVersionsPerMock != settings.DefaultMaxVersionsPerMock {
		t.Fatalf("expected the default max versions per mock, got %+v", body)
	}
	if len(body.RedactedHeaders) != len(settings.DefaultRedactedHeaders) {
		t.Fatalf("expected default redacted headers, got %+v", body.RedactedHeaders)
	}
}

func TestSettingsPutValidatesAndPersists(t *testing.T) {
	r, _ := newTestSettingsRouter(t)

	badReq := httptest.NewRequest(http.MethodPut, "/api/settings/", bytes.NewBufferString(`{"hitLogMaxAgeDays":0,"hitLogMaxRowsPerMock":100,"maxVersionsPerMock":20}`))
	badRec := httptest.NewRecorder()
	r.ServeHTTP(badRec, badReq)
	if badRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a zero max age, got %d", badRec.Code)
	}

	badVersionsReq := httptest.NewRequest(http.MethodPut, "/api/settings/", bytes.NewBufferString(`{"hitLogMaxAgeDays":7,"hitLogMaxRowsPerMock":500,"maxVersionsPerMock":0,"maxCapturedBodyBytes":65536}`))
	badVersionsRec := httptest.NewRecorder()
	r.ServeHTTP(badVersionsRec, badVersionsReq)
	if badVersionsRec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a zero max versions per mock, got %d", badVersionsRec.Code)
	}

	goodReq := httptest.NewRequest(http.MethodPut, "/api/settings/", bytes.NewBufferString(`{"hitLogMaxAgeDays":7,"hitLogMaxRowsPerMock":500,"maxVersionsPerMock":5,"maxCapturedBodyBytes":65536,"sessionTimeoutMinutes":24,"loadTestRunMaxAgeDays":30,"loadTestRunMaxRowsPerItem":50}`))
	goodRec := httptest.NewRecorder()
	r.ServeHTTP(goodRec, goodReq)
	if goodRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", goodRec.Code, goodRec.Body.String())
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/settings/", nil)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	var body settingsBody
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.HitLogMaxAgeDays != 7 || body.HitLogMaxRowsPerMock != 500 || body.MaxVersionsPerMock != 5 {
		t.Fatalf("expected the PUT to persist, got %+v", body)
	}
}

func TestSettingsPutAppliesRedactedHeadersLiveAndPersists(t *testing.T) {
	r, fake := newTestSettingsRouter(t)

	putReq := httptest.NewRequest(http.MethodPut, "/api/settings/", bytes.NewBufferString(
		`{"hitLogMaxAgeDays":30,"hitLogMaxRowsPerMock":10000,"maxVersionsPerMock":20,"maxCapturedBodyBytes":65536,"sessionTimeoutMinutes":24,"loadTestRunMaxAgeDays":30,"loadTestRunMaxRowsPerItem":50,"redactedHeaders":["Authorization","X-Custom-Secret"]}`))
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", putRec.Code, putRec.Body.String())
	}

	if fake.calls != 1 {
		t.Fatalf("expected SetRedactedHeaders to be called exactly once, got %d", fake.calls)
	}
	if len(fake.lastCall) != 2 || fake.lastCall[0] != "Authorization" || fake.lastCall[1] != "X-Custom-Secret" {
		t.Fatalf("expected the live engine to receive the new list, got %+v", fake.lastCall)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/settings/", nil)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	var body settingsBody
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(body.RedactedHeaders) != 2 || body.RedactedHeaders[1] != "X-Custom-Secret" {
		t.Fatalf("expected the redacted headers to persist, got %+v", body.RedactedHeaders)
	}
}

// TestSettingsPutEnforcesMaxVersionsImmediately covers a real requirement:
// lowering MaxVersionsPerMock must trim every mock's ALREADY-STORED version
// history right away, not just apply on that mock's next edit.
func TestSettingsPutEnforcesMaxVersionsImmediately(t *testing.T) {
	r, _, pruner := newTestSettingsRouterWithVersionPruner(t)

	putReq := httptest.NewRequest(http.MethodPut, "/api/settings/", bytes.NewBufferString(
		`{"hitLogMaxAgeDays":30,"hitLogMaxRowsPerMock":10000,"maxVersionsPerMock":5,"maxCapturedBodyBytes":65536,"sessionTimeoutMinutes":24,"loadTestRunMaxAgeDays":30,"loadTestRunMaxRowsPerItem":50,"redactedHeaders":[]}`))
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", putRec.Code, putRec.Body.String())
	}

	if pruner.calls != 1 {
		t.Fatalf("expected EnforceMaxVersionsForAllMocks to be called exactly once, got %d", pruner.calls)
	}
	if pruner.lastMax != 5 {
		t.Fatalf("expected the newly-saved limit of 5 to be enforced, got %d", pruner.lastMax)
	}
}

// TestSettingsGetReturnsNewDefaults guards the three fields added for the
// "easy settings" pass: DefaultResponseDelayMs/DefaultFailureRatePercent
// (pre-fill a brand-new mock's own fields) and MaxCapturedBodyBytes (hit
// log response-body capture cap) — all three must come back with sensible
// defaults on a fresh instance, matching settings.Store.Get's own fallback.
func TestSettingsGetReturnsNewDefaults(t *testing.T) {
	r, _ := newTestSettingsRouter(t)

	req := httptest.NewRequest(http.MethodGet, "/api/settings/", nil)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	var body settingsBody
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.DefaultResponseDelayMs != 0 {
		t.Errorf("expected a default response delay of 0, got %d", body.DefaultResponseDelayMs)
	}
	if body.DefaultFailureRatePercent != 0 {
		t.Errorf("expected a default failure rate of 0, got %v", body.DefaultFailureRatePercent)
	}
	if body.MaxCapturedBodyBytes != settings.DefaultMaxCapturedBodyBytes {
		t.Errorf("expected the default max captured body bytes, got %d", body.MaxCapturedBodyBytes)
	}
}

// TestSettingsPutPersistsAndAppliesNewDefaultsLive covers both the
// persistence and the live SetBodyCaptureLimit application — mirroring
// TestSettingsPutAppliesRedactedHeadersLiveAndPersists's own pattern for
// SetRedactedHeaders.
func TestSettingsPutPersistsAndAppliesNewDefaultsLive(t *testing.T) {
	r, fake := newTestSettingsRouter(t)

	putReq := httptest.NewRequest(http.MethodPut, "/api/settings/", bytes.NewBufferString(
		`{"hitLogMaxAgeDays":30,"hitLogMaxRowsPerMock":10000,"maxVersionsPerMock":20,"maxCapturedBodyBytes":4096,"sessionTimeoutMinutes":24,"loadTestRunMaxAgeDays":30,"loadTestRunMaxRowsPerItem":50,"defaultResponseDelayMs":250,"defaultFailureRatePercent":12.5}`))
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", putRec.Code, putRec.Body.String())
	}

	if fake.bodyLimitCalls != 1 || fake.lastBodyLimitCall != 4096 {
		t.Fatalf("expected SetBodyCaptureLimit(4096) exactly once, got calls=%d last=%d", fake.bodyLimitCalls, fake.lastBodyLimitCall)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/settings/", nil)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	var body settingsBody
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.MaxCapturedBodyBytes != 4096 || body.DefaultResponseDelayMs != 250 || body.DefaultFailureRatePercent != 12.5 {
		t.Fatalf("expected all three new fields to persist, got %+v", body)
	}
}

func TestSettingsPutAppliesSessionTimeoutLiveAndPersists(t *testing.T) {
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	sessionSetter := &fakeSessionTimeoutSetter{}
	r := chi.NewRouter()
	r.Route("/api/settings", NewSettingsHandler(settings.NewStore(db), &fakeRedactedHeaderSetter{}, &fakeVersionPruner{}, sessionSetter).Routes)

	putReq := httptest.NewRequest(http.MethodPut, "/api/settings/", bytes.NewBufferString(
		`{"hitLogMaxAgeDays":30,"hitLogMaxRowsPerMock":10000,"maxVersionsPerMock":20,"maxCapturedBodyBytes":65536,"sessionTimeoutMinutes":480,"loadTestRunMaxAgeDays":30,"loadTestRunMaxRowsPerItem":50}`))
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", putRec.Code, putRec.Body.String())
	}
	if sessionSetter.calls != 1 || sessionSetter.lastCall != 480*time.Minute {
		t.Fatalf("expected SetSessionTimeout(480m) exactly once, got calls=%d last=%v", sessionSetter.calls, sessionSetter.lastCall)
	}

	getReq := httptest.NewRequest(http.MethodGet, "/api/settings/", nil)
	getRec := httptest.NewRecorder()
	r.ServeHTTP(getRec, getReq)
	var body settingsBody
	if err := json.Unmarshal(getRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.SessionTimeoutMinutes != 480 {
		t.Fatalf("expected sessionTimeoutMinutes to persist, got %+v", body)
	}
}

// TestSettingsPutInactivityLockMinutesRoundTripsAndAllowsZero covers the
// field driving the frontend's inactivity auto-lock (ui/src/lib/
// inactivityWatcher.js) — a SEPARATE setting from SessionTimeoutMinutes
// above, where (unlike that field) 0 is a valid, meaningful "off" value
// rather than something rejected or falling back to a default.
func TestSettingsPutInactivityLockMinutesRoundTripsAndAllowsZero(t *testing.T) {
	r, _ := newTestSettingsRouter(t)

	putReq := httptest.NewRequest(http.MethodPut, "/api/settings/", bytes.NewBufferString(
		`{"hitLogMaxAgeDays":30,"hitLogMaxRowsPerMock":10000,"maxVersionsPerMock":20,"maxCapturedBodyBytes":65536,"sessionTimeoutMinutes":1440,"loadTestRunMaxAgeDays":30,"loadTestRunMaxRowsPerItem":50,"inactivityLockMinutes":5}`))
	putRec := httptest.NewRecorder()
	r.ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", putRec.Code, putRec.Body.String())
	}
	var body settingsBody
	if err := json.Unmarshal(putRec.Body.Bytes(), &body); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if body.InactivityLockMinutes != 5 {
		t.Fatalf("expected inactivityLockMinutes to persist, got %+v", body)
	}

	// 0 (off) must be accepted, not rejected the way a zero
	// sessionTimeoutMinutes would be.
	offReq := httptest.NewRequest(http.MethodPut, "/api/settings/", bytes.NewBufferString(
		`{"hitLogMaxAgeDays":30,"hitLogMaxRowsPerMock":10000,"maxVersionsPerMock":20,"maxCapturedBodyBytes":65536,"sessionTimeoutMinutes":1440,"loadTestRunMaxAgeDays":30,"loadTestRunMaxRowsPerItem":50,"inactivityLockMinutes":0}`))
	offRec := httptest.NewRecorder()
	r.ServeHTTP(offRec, offReq)
	if offRec.Code != http.StatusOK {
		t.Fatalf("expected 200 for inactivityLockMinutes=0, got %d: %s", offRec.Code, offRec.Body.String())
	}
}

func TestSettingsPutRejectsNegativeInactivityLockMinutes(t *testing.T) {
	r, _ := newTestSettingsRouter(t)
	rec := doJSON(t, r, http.MethodPut, "/api/settings/", map[string]any{
		"hitLogMaxAgeDays": 30, "hitLogMaxRowsPerMock": 10000, "maxVersionsPerMock": 20,
		"maxCapturedBodyBytes": 65536, "sessionTimeoutMinutes": 1440, "inactivityLockMinutes": -1,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a negative inactivityLockMinutes, got %d", rec.Code)
	}
}

// TestSettingsPutRejectsInvalidDefaults guards the new validation added
// alongside these fields: a negative delay or an out-of-range failure
// rate is rejected rather than silently saved.
func TestSettingsPutRejectsInvalidDefaults(t *testing.T) {
	r, _ := newTestSettingsRouter(t)

	cases := []string{
		`{"hitLogMaxAgeDays":30,"hitLogMaxRowsPerMock":10000,"maxVersionsPerMock":20,"maxCapturedBodyBytes":4096,"defaultResponseDelayMs":-1}`,
		`{"hitLogMaxAgeDays":30,"hitLogMaxRowsPerMock":10000,"maxVersionsPerMock":20,"maxCapturedBodyBytes":4096,"defaultFailureRatePercent":101}`,
		`{"hitLogMaxAgeDays":30,"hitLogMaxRowsPerMock":10000,"maxVersionsPerMock":20,"maxCapturedBodyBytes":4096,"defaultFailureRatePercent":-5}`,
	}
	for _, body := range cases {
		req := httptest.NewRequest(http.MethodPut, "/api/settings/", bytes.NewBufferString(body))
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, req)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("expected 400 for body %s, got %d: %s", body, rec.Code, rec.Body.String())
		}
	}
}

func putSettings(t *testing.T, r chi.Router, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	b, _ := json.Marshal(body)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPut, "/api/settings/", bytes.NewReader(b)))
	return rec
}

func baseSettingsBody() map[string]any {
	return map[string]any{
		"hitLogMaxAgeDays": 30, "hitLogMaxRowsPerMock": 1000, "maxVersionsPerMock": 20, "redactedHeaders": []string{},
		"maxCapturedBodyBytes": 65536, "sessionTimeoutMinutes": 60, "loadTestRunMaxAgeDays": 30, "loadTestRunMaxRowsPerItem": 50,
	}
}

func TestSettingsCORSDefaultsToPermissiveAndIsOn(t *testing.T) {
	r, _ := newTestSettingsRouter(t)
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings/", nil))
	var body settingsBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.CORS == nil || !body.CORS.Enabled || body.CORS.AllowOrigin != "*" || body.CORS.AllowHeaders != "*" || body.CORS.MaxAgeSecs != 600 {
		t.Fatalf("expected permissive CORS defaults, got %+v", body.CORS)
	}
}

func TestSettingsPutAppliesCORSLiveAndOmittingItKeepsTheStoredValue(t *testing.T) {
	r, fake := newTestSettingsRouter(t)

	b := baseSettingsBody()
	b["cors"] = map[string]any{"enabled": true, "allowOrigin": "https://app.example", "allowMethods": "GET, POST", "allowHeaders": "X-Token", "allowCredentials": true, "maxAgeSecs": 120}
	if rec := putSettings(t, r, b); rec.Code != http.StatusOK {
		t.Fatalf("put: %d %s", rec.Code, rec.Body.String())
	}
	if fake.corsCalls != 1 || fake.lastCORS.AllowOrigin != "https://app.example" || !fake.lastCORS.AllowCredentials || fake.lastCORS.MaxAgeSecs != 120 {
		t.Fatalf("CORS should be applied live, got calls=%d %+v", fake.corsCalls, fake.lastCORS)
	}

	// An older client that sends no "cors" key must not reset it.
	if rec := putSettings(t, r, baseSettingsBody()); rec.Code != http.StatusOK {
		t.Fatalf("put without cors: %d %s", rec.Code, rec.Body.String())
	}
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/settings/", nil))
	var body settingsBody
	_ = json.Unmarshal(rec.Body.Bytes(), &body)
	if body.CORS == nil || body.CORS.AllowOrigin != "https://app.example" || !body.CORS.Enabled {
		t.Fatalf("omitting cors must keep the stored settings, got %+v", body.CORS)
	}
}

func TestSettingsRejectsCredentialedWildcardOrigin(t *testing.T) {
	r, _ := newTestSettingsRouter(t)
	b := baseSettingsBody()
	b["cors"] = map[string]any{"enabled": true, "allowOrigin": "*", "allowMethods": "GET", "allowHeaders": "*", "allowCredentials": true, "maxAgeSecs": 60}
	// A wildcard with credentials is accepted: the gateway echoes the request
	// origin instead. Only a negative max age is invalid.
	if rec := putSettings(t, r, b); rec.Code != http.StatusOK {
		t.Fatalf("wildcard + credentials should be accepted (origin is echoed), got %d: %s", rec.Code, rec.Body.String())
	}
	b["cors"] = map[string]any{"enabled": true, "allowOrigin": "*", "allowMethods": "GET", "allowHeaders": "*", "maxAgeSecs": -5}
	if rec := putSettings(t, r, b); rec.Code != http.StatusBadRequest {
		t.Fatalf("a negative maxAgeSecs must be rejected, got %d", rec.Code)
	}
}
