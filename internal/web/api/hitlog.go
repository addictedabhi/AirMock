package api

import (
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"nhooyr.io/websocket"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
)

type HitLogHandler struct {
	store       *hitlog.Store
	mockStore   *mock.Store
	broadcaster *hitlog.Broadcaster
}

func NewHitLogHandler(store *hitlog.Store, mockStore *mock.Store, broadcaster *hitlog.Broadcaster) *HitLogHandler {
	return &HitLogHandler{store: store, mockStore: mockStore, broadcaster: broadcaster}
}

func (h *HitLogHandler) Routes(r chi.Router) {
	r.Get("/", h.list)
	r.Delete("/", h.deleteMatching)
	r.Get("/tail", h.tail)
	r.Get("/export", h.export)
	r.Get("/{id}", h.get)
	r.Delete("/{id}", h.delete)
	r.Post("/{id}/promote-to-mock", h.promoteToMock)
}

// parseQueryOptions reads the filter bar params a log viewer needs — mockId,
// a created_at since/until range (RFC3339), and limit/offset pagination —
// shared between list (small pages for on-screen viewing) and export
// (everything matching the filter, up to a much higher cap).
func parseQueryOptions(q url.Values, defaultLimit int) hitlog.QueryOptions {
	opts := hitlog.QueryOptions{MockID: q.Get("mockId")}
	if v := q.Get("since"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.Since = t
		}
	}
	if v := q.Get("until"); v != "" {
		if t, err := time.Parse(time.RFC3339, v); err == nil {
			opts.Until = t
		}
	}
	opts.ProtocolType = q.Get("protocolType")
	opts.Direction = q.Get("direction")
	opts.Method = q.Get("method")
	opts.StatusClass = q.Get("statusClass")
	opts.Search = q.Get("search")
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.Limit = n
		}
	}
	if v := q.Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			opts.Offset = n
		}
	}
	if opts.Limit == 0 {
		opts.Limit = defaultLimit
	}
	return opts
}

// list supports the filter bar a log viewer needs: mockId, a created_at
// since/until range (RFC3339), and limit/offset pagination.
func (h *HitLogHandler) list(w http.ResponseWriter, r *http.Request) {
	entries, err := h.store.Query(parseQueryOptions(r.URL.Query(), 200))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// hitLogExportLimitCap bounds how much a single export can pull into memory
// at once — high enough that "export everything matching my filter" works
// for realistic hit-log sizes, low enough that a forgotten/very broad filter
// can't make one request try to serialize the entire table.
const hitLogExportLimitCap = 20000

// clampExportLimit bounds a requested limit at hitLogExportLimitCap — split
// out as its own pure function (rather than inlined in export) so the
// clamp itself is directly unit-testable without needing to seed 20000+
// rows into a store just to observe truncation, mirroring how
// apiclient.clampLoadTestConfig is tested directly.
func clampExportLimit(limit int) int {
	if limit > hitLogExportLimitCap {
		return hitLogExportLimitCap
	}
	return limit
}

// export mirrors list's own filter bar but returns a downloadable file
// (CSV by default, or JSON via ?format=json) covering every matching entry
// up to hitLogExportLimitCap, not just the page currently on screen.
func (h *HitLogHandler) export(w http.ResponseWriter, r *http.Request) {
	opts := parseQueryOptions(r.URL.Query(), 5000)
	opts.Limit = clampExportLimit(opts.Limit)

	entries, err := h.store.Query(opts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", `attachment; filename="airmock-hitlog-export.json"`)
		json.NewEncoder(w).Encode(entries)
		return
	}
	writeHitLogCSV(w, entries)
}

var hitLogCSVHeader = []string{
	"id", "mockId", "protocolType", "direction", "method", "path", "targetUrl",
	"responseStatus", "latencyMs", "createdAt", "requestHeaders", "requestBody",
	"responseHeaders", "responseBody",
}

// csvFormulaGuard neutralizes CSV/formula injection: encoding/csv correctly
// escapes delimiters/quotes/newlines, but a cell whose content is
// attacker-controlled (here, Path/TargetURL/Method/RequestBody/ResponseBody
// all come from whatever hit the mock, or whatever the API client sent)
// and starts with =, +, -, or @ is interpreted as a formula by Excel/
// Google Sheets when the exported file is opened — e.g. a request body of
// `=cmd|'/c calc'!A1`. Prefixing such a cell with a single quote is the
// standard mitigation: spreadsheet software then treats the cell as forced
// plain text instead of evaluating it, while every other consumer of the
// CSV (including re-parsing it as JSON's RequestBody/ResponseBody data)
// sees the harmless leading quote as just another character.
func csvFormulaGuard(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case '=', '+', '-', '@':
		return "'" + s
	}
	return s
}

func writeHitLogCSV(w http.ResponseWriter, entries []*hitlog.Entry) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", `attachment; filename="airmock-hitlog-export.csv"`)

	cw := csv.NewWriter(w)
	cw.Write(hitLogCSVHeader)
	for _, e := range entries {
		reqHeaders, _ := json.Marshal(e.RequestHeaders)
		respHeaders, _ := json.Marshal(e.ResponseHeaders)
		cw.Write([]string{
			e.ID, e.MockID, e.ProtocolType, e.Direction, csvFormulaGuard(e.Method), csvFormulaGuard(e.Path), csvFormulaGuard(e.TargetURL),
			strconv.Itoa(e.ResponseStatus), strconv.FormatInt(e.LatencyMs, 10), e.CreatedAt.Format(time.RFC3339),
			string(reqHeaders), csvFormulaGuard(e.RequestBody), string(respHeaders), csvFormulaGuard(e.ResponseBody),
		})
	}
	cw.Flush()
}

// tail streams newly-recorded entries over a WebSocket as they happen — the
// "live" in live tail. Best-effort: a slow/disconnected client just stops
// receiving (see hitlog.Broadcaster.Publish), it never blocks recording.
func (h *HitLogHandler) tail(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, nil)
	if err != nil {
		return
	}
	defer conn.Close(websocket.StatusNormalClosure, "")

	ch, cancel := h.broadcaster.Subscribe()
	defer cancel()

	ctx := r.Context()
	for {
		select {
		case <-ctx.Done():
			return
		case entry, ok := <-ch:
			if !ok {
				return
			}
			data, err := json.Marshal(entry)
			if err != nil {
				continue
			}
			if err := conn.Write(ctx, websocket.MessageText, data); err != nil {
				return
			}
		}
	}
}

// delete removes a single log entry — the per-row counterpart to
// deleteMatching's bulk, filter-driven delete.
func (h *HitLogHandler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, hitlog.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// deleteMatching bulk-deletes every entry matching the same filter bar as
// list/export (mockId, since/until, protocol/direction/method/statusClass,
// search) — deliberately ignoring limit/offset, since "delete everything
// matching my filters" should never leave a filtered set half-purged just
// because the on-screen page was capped at, say, 200 rows.
func (h *HitLogHandler) deleteMatching(w http.ResponseWriter, r *http.Request) {
	opts := parseQueryOptions(r.URL.Query(), 0)
	n, err := h.store.DeleteWhere(opts)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]int64{"deleted": n})
}

func (h *HitLogHandler) get(w http.ResponseWriter, r *http.Request) {
	e, err := h.store.Get(chi.URLParam(r, "id"))
	if errors.Is(err, hitlog.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

// hopByHopHeaders must never be replayed verbatim from a captured
// response: they describe the original transport (its exact body length,
// connection handling), not the mock's own, which may render a
// differently-sized body from a BodyTemplate.
var hopByHopHeaders = map[string]bool{
	"Content-Length":    true,
	"Transfer-Encoding": true,
	"Connection":        true,
	"Date":              true,
	"Content-Encoding":  true,
}

// promoteToMock turns a captured proxy-capture hit into a real, editable
// sync mock reproducing exactly what the real upstream returned —
// "point this at the real API for a bit" becomes an instant mock.
func (h *HitLogHandler) promoteToMock(w http.ResponseWriter, r *http.Request) {
	entry, err := h.store.Get(chi.URLParam(r, "id"))
	if errors.Is(err, hitlog.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	headers := map[string]string{}
	for k, v := range entry.ResponseHeaders {
		if !hopByHopHeaders[http.CanonicalHeaderKey(k)] {
			headers[k] = v
		}
	}

	def := &mock.Definition{
		Name:         "Promoted: " + entry.Method + " " + entry.Path,
		ProtocolType: "rest",
		Method:       entry.Method,
		PathPattern:  entry.Path,
		Enabled:      true,
		Mode:         "sync",
		Response: mock.ResponseTemplate{
			StatusCode:   entry.ResponseStatus,
			Headers:      headers,
			BodyTemplate: entry.ResponseBody,
		},
	}

	// Unlike create/update/import, this handler builds its own Definition
	// straight from a captured hit-log entry rather than a caller-supplied
	// one — validateMockShape must still run before it's persisted, or
	// promoting a non-REST hit (a TCP/SMTP/FTP inbound entry has no Method
	// at all, or one with a nonstandard Method) produces a "rest" mock with
	// an invalid Method that panics chi's router the moment it's dispatched
	// (now, and again on every future server restart via loadExistingMocks).
	if err := validateMockShape(def); err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("this hit can't be promoted to a mock: %w", err))
		return
	}

	created, err := h.mockStore.Create(def)
	if errors.Is(err, mock.ErrDuplicateEndpoint) {
		writeErr(w, http.StatusConflict, fmt.Errorf("a mock for %s %s already exists, so this hit can't be promoted: edit or delete the existing mock first", def.Method, def.PathPattern))
		return
	}
	if errors.Is(err, mock.ErrDuplicateName) {
		writeErr(w, http.StatusConflict, fmt.Errorf("a mock named %q already exists (this hit may already have been promoted): rename or delete it first", def.Name))
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := engine.Dispatch(created); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}
