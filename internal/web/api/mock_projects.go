package api

import (
	"encoding/json"
	"errors"
	"log"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	httpengine "github.com/addictedabhi/airmock/internal/engine/http"
	"github.com/addictedabhi/airmock/internal/mock"
)

type MockProjectsHandler struct {
	store           *mock.Store
	httpEngine      *httpengine.Engine
	wsLockChecker   WorkspaceLockChecker
	wsUnlockTracker WorkspaceUnlockTracker
}

func NewMockProjectsHandler(store *mock.Store, httpEngine *httpengine.Engine, wsLockChecker WorkspaceLockChecker, wsUnlockTracker WorkspaceUnlockTracker) *MockProjectsHandler {
	return &MockProjectsHandler{store: store, httpEngine: httpEngine, wsLockChecker: wsLockChecker, wsUnlockTracker: wsUnlockTracker}
}

func (h *MockProjectsHandler) Routes(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Put("/{id}", h.update)
	r.Delete("/{id}", h.delete)
}

func (h *MockProjectsHandler) list(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.ListProjects()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *MockProjectsHandler) create(w http.ResponseWriter, r *http.Request) {
	var p mock.Project
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if p.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	if !checkWorkspaceUnlocked(w, r, h.wsLockChecker, h.wsUnlockTracker, p.WorkspaceID) {
		return
	}
	created, err := h.store.CreateProject(&p)
	if errors.Is(err, mock.ErrDuplicateProjectName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *MockProjectsHandler) update(w http.ResponseWriter, r *http.Request) {
	var p mock.Project
	if err := json.NewDecoder(r.Body).Decode(&p); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	p.ID = chi.URLParam(r, "id")
	previous, prevErr := h.store.GetProject(p.ID)
	if prevErr == nil {
		if !checkWorkspaceUnlocked(w, r, h.wsLockChecker, h.wsUnlockTracker, previous.WorkspaceID, p.WorkspaceID) {
			return
		}
	} else if !checkWorkspaceUnlocked(w, r, h.wsLockChecker, h.wsUnlockTracker, p.WorkspaceID) {
		return
	}
	updated, err := h.store.UpdateProject(&p)
	if errors.Is(err, mock.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, mock.ErrDuplicateProjectName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	// A project's OWN settings (dedicated port, TLS bundle, client-cert
	// mode) have no mock event to ride along with — RegisterMock/
	// UnregisterMock already re-sync the engine's per-project listener as a
	// side effect of a MOCK changing, but editing the project itself
	// wouldn't otherwise take effect until some unrelated mock in it next
	// changed. Rebuild re-syncs it immediately instead.
	if h.httpEngine != nil {
		if err := h.httpEngine.Rebuild(); err != nil {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *MockProjectsHandler) delete(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")

	if p, err := h.store.GetProject(id); err == nil {
		if !checkWorkspaceUnlocked(w, r, h.wsLockChecker, h.wsUnlockTracker, p.WorkspaceID) {
			return
		}
	}

	// Fetched before DeleteProject removes their rows — this is the only
	// place that still knows which mocks belonged to this project, and
	// each one needs an explicit DispatchUnregister to actually stop
	// serving (DeleteProject only handles the database side; the engine's
	// own in-memory route table/listeners are a separate thing that a mock
	// row disappearing doesn't automatically update).
	toUnregister, err := h.store.ListByProject(id)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if err := h.store.DeleteProject(id); err != nil {
		if errors.Is(err, mock.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	// Best-effort: the project and its mocks are already committed gone in
	// the database: a failure to unregister one here only risks a stale
	// route lingering in memory until the next unrelated mock event/
	// restart re-syncs it, not the delete itself failing.
	for _, m := range toUnregister {
		if err := engine.DispatchUnregister(m.ProtocolType, m.ID); err != nil {
			log.Printf("airmock: failed to unregister mock %q (%s) after deleting its project: %v", m.Name, m.ID, err)
		}
	}
	if h.httpEngine != nil {
		h.httpEngine.Rebuild()
	}
	w.WriteHeader(http.StatusNoContent)
}
