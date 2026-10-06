package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
)

const mockExportFilename = "airmock-mocks-export.json"

// WorkspaceLockChecker is the narrow slice of apiclient.Store MocksHandler
// needs to enforce workspace locks — just whether a workspace currently has
// one at all, nothing about the lock's own credential/hash.
type WorkspaceLockChecker interface {
	IsWorkspaceLocked(id string) (bool, error)
}

// WorkspaceUnlockTracker is the narrow slice of wslock.Tracker MocksHandler
// needs — whether the calling browser has already unlocked a given
// workspace this session.
type WorkspaceUnlockTracker interface {
	IsUnlocked(clientID, workspaceID string) bool
}

type MocksHandler struct {
	store           *mock.Store
	wsLockChecker   WorkspaceLockChecker
	wsUnlockTracker WorkspaceUnlockTracker
}

func NewMocksHandler(store *mock.Store, wsLockChecker WorkspaceLockChecker, wsUnlockTracker WorkspaceUnlockTracker) *MocksHandler {
	return &MocksHandler{store: store, wsLockChecker: wsLockChecker, wsUnlockTracker: wsUnlockTracker}
}

// checkWorkspaceUnlocked writes a 403 (and returns false) if any of
// workspaceIDs is currently locked and the calling browser (identified by
// its client-id cookie, if any — see clientIDIfPresent) hasn't already
// unlocked it. Duplicate/empty ids are ignored. An id that no longer
// resolves to a real workspace (e.g. deleted since) has nothing to enforce,
// so it's skipped rather than failing the whole request over stale
// metadata — the response shape ({"code":"workspace_locked", ...}) is
// distinguishable from a generic error so the frontend can specifically
// catch it and prompt for that workspace's password, then retry.
func (h *MocksHandler) checkWorkspaceUnlocked(w http.ResponseWriter, r *http.Request, workspaceIDs ...string) bool {
	return checkWorkspaceUnlocked(w, r, h.wsLockChecker, h.wsUnlockTracker, workspaceIDs...)
}

// checkWorkspaceUnlocked is the package-level gate both MocksHandler and
// MockProjectsHandler enforce against — factored out once the project
// itself (not just individual mocks) also became a lockable-workspace-
// mappable resource, so the two handlers don't drift from each other.
func checkWorkspaceUnlocked(w http.ResponseWriter, r *http.Request, checker WorkspaceLockChecker, tracker WorkspaceUnlockTracker, workspaceIDs ...string) bool {
	id := firstLockedWorkspace(checker, tracker, clientIDIfPresent(r), workspaceIDs...)
	if id == "" {
		return true
	}
	writeJSON(w, http.StatusForbidden, map[string]string{
		"error":       "workspace locked",
		"code":        "workspace_locked",
		"workspaceId": id,
	})
	return false
}

// firstLockedWorkspace is checkWorkspaceUnlocked's underlying check,
// without the http.ResponseWriter side effect — needed by call sites
// (bulkAction below) that process several items in one request and must
// report a single locked item as that one item's own failure rather than
// aborting the whole response the way writing straight to w would.
// Returns the first of workspaceIDs that's currently locked and not
// unlocked for clientID, or "" if none are.
func firstLockedWorkspace(checker WorkspaceLockChecker, tracker WorkspaceUnlockTracker, clientID string, workspaceIDs ...string) string {
	seen := make(map[string]bool, len(workspaceIDs))
	for _, id := range workspaceIDs {
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		locked, err := checker.IsWorkspaceLocked(id)
		if err != nil {
			continue
		}
		if locked && !tracker.IsUnlocked(clientID, id) {
			return id
		}
	}
	return ""
}

// projectWorkspaceID resolves projectID's mapped workspace, if any — ""
// for an empty or no-longer-resolvable projectID, the same fail-open
// convention store.GatewayPortForProject/TLSConfigForProject already use
// for a stale project reference.
func (h *MocksHandler) projectWorkspaceID(projectID string) string {
	if projectID == "" {
		return ""
	}
	p, err := h.store.GetProject(projectID)
	if err != nil {
		return ""
	}
	return p.WorkspaceID
}

func (h *MocksHandler) Routes(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Get("/export", h.exportAll)
	r.Post("/import", h.importAll)
	r.Post("/bulk", h.bulkAction)
	r.Get("/{id}", h.get)
	r.Put("/{id}", h.update)
	r.Delete("/{id}", h.delete)
	r.Get("/{id}/versions", h.listVersions)
	r.Post("/{id}/versions/{versionId}/restore", h.restoreVersion)
	r.Put("/{id}/csv-source", h.setCSVSource)
	r.Get("/{id}/csv-source", h.getCSVSource)
	r.Delete("/{id}/csv-source", h.deleteCSVSource)
}

// setCSVSource attaches (or replaces) a mock's CSV data source — a
// multipart upload (a "file" part plus a "mode" field, "round_robin" or
// "random") backing the {{csv "column"}} template function (see
// internal/mock/template.go). Replacing an existing attachment restarts its
// round-robin cursor from the top.
func (h *MocksHandler) setCSVSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	mode, content, err := parseCSVUpload(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	meta, err := h.store.SetCSVSource(id, mode, content)
	if err != nil {
		if errors.Is(err, mock.ErrEmptyCSV) || errors.Is(err, mock.ErrInvalidCSVMode) {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

// getCSVSource returns metadata only (mode, row count, column names) — the
// raw CSV content never round-trips back to the client once uploaded.
func (h *MocksHandler) getCSVSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := h.store.Get(id); errors.Is(err, mock.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	meta, err := h.store.GetCSVSourceMeta(id)
	if errors.Is(err, mock.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent) // exists, but no CSV attached
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (h *MocksHandler) deleteCSVSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.store.DeleteCSVSource(id); err != nil {
		if errors.Is(err, mock.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *MocksHandler) list(w http.ResponseWriter, r *http.Request) {
	defs, err := h.store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, defs)
}

func (h *MocksHandler) create(w http.ResponseWriter, r *http.Request) {
	var d mock.Definition
	if err := decodeStrict(r, &d); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if d.ProtocolType == "" {
		d.ProtocolType = "rest"
	}
	h.applyProjectBasePath(&d)
	if err := validateMockShape(&d); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !h.checkWorkspaceUnlocked(w, r, d.WorkspaceID, h.projectWorkspaceID(d.ProjectID)) {
		return
	}

	if isDryRun(r) {
		if err := h.store.CheckDuplicates(&d); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, dryRunReport{DryRun: true, Action: "create", Warnings: mockWarnings(&d)})
		return
	}

	created, err := h.store.Create(&d)
	if errors.Is(err, mock.ErrDuplicateName) || errors.Is(err, mock.ErrDuplicateEndpoint) {
		writeErr(w, http.StatusConflict, err)
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
	writeMockJSON(w, http.StatusCreated, created)
}

func (h *MocksHandler) get(w http.ResponseWriter, r *http.Request) {
	d, err := h.store.Get(chi.URLParam(r, "id"))
	if errors.Is(err, mock.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeMockJSON(w, http.StatusOK, d)
}

func (h *MocksHandler) update(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	var d mock.Definition
	if err := decodeStrict(r, &d); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	d.ID = id
	if d.ProtocolType == "" {
		d.ProtocolType = "rest" // same default as create, so a file that creates also updates
	}
	h.applyProjectBasePath(&d)
	if err := validateMockShape(&d); err != nil {
		// A mock that is being switched off never renders anything, so a
		// template that already doesn't parse must not stop it being disabled.
		var te *templateError
		if !(errors.As(err, &te) && !d.Enabled) {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}

	// Read the pre-update ProtocolType so a cross-engine change (e.g. tcp ->
	// rest) can be detected below — store.Update overwrites protocol_type
	// unconditionally, and engine.Dispatch(updated) only ever registers with
	// the NEW protocol's engine, so without this the OLD engine (a
	// completely different Engine instance, e.g. a live TCP listener) never
	// learns this ID no longer belongs to it and keeps serving under a
	// stale *mock.Definition forever. Also doubles as the "at risk"
	// workspace check below: both the mock's CURRENT workspace (being
	// changed away from or edited in place) and its INCOMING one (being
	// reassigned into) need to be unlocked, not just whichever one ends up
	// persisted.
	previous, prevErr := h.store.Get(id)
	if prevErr == nil {
		if !h.checkWorkspaceUnlocked(w, r, previous.WorkspaceID, d.WorkspaceID, h.projectWorkspaceID(previous.ProjectID), h.projectWorkspaceID(d.ProjectID)) {
			return
		}
	} else if !h.checkWorkspaceUnlocked(w, r, d.WorkspaceID, h.projectWorkspaceID(d.ProjectID)) {
		return
	}

	if prevErr == nil && mock.SameDefinition(previous, &d) {
		w.Header().Set("X-AirMock-Unchanged", "true")
		if isDryRun(r) {
			writeJSON(w, http.StatusOK, dryRunReport{DryRun: true, Action: "unchanged", Warnings: mockWarnings(&d)})
			return
		}
		writeMockJSON(w, http.StatusOK, previous)
		return
	}
	if isDryRun(r) {
		if prevErr != nil {
			writeErr(w, http.StatusNotFound, mock.ErrNotFound)
			return
		}
		if err := h.store.CheckDuplicates(&d); err != nil {
			writeErr(w, http.StatusConflict, err)
			return
		}
		writeJSON(w, http.StatusOK, dryRunReport{DryRun: true, Action: "update", Warnings: mockWarnings(&d)})
		return
	}

	updated, err := h.store.Update(&d)
	if errors.Is(err, mock.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, mock.ErrDuplicateName) || errors.Is(err, mock.ErrDuplicateEndpoint) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := engine.Dispatch(updated); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if prevErr == nil && engine.ProtocolFamily(previous.ProtocolType) != engine.ProtocolFamily(updated.ProtocolType) {
		if err := engine.DispatchUnregister(previous.ProtocolType, id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, withWarnings(updated))
}

func (h *MocksHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	d, err := h.store.Get(id)
	if errors.Is(err, mock.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if !h.checkWorkspaceUnlocked(w, r, d.WorkspaceID, h.projectWorkspaceID(d.ProjectID)) {
		return
	}
	if err := h.store.Delete(id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := engine.DispatchUnregister(d.ProtocolType, id); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// listVersions returns this mock's edit history, most recent first — each
// entry is a full snapshot of the mock as it looked immediately before the
// edit that superseded it (see mock.Store.snapshotVersion).
func (h *MocksHandler) listVersions(w http.ResponseWriter, r *http.Request) {
	versions, err := h.store.ListVersions(chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, versions)
}

// restoreVersion overwrites the mock with an old snapshot via the normal
// update path — engine.Dispatch re-registers it exactly like any other
// save, so a restored TCP/SMTP/MQTT/FTP mock's listener config takes effect
// immediately too, not just its REST/SOAP/GraphQL response.
func (h *MocksHandler) restoreVersion(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	// Same cross-engine cleanup update() needs (see its comment): a restored
	// snapshot can carry a different ProtocolType than the mock's current
	// live state (e.g. it was tcp when snapshotted, changed to rest since,
	// and is now being restored back to tcp — or the reverse), so the engine
	// currently serving it may not be the one the restored snapshot belongs to.
	previous, prevErr := h.store.Get(id)

	restored, err := h.store.RestoreVersion(id, chi.URLParam(r, "versionId"))
	if errors.Is(err, mock.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, mock.ErrDuplicateName) || errors.Is(err, mock.ErrDuplicateEndpoint) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if err := engine.Dispatch(restored); err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	if prevErr == nil && engine.ProtocolFamily(previous.ProtocolType) != engine.ProtocolFamily(restored.ProtocolType) {
		if err := engine.DispatchUnregister(previous.ProtocolType, id); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, restored)
}

// exportedMocks is the export/import interchange shape — wrapped in an
// object (rather than a bare array) so future export metadata (a format
// version, an export timestamp) can be added without breaking existing
// exported files. ProjectID is import-only (never populated by exportAll):
// when set, every imported mock is grouped under that project regardless of
// whatever ProjectID it originally carried, which also sidesteps importing
// a project ID that doesn't exist on this instance (from a different
// AirMock instance's export) — left as one of a project's own mocks
// forever, invisible, since the UI only renders a mock under a project ID
// it actually has loaded.
type exportedMocks struct {
	Mocks     []*mock.Definition `json:"mocks"`
	ProjectID string             `json:"projectId,omitempty"`
}

// exportAll dumps every mock (every protocol family — REST/SOAP/GraphQL/TCP/
// SMTP all share this one store) as a downloadable JSON file, for backup or
// for sharing a set of mocks with another AirMock instance.
func (h *MocksHandler) exportAll(w http.ResponseWriter, r *http.Request) {
	defs, err := h.store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="`+mockExportFilename+`"`)
	writeJSON(w, http.StatusOK, exportedMocks{Mocks: defs})
}

type importSkip struct {
	Name   string `json:"name"`
	Reason string `json:"reason"`
}

type importResult struct {
	Imported []string     `json:"imported"`
	Skipped  []importSkip `json:"skipped"`
}

// importAll creates a fresh mock from every entry in body.Mocks. Each
// imported definition always gets a brand new ID/timestamps — reusing the
// ID from an export would either collide with an existing mock on the same
// instance or silently impersonate one on another, neither of which is what
// "import" should mean. A definition that fails validation or collides on
// name/endpoint is skipped with a reason rather than aborting the whole
// batch, so importing 50 mocks where 2 already exist still imports the
// other 48.
func (h *MocksHandler) importAll(w http.ResponseWriter, r *http.Request) {
	var body exportedMocks
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	result := importResult{Imported: []string{}, Skipped: []importSkip{}}
	for _, d := range body.Mocks {
		d.ID = ""
		d.ProjectID = body.ProjectID
		if d.ProtocolType == "" {
			d.ProtocolType = "rest"
		}
		h.applyProjectBasePath(d)
		if err := validateMockShape(d); err != nil {
			result.Skipped = append(result.Skipped, importSkip{Name: d.Name, Reason: err.Error()})
			continue
		}
		created, err := h.store.Create(d)
		if err != nil {
			result.Skipped = append(result.Skipped, importSkip{Name: d.Name, Reason: err.Error()})
			continue
		}
		if created.Enabled {
			if err := engine.Dispatch(created); err != nil {
				result.Skipped = append(result.Skipped, importSkip{Name: created.Name, Reason: "created but failed to start: " + err.Error()})
				continue
			}
		}
		result.Imported = append(result.Imported, created.Name)
	}
	writeJSON(w, http.StatusOK, result)
}

type bulkActionBody struct {
	IDs    []string `json:"ids"`
	Action string   `json:"action"` // "enable" | "disable" | "delete" | "move"
	// ProjectID is only read for action "move" — the project to reassign
	// every selected mock to, or "" to move them to Ungrouped.
	ProjectID string `json:"projectId,omitempty"`
}

type bulkActionResult struct {
	Succeeded []string     `json:"succeeded"`
	Failed    []importSkip `json:"failed"`
}

// bulkAction applies one action to a set of mocks at once, reusing exactly
// the same store/engine calls the single-mock create/update/delete handlers
// use — so a bulk operation behaves identically to doing each one by hand,
// just without the round trips.
func (h *MocksHandler) bulkAction(w http.ResponseWriter, r *http.Request) {
	var body bulkActionBody
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.Action != "enable" && body.Action != "disable" && body.Action != "delete" && body.Action != "move" {
		writeErr(w, http.StatusBadRequest, errors.New(`action must be one of "enable", "disable", "delete", "move"`))
		return
	}

	clientID := clientIDIfPresent(r)
	result := bulkActionResult{Succeeded: []string{}, Failed: []importSkip{}}
	for _, id := range body.IDs {
		if err := h.applyBulkAction(clientID, id, body.Action, body.ProjectID); err != nil {
			result.Failed = append(result.Failed, importSkip{Name: id, Reason: err.Error()})
			continue
		}
		result.Succeeded = append(result.Succeeded, id)
	}
	writeJSON(w, http.StatusOK, result)
}

// errBulkWorkspaceLocked is applyBulkAction's error for an item whose
// workspace (its own, or its project's) is locked and not unlocked for
// the calling client — reported per-item in bulkActionResult.Failed
// rather than aborting the whole bulk request, the same way any other
// per-item failure (not found, etc.) already is. Previously this check
// didn't exist at all here: the singular create/update/delete handlers
// below all gate on checkWorkspaceUnlocked, but bulk enable/disable/
// delete/move went straight to the store, silently bypassing the lock
// for anyone using the bulk-action bar instead of the per-mock controls.
var errBulkWorkspaceLocked = errors.New("workspace locked")

func (h *MocksHandler) applyBulkAction(clientID, id, action, projectID string) error {
	d, err := h.store.Get(id)
	if err != nil {
		return err
	}
	atRisk := []string{d.WorkspaceID, h.projectWorkspaceID(d.ProjectID)}
	if action == "move" {
		atRisk = append(atRisk, h.projectWorkspaceID(projectID))
	}
	if firstLockedWorkspace(h.wsLockChecker, h.wsUnlockTracker, clientID, atRisk...) != "" {
		return errBulkWorkspaceLocked
	}

	if action == "delete" {
		if err := h.store.Delete(id); err != nil {
			return err
		}
		return engine.DispatchUnregister(d.ProtocolType, id)
	}

	if action == "move" {
		d.ProjectID = projectID
	} else {
		d.Enabled = action == "enable"
	}
	updated, err := h.store.Update(d)
	if err != nil {
		return err
	}
	return engine.Dispatch(updated)
}

// validateMockShape enforces the one required-fields rule that differs by
// protocol family: REST/SOAP/GraphQL need a method and path to route on; WS
// needs just a path (its method is always GET, set by the store rather than
// asked of the caller); TCP, SMTP, and MQTT need neither — they need a
// listener port instead, since none of them has a path to multiplex several
// mocks onto one gateway port.
var validProtocolTypes = map[string]bool{
	"rest": true, "soap": true, "graphql": true, "ws": true,
	"tcp": true, "smtp": true, "mqtt": true, "ftp": true, "kafka": true, "smpp": true, "diameter": true, "jms": true,
}

// validRESTMethods is exactly the set chi's router recognizes (see
// go-chi/chi/v5's methodMap) — chi.Mux.MethodFunc PANICS on anything
// outside this set, and a REST mock's Method is passed to it verbatim
// (buildMux in internal/engine/http/engine.go), so a bad method must never
// reach that call. Matched case-insensitively since chi itself uppercases
// before comparing.
var validRESTMethods = map[string]bool{
	http.MethodGet: true, http.MethodPost: true, http.MethodPut: true, http.MethodPatch: true,
	http.MethodDelete: true, http.MethodHead: true, http.MethodOptions: true,
	http.MethodConnect: true, http.MethodTrace: true, "QUERY": true,
}

// listenerPortOf is table-driven (one entry per dedicated-listener
// protocol) rather than a growing switch statement — TCP/SMTP/MQTT/FTP/
// Kafka/SMPP each own their own listener port instead of sharing the HTTP
// gateway, and each new one added here is one map entry, not another
// branch for validateMockShape to carry.
var listenerPortOf = map[string]func(d *mock.Definition) (port int, hasConfig bool){
	"tcp":      func(d *mock.Definition) (int, bool) { return tcpPort(d), d.TCP != nil },
	"smtp":     func(d *mock.Definition) (int, bool) { return smtpPort(d), d.SMTP != nil },
	"mqtt":     func(d *mock.Definition) (int, bool) { return mqttPort(d), d.MQTT != nil },
	"ftp":      func(d *mock.Definition) (int, bool) { return ftpPort(d), d.FTP != nil },
	"kafka":    func(d *mock.Definition) (int, bool) { return kafkaPort(d), d.Kafka != nil },
	"smpp":     func(d *mock.Definition) (int, bool) { return smppPort(d), d.SMPP != nil },
	"diameter": func(d *mock.Definition) (int, bool) { return diameterPort(d), d.Diameter != nil },
	"jms":      func(d *mock.Definition) (int, bool) { return jmsPort(d), d.JMS != nil },
}

func tcpPort(d *mock.Definition) int {
	if d.TCP == nil {
		return 0
	}
	return d.TCP.Port
}

func smtpPort(d *mock.Definition) int {
	if d.SMTP == nil {
		return 0
	}
	return d.SMTP.Port
}

func mqttPort(d *mock.Definition) int {
	if d.MQTT == nil {
		return 0
	}
	return d.MQTT.Port
}

func ftpPort(d *mock.Definition) int {
	if d.FTP == nil {
		return 0
	}
	return d.FTP.Port
}

func kafkaPort(d *mock.Definition) int {
	if d.Kafka == nil {
		return 0
	}
	return d.Kafka.Port
}

func smppPort(d *mock.Definition) int {
	if d.SMPP == nil {
		return 0
	}
	return d.SMPP.Port
}

func diameterPort(d *mock.Definition) int {
	if d.Diameter == nil {
		return 0
	}
	return d.Diameter.Port
}

func jmsPort(d *mock.Definition) int {
	if d.JMS == nil {
		return 0
	}
	return d.JMS.Port
}

// applyProjectBasePath puts the project's basePath in front of an HTTP-family
// mock's path when it is not already there, so a mock saved through the API
// is served under the project's prefix the same way one created in the UI
// is (the UI pre-fills it). It is idempotent: a path that already starts
// with the prefix, at a path-segment boundary, is left alone, so re-applying
// a file or editing a UI-created mock never doubles it. Changing a project's
// basePath later does not move mocks already saved (see mock.Project).
func (h *MocksHandler) applyProjectBasePath(d *mock.Definition) {
	switch d.ProtocolType {
	case "rest", "soap", "graphql", "ws":
	default:
		return
	}
	if d.ProjectID == "" {
		return
	}
	project, err := h.store.GetProject(d.ProjectID)
	if err != nil || project == nil {
		return
	}
	base := strings.Trim(strings.TrimSpace(project.BasePath), "/")
	if base == "" {
		return
	}
	base = "/" + base
	p := d.PathPattern
	if p == base || strings.HasPrefix(p, base+"/") {
		return
	}
	rest := strings.Trim(p, "/")
	if strings.HasSuffix(p, "/") && rest != "" {
		rest += "/" // keep a trailing slash the author wrote
	}
	if rest == "" {
		d.PathPattern = base
		return
	}
	d.PathPattern = base + "/" + rest
}

// templateError marks a mock rejected only because one of its templates does
// not parse.
type templateError struct{ err error }

func (e *templateError) Error() string { return e.err.Error() }
func (e *templateError) Unwrap() error { return e.err }

// validateMockShape checks the structure of a mock and that every template it
// carries parses, so a typo is reported when the mock is saved rather than on
// every request.
func validateMockShape(d *mock.Definition) error {
	if err := validateMockStructure(d); err != nil {
		return err
	}
	if err := mock.ValidateDefinitionTemplates(d); err != nil {
		return &templateError{err}
	}
	return nil
}

func validateMockStructure(d *mock.Definition) error {
	if !validProtocolTypes[d.ProtocolType] {
		return fmt.Errorf("unknown protocolType %q", d.ProtocolType)
	}
	if getPort, ok := listenerPortOf[d.ProtocolType]; ok {
		port, hasConfig := getPort(d)
		if !hasConfig || port <= 0 {
			return fmt.Errorf("%s.port is required and must be positive", d.ProtocolType)
		}
		return validateProtocolConfig(d)
	}
	if d.ProtocolType == "ws" {
		if d.PathPattern == "" {
			return errors.New("pathPattern is required")
		}
		return nil
	}
	if d.Method == "" || d.PathPattern == "" {
		return errors.New("method and pathPattern are required")
	}
	if d.ProtocolType == "rest" && !validRESTMethods[strings.ToUpper(d.Method)] {
		return fmt.Errorf("method %q is not a supported HTTP method", d.Method)
	}
	if d.Mode == "proxy" && (d.Proxy == nil || strings.TrimSpace(d.Proxy.TargetBaseURL) == "") {
		return errors.New(`mode "proxy" needs proxy.targetBaseUrl (the upstream to forward to; every proxied exchange is captured to the hit log automatically, there is no separate "record" setting)`)
	}
	return nil
}

// smtpBannerPattern: an SMTP greeting line must begin with a three-digit
// reply code followed by a space (last line) or hyphen (multi-line).
var smtpBannerPattern = regexp.MustCompile(`^\d{3}[ -]`)

var validFinalUnitActions = map[string]bool{"": true, "terminate": true, "redirect": true, "restrict_access": true}

// validateProtocolConfig holds the per-protocol checks that go beyond "has a
// port", so a mistake that would only surface as a confusing client-side
// failure is rejected when the mock is saved.
func validateProtocolConfig(d *mock.Definition) error {
	switch d.ProtocolType {
	case "smtp":
		if b := strings.TrimSpace(d.SMTP.Banner); b != "" && !smtpBannerPattern.MatchString(b) {
			return fmt.Errorf(`smtp.banner must start with a three-digit reply code such as "220 " (it is sent to the client verbatim as the greeting), got %q`, b)
		}
	case "smpp":
		for i, r := range d.SMPP.Rules {
			// Mirror the engine: with no messageMatch, a regex matchType also
			// applies to the destination, so its pattern must compile.
			if r.DestAddrMatchType == "" && r.MessageMatch == "" && r.MatchType == "regex" && r.DestAddrPattern != "" {
				if _, err := regexp.Compile(r.DestAddrPattern); err != nil {
					return fmt.Errorf("smpp.rules[%d].destAddrPattern is not a valid regular expression: %w", i, err)
				}
			}
			switch r.DestAddrMatchType {
			case "", "exact", "prefix", "contains", "wildcard", "range":
			case "regex":
				if _, err := regexp.Compile(r.DestAddrPattern); err != nil {
					return fmt.Errorf("smpp.rules[%d].destAddrPattern is not a valid regular expression: %w", i, err)
				}
			default:
				return fmt.Errorf(`smpp.rules[%d].destAddrMatchType must be "exact", "prefix", "contains", "wildcard", "regex" or "range", got %q`, i, r.DestAddrMatchType)
			}
		}
	case "diameter":
		if d.Diameter.DefaultResultCode < 0 {
			return errors.New("diameter.defaultResultCode must not be negative")
		}
		for i, r := range d.Diameter.Rules {
			if !validFinalUnitActions[r.FinalUnitAction] {
				return fmt.Errorf(`diameter.rules[%d].finalUnitAction must be "terminate", "redirect" or "restrict_access", got %q`, i, r.FinalUnitAction)
			}
			if r.DelayMs < 0 {
				return fmt.Errorf("diameter.rules[%d].delayMs must not be negative", i)
			}
			if r.CCRequestType < 0 || r.CCRequestType > 4 {
				return fmt.Errorf("diameter.rules[%d].ccRequestType must be 0 (any) or 1-4", i)
			}
		}
	}
	return nil
}

// dryRunReport is the answer to a create/update sent with ?dryRun=true: the
// mock was fully validated (shape, templates, name/endpoint conflicts and
// workspace locks) but nothing was saved.
type dryRunReport struct {
	DryRun   bool     `json:"dryRun"`
	Action   string   `json:"action"` // "create" | "update" | "unchanged"
	Warnings []string `json:"warnings,omitempty"`
}

func isDryRun(r *http.Request) bool {
	v := r.URL.Query().Get("dryRun")
	return v == "true" || v == "1"
}

// decodeStrict decodes a JSON request body and rejects any field the
// target type doesn't define. Without this a typo or an unsupported setting
// is silently dropped and the saved mock quietly differs from what the
// caller sent.
func decodeStrict(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	// A client that round-trips a saved mock sends back the response's
	// read-only "warnings"; tolerate (and discard) exactly that one extra.
	var err error
	if d, ok := v.(*mock.Definition); ok {
		wrapper := struct {
			*mock.Definition
			Warnings json.RawMessage `json:"warnings"`
		}{Definition: d}
		err = dec.Decode(&wrapper)
	} else {
		err = dec.Decode(v)
	}
	if err != nil {
		if strings.HasPrefix(err.Error(), "json: unknown field ") {
			return fmt.Errorf("%s: not a setting AirMock supports (unrecognised fields are rejected, not ignored, so a typo cannot silently disappear)", strings.TrimPrefix(err.Error(), "json: "))
		}
		return err
	}
	return nil
}

// mockResponse is a mock plus any non-fatal advice about how it is
// configured.
type mockResponse struct {
	*mock.Definition
	Warnings []string `json:"warnings,omitempty"`
}

// writeMockJSON writes a mock with its warnings both in the body and as
// X-AirMock-Warning response headers, so a script or CLI that never parses
// the body still has them in front of it.
func writeMockJSON(w http.ResponseWriter, status int, d *mock.Definition) {
	resp := withWarnings(d)
	for _, msg := range resp.Warnings {
		w.Header().Add("X-AirMock-Warning", msg)
	}
	writeJSON(w, status, resp)
}

func withWarnings(d *mock.Definition) mockResponse {
	return mockResponse{Definition: d, Warnings: mockWarnings(d)}
}

// mockWarnings flags configurations that are valid but very likely not what
// was intended.
func mockWarnings(d *mock.Definition) []string {
	var out []string
	check := func(where string, a *mock.AsyncConfig) {
		if a == nil || strings.TrimSpace(a.CallbackBodyTemplate) != "" || a.EmailTemplateID != "" {
			return
		}
		switch a.CallbackChannel {
		case "email":
			out = append(out, where+": the email callback has no callbackBodyTemplate or emailTemplateId, so the email will have an empty body")
		default:
			method := strings.ToUpper(a.CallbackMethod)
			if method == "" {
				method = http.MethodPost
			}
			if method == http.MethodPost || method == http.MethodPut || method == http.MethodPatch {
				out = append(out, fmt.Sprintf("%s: the callback has no callbackBodyTemplate, so each %s sends the default body %s; set callbackBodyTemplate to send something else", where, method, mock.DefaultCallbackBody))
			}
		}
	}
	if d.Mode == "async" {
		check("asyncConfig", d.AsyncConfig)
	}
	if d.TCP != nil {
		for i := range d.TCP.Interactions {
			check(fmt.Sprintf("tcp.interactions[%d].async", i), d.TCP.Interactions[i].Async)
		}
	}
	if d.WS != nil {
		for i := range d.WS.Interactions {
			check(fmt.Sprintf("ws.interactions[%d].async", i), d.WS.Interactions[i].Async)
		}
	}
	return out
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, err error) {
	writeJSON(w, status, map[string]string{"error": err.Error()})
}
