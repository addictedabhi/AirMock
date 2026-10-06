package httpengine

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/vektah/gqlparser/v2/ast"
	"github.com/vektah/gqlparser/v2/parser"

	"github.com/addictedabhi/airmock/internal/mock"
)

type graphqlRequestBody struct {
	Query         string         `json:"query"`
	Variables     map[string]any `json:"variables,omitempty"`
	OperationName string         `json:"operationName,omitempty"`
}

// serveGraphQL dispatches a request against every GraphQL mock sharing this
// endpoint's path (conventionally "/graphql"), matching by the request's
// operationName field or, failing that, the query document's own operation
// name / top-level field name — since anonymous operations (the common
// `query { field { ... } }` shorthand) have no operation name at all.
func (e *Engine) serveGraphQL(w http.ResponseWriter, r *http.Request, ops []*mock.Definition) {
	bodyBytes, _, ok := readRequestBody(w, r)
	if !ok {
		return
	}

	var reqBody graphqlRequestBody
	if err := json.Unmarshal(bodyBytes, &reqBody); err != nil {
		writeGraphQLError(w, "invalid GraphQL request body: "+err.Error())
		return
	}

	opName := reqBody.OperationName
	if opName == "" {
		opName = detectGraphQLOperationName(reqBody.Query)
	}

	def := matchGraphQLOperation(ops, opName)
	if def == nil {
		writeGraphQLError(w, "no matching GraphQL operation for this request")
		return
	}

	// Rules, validation and templates read the request through
	// body.variables.*; make inline arguments such as user(id: "1") visible
	// there too, as if they had been sent as variables.
	reqCtx := mock.BuildRequestContext(r, withInlineArguments(bodyBytes, reqBody, opName), nil)

	cw := newCapturingWriter(w)
	start := time.Now()
	defer e.logInboundHit(cw, r, def, bodyBytes, start)
	w = cw

	if e.runValidation(w, def, reqCtx) {
		return
	}

	if def.Mode == "async" && def.AsyncConfig != nil {
		e.serveAsync(w, reqCtx, def, jsonContentType)
		return
	}

	if def.Scenario != nil && len(def.Scenario.Steps) > 0 {
		e.serveScenario(w, r, reqCtx, def, jsonContentType)
		return
	}

	e.writeTemplatedResponse(w, mock.SelectResponse(def, reqCtx), reqCtx, def.Fault, def.ID)
}

// withInlineArguments returns the request body with the arguments of the
// operation's root fields merged into "variables": literals as written,
// variable references resolved. A variable the client sent explicitly is
// never overridden, and the first root field wins when two use the same
// argument name. If there is nothing to add (or the query does not parse)
// the body is returned unchanged.
func withInlineArguments(body []byte, req graphqlRequestBody, opName string) []byte {
	if req.Query == "" {
		return body
	}
	doc, err := parser.ParseQuery(&ast.Source{Input: req.Query})
	if err != nil || len(doc.Operations) == 0 {
		return body
	}
	op := doc.Operations[0]
	if opName != "" {
		for _, o := range doc.Operations {
			if o.Name == opName {
				op = o
				break
			}
		}
	}
	added := map[string]any{}
	for _, sel := range op.SelectionSet {
		field, ok := sel.(*ast.Field)
		if !ok {
			continue
		}
		for _, arg := range field.Arguments {
			if _, exists := req.Variables[arg.Name]; exists {
				continue
			}
			if _, seen := added[arg.Name]; seen {
				continue
			}
			if v, err := arg.Value.Value(req.Variables); err == nil && v != nil {
				added[arg.Name] = v
			}
		}
	}
	if len(added) == 0 {
		return body
	}
	var doc2 map[string]any
	if err := json.Unmarshal(body, &doc2); err != nil {
		return body
	}
	vars, _ := doc2["variables"].(map[string]any)
	if vars == nil {
		vars = map[string]any{}
	}
	for k, v := range added {
		vars[k] = v
	}
	doc2["variables"] = vars
	out, err := json.Marshal(doc2)
	if err != nil {
		return body
	}
	return out
}

func matchGraphQLOperation(ops []*mock.Definition, opName string) *mock.Definition {
	if opName != "" {
		for _, d := range ops {
			if d.OperationName != "" && d.OperationName == opName {
				return d
			}
		}
	}
	if len(ops) == 1 {
		return ops[0]
	}
	return nil
}

// detectGraphQLOperationName parses the query document and returns its
// (single, since a request always executes exactly one) operation's Name,
// falling back to the first top-level selected field's name for anonymous
// operations — e.g. "order" in `query { order(id: 1) { status } }`.
func detectGraphQLOperationName(query string) string {
	if query == "" {
		return ""
	}
	doc, err := parser.ParseQuery(&ast.Source{Input: query})
	if err != nil || len(doc.Operations) == 0 {
		return ""
	}
	op := doc.Operations[0]
	if op.Name != "" {
		return op.Name
	}
	for _, sel := range op.SelectionSet {
		if field, ok := sel.(*ast.Field); ok {
			return field.Name
		}
	}
	return ""
}

func writeGraphQLError(w http.ResponseWriter, message string) {
	w.Header().Set("Content-Type", jsonContentType)
	w.WriteHeader(http.StatusOK) // GraphQL convention: errors are reported in-band, not via HTTP status
	json.NewEncoder(w).Encode(map[string]any{
		"errors": []map[string]string{{"message": message}},
	})
}
