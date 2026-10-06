package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/vektah/gqlparser/v2"
	"github.com/vektah/gqlparser/v2/ast"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/validate"
)

type GraphQLHandler struct {
	store *mock.Store
}

func NewGraphQLHandler(store *mock.Store) *GraphQLHandler {
	return &GraphQLHandler{store: store}
}

func (h *GraphQLHandler) Routes(r chi.Router) {
	r.Post("/import", h.importSDL)
}

type graphqlImportRequest struct {
	SDLContent  string `json:"sdlContent"`
	PathPattern string `json:"pathPattern"`
	ProjectID   string `json:"projectId,omitempty"`
}

// importSDL parses a pasted GraphQL SDL schema and scaffolds one mock per
// top-level Query/Mutation field on the given endpoint path, each with a
// stub response and validation rules derived from the field's non-null
// arguments — schema-aware validation, the GraphQL counterpart of phase
// 1.5's WSDL import and phase 1.7's OpenAPI import.
func (h *GraphQLHandler) importSDL(w http.ResponseWriter, r *http.Request) {
	var req graphqlImportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.PathPattern == "" {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("pathPattern is required"))
		return
	}

	schema, err := gqlparser.LoadSchema(&ast.Source{Input: req.SDLContent})
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("parse SDL: %w", err))
		return
	}

	var fields []*ast.FieldDefinition
	if schema.Query != nil {
		fields = append(fields, nonIntrospectionFields(schema.Query.Fields)...)
	}
	if schema.Mutation != nil {
		fields = append(fields, nonIntrospectionFields(schema.Mutation.Fields)...)
	}
	if len(fields) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no Query or Mutation fields found in the schema"))
		return
	}

	created := make([]*mock.Definition, 0, len(fields))
	for _, field := range fields {
		def := &mock.Definition{
			Name:          field.Name,
			ProtocolType:  "graphql",
			Method:        http.MethodPost,
			PathPattern:   req.PathPattern,
			Enabled:       true,
			ProjectID:     req.ProjectID,
			OperationName: field.Name,
			Validation:    requiredArgRules(field),
			Response: mock.ResponseTemplate{
				StatusCode:   200,
				BodyTemplate: fmt.Sprintf(`{"data":{"%s":null}}`, field.Name),
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

// nonIntrospectionFields filters out GraphQL's built-in introspection
// fields (__schema, __type, ...) that gqlparser injects into every parsed
// schema — scaffolding mocks for those would be noise, not a real operation.
func nonIntrospectionFields(fields ast.FieldList) []*ast.FieldDefinition {
	var out []*ast.FieldDefinition
	for _, f := range fields {
		if !strings.HasPrefix(f.Name, "__") {
			out = append(out, f)
		}
	}
	return out
}

// requiredArgRules derives one Required validate.Rule per non-null
// argument, matched against the request's "variables" object — the same
// JSON body validation already reads via ExtractField's "body." prefix
// (GraphQL variables are just a nested key under the request body).
func requiredArgRules(field *ast.FieldDefinition) []validate.Rule {
	var rules []validate.Rule
	for _, arg := range field.Arguments {
		if arg.Type != nil && arg.Type.NonNull {
			rules = append(rules, validate.Rule{
				Field:    "body.variables." + arg.Name,
				Required: true,
			})
		}
	}
	return rules
}
