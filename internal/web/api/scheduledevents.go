package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/scheduledevent"
)

type ScheduledEventsHandler struct {
	store  *scheduledevent.Store
	worker *scheduledevent.Worker
}

func NewScheduledEventsHandler(store *scheduledevent.Store, worker *scheduledevent.Worker) *ScheduledEventsHandler {
	return &ScheduledEventsHandler{store: store, worker: worker}
}

func (h *ScheduledEventsHandler) Routes(r chi.Router) {
	r.Get("/", h.list)
	r.Post("/", h.create)
	r.Get("/{id}", h.get)
	r.Put("/{id}", h.update)
	r.Delete("/{id}", h.delete)
	r.Post("/{id}/fire-now", h.fireNow)
	r.Put("/{id}/csv-source", h.setCSVSource)
	r.Get("/{id}/csv-source", h.getCSVSource)
	r.Delete("/{id}/csv-source", h.deleteCSVSource)
}

// setCSVSource attaches (or replaces) a scheduled event's CSV data source —
// see MocksHandler.setCSVSource's identical shape; scheduled events get
// their own counters/CSV rows (scoped by the event's own id) rather than
// sharing a mock's, via scheduledevent.Store's own Counter/NextCSVRow
// methods (internal/scheduledevent/dynamicvalues.go).
func (h *ScheduledEventsHandler) setCSVSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	mode, content, err := parseCSVUpload(r)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	meta, err := h.store.SetCSVSource(id, mode, content)
	if err != nil {
		if errors.Is(err, scheduledevent.ErrEmptyCSV) || errors.Is(err, scheduledevent.ErrInvalidCSVMode) {
			writeErr(w, http.StatusBadRequest, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (h *ScheduledEventsHandler) getCSVSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if _, err := h.store.Get(id); errors.Is(err, scheduledevent.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	meta, err := h.store.GetCSVSourceMeta(id)
	if errors.Is(err, scheduledevent.ErrNotFound) {
		w.WriteHeader(http.StatusNoContent) // exists, but no CSV attached
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, meta)
}

func (h *ScheduledEventsHandler) deleteCSVSource(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if err := h.store.DeleteCSVSource(id); err != nil {
		if errors.Is(err, scheduledevent.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *ScheduledEventsHandler) list(w http.ResponseWriter, r *http.Request) {
	events, err := h.store.List()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, events)
}

func (h *ScheduledEventsHandler) create(w http.ResponseWriter, r *http.Request) {
	var e scheduledevent.Event
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := validateScheduledEvent(&e); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	created, err := h.store.Create(&e)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *ScheduledEventsHandler) get(w http.ResponseWriter, r *http.Request) {
	e, err := h.store.Get(chi.URLParam(r, "id"))
	if errors.Is(err, scheduledevent.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, e)
}

func (h *ScheduledEventsHandler) update(w http.ResponseWriter, r *http.Request) {
	var e scheduledevent.Event
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	e.ID = chi.URLParam(r, "id")
	if err := validateScheduledEvent(&e); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	updated, err := h.store.Update(&e)
	if errors.Is(err, scheduledevent.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *ScheduledEventsHandler) delete(w http.ResponseWriter, r *http.Request) {
	if err := h.store.Delete(chi.URLParam(r, "id")); errors.Is(err, scheduledevent.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	} else if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// fireNow delivers the event immediately, through the exact same Worker.Fire
// path a real timer tick uses, so a user can verify a target URL/body
// template actually works without waiting out the interval.
func (h *ScheduledEventsHandler) fireNow(w http.ResponseWriter, r *http.Request) {
	e, err := h.store.Get(chi.URLParam(r, "id"))
	if errors.Is(err, scheduledevent.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	h.worker.Fire(e)
	fresh, err := h.store.Get(e.ID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, fresh)
}

func validateScheduledEvent(e *scheduledevent.Event) error {
	if e.Name == "" {
		return errors.New("name is required")
	}
	if e.TargetURL == "" {
		return errors.New("targetUrl is required")
	}
	if e.IntervalSecs <= 0 {
		return errors.New("intervalSecs must be positive")
	}
	if err := mock.ValidateTemplate(e.BodyTemplate); err != nil {
		return fmt.Errorf("bodyTemplate is not a valid template: %w", err)
	}
	return nil
}
