package api

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/openapi"
	"github.com/addictedabhi/airmock/internal/validate"
)

type OpenAPIHandler struct {
	store *mock.Store
}

func NewOpenAPIHandler(store *mock.Store) *OpenAPIHandler {
	return &OpenAPIHandler{store: store}
}

func (h *OpenAPIHandler) Routes(r chi.Router) {
	r.Post("/import", h.importSpec)
}

type openapiImportRequest struct {
	OpenAPIContent string `json:"openapiContent"`
	PathPrefix     string `json:"pathPrefix,omitempty"`
	ProjectID      string `json:"projectId,omitempty"`
}

// importSpec parses a pasted OpenAPI 3.x document (JSON or YAML) and
// scaffolds one working, validated REST mock per operation — unlike WSDL/
// GraphQL import, OpenAPI already declares full paths and methods, so no
// endpoint path needs to be supplied, only an optional mount prefix.
func (h *OpenAPIHandler) importSpec(w http.ResponseWriter, r *http.Request) {
	var req openapiImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	ops, err := openapi.Parse([]byte(req.OpenAPIContent))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("parse OpenAPI document: %w", err))
		return
	}
	if len(ops) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no operations found in the OpenAPI document"))
		return
	}

	created := make([]*mock.Definition, 0, len(ops))
	for _, op := range ops {
		def := &mock.Definition{
			Name:         op.Name,
			ProtocolType: "rest",
			Method:       op.Method,
			PathPattern:  req.PathPrefix + op.Path,
			Enabled:      true,
			ProjectID:    req.ProjectID,
			Validation:   validationRulesFromParams(op.Params),
			Response: mock.ResponseTemplate{
				StatusCode:   op.StatusCode,
				Headers:      map[string]string{"Content-Type": op.ContentType},
				BodyTemplate: op.ExampleJSON,
			},
		}
		saved, err := h.store.Create(def)
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
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

// validationRulesFromParams derives Required rules for query/header/body
// parameters. Path parameters are skipped: chi's route pattern already
// guarantees their presence, so a validation rule for them would be dead
// code that can never fail.
func validationRulesFromParams(params []openapi.Param) []validate.Rule {
	var rules []validate.Rule
	for _, p := range params {
		if !p.Required || p.In == "path" {
			continue
		}
		rules = append(rules, validate.Rule{Field: p.In + "." + p.Name, Required: true})
	}
	return rules
}
