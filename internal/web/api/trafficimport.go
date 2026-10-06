package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/traffic"
	"github.com/addictedabhi/airmock/internal/validate"
)

// TrafficImportHandler scaffolds REST mocks from traffic captured outside
// this instance (a HAR export or a Postman collection with saved examples) —
// the same "parse then Create+Dispatch each ScaffoldOperation" shape as
// OpenAPIHandler/WSDLHandler, just fed by internal/traffic instead of a spec
// parser.
type TrafficImportHandler struct {
	store *mock.Store
}

func NewTrafficImportHandler(store *mock.Store) *TrafficImportHandler {
	return &TrafficImportHandler{store: store}
}

func (h *TrafficImportHandler) Routes(r chi.Router) {
	r.Post("/har", h.importHAR)
	r.Post("/postman-examples", h.importPostmanExamples)
}

type trafficImportRequest struct {
	Content    string `json:"content"`
	PathPrefix string `json:"pathPrefix,omitempty"`
	ProjectID  string `json:"projectId,omitempty"`
}

func (h *TrafficImportHandler) importHAR(w http.ResponseWriter, r *http.Request) {
	var req trafficImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	scaffolds, err := traffic.ParseHAR([]byte(req.Content))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	h.createScaffolds(w, scaffolds, req.PathPrefix, req.ProjectID)
}

func (h *TrafficImportHandler) importPostmanExamples(w http.ResponseWriter, r *http.Request) {
	var req trafficImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	scaffolds, err := traffic.ParsePostmanExamples([]byte(req.Content))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	h.createScaffolds(w, scaffolds, req.PathPrefix, req.ProjectID)
}

// createScaffolds mirrors OpenAPIHandler.importSpec: create-and-dispatch
// each scaffolded mock, aborting with whatever error the first failure hit
// rather than partially importing — same all-or-nothing behavior as the
// existing spec importers, so this reverse importer doesn't behave
// differently from its siblings for no reason. A name collision (e.g.
// re-importing the same capture twice) surfaces as a normal 409/500 from
// store.Create, same as it would through the regular "+ New mock" form.
func (h *TrafficImportHandler) createScaffolds(w http.ResponseWriter, scaffolds []traffic.ScaffoldMock, pathPrefix, projectID string) {
	created := make([]*mock.Definition, 0, len(scaffolds))
	for _, s := range scaffolds {
		def := &mock.Definition{
			Name:         s.Name,
			ProtocolType: "rest",
			Method:       s.Method,
			PathPattern:  pathPrefix + s.Path,
			Enabled:      true,
			ProjectID:    projectID,
			Validation:   validationRulesFromSampleBody(s.RequestBody),
			Response: mock.ResponseTemplate{
				StatusCode:   s.StatusCode,
				Headers:      s.ResponseHeaders,
				BodyTemplate: s.ResponseBody,
			},
		}
		saved, err := h.store.Create(def)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, fmt.Errorf("%s %s: %w", s.Method, s.Path, err))
			return
		}
		if err := engine.Dispatch(saved); err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		created = append(created, saved)
	}
	writeJSON(w, http.StatusCreated, created)
}

// validationRulesFromSampleBody derives a best-effort "required" rule per
// top-level JSON key actually present in one captured request — the closest
// equivalent captured traffic has to OpenAPI import's schema-derived
// validationRulesFromParams, since there's no formal spec here, only one
// real observed request. Previously the captured request body was read
// nowhere at all once path/method matching was done, discarding real
// information about what a working request to this endpoint looks like.
// Silently returns nil for an empty or non-object body (GET requests, a
// JSON array, plain text) rather than guessing.
func validationRulesFromSampleBody(body string) []validate.Rule {
	if body == "" {
		return nil
	}
	var parsed map[string]any
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		return nil
	}
	if len(parsed) == 0 {
		return nil
	}
	keys := make([]string, 0, len(parsed))
	for key := range parsed {
		keys = append(keys, key)
	}
	sort.Strings(keys) // deterministic order — map iteration isn't
	rules := make([]validate.Rule, 0, len(keys))
	for _, key := range keys {
		rules = append(rules, validate.Rule{Field: "body." + key, Required: true})
	}
	return rules
}
