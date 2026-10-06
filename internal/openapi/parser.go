// Package openapi is a thin wrapper around getkin/kin-openapi that reduces
// a parsed OpenAPI 3.x document to exactly what AirMock needs to scaffold
// REST mocks: one ScaffoldOperation per path+method, an example response
// body (from the spec's own example, or synthesized from its schema when
// no example is given), and the parameters/body fields required so a
// caller can derive validation rules — mirroring phase 1.5's WSDL import
// and phase 1.6's GraphQL SDL import for the REST side of the mock engine.
package openapi

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strconv"
	"strings"

	"github.com/getkin/kin-openapi/openapi3"
)

const jsonContentType = "application/json"

type Param struct {
	In       string // "path" | "query" | "header" | "body"
	Name     string
	Required bool
}

type ScaffoldOperation struct {
	Name        string // operationId, or "METHOD /path" when absent
	Method      string
	Path        string // chi-compatible, includes the servers[] base path if any: OpenAPI and chi both use {param}
	StatusCode  int
	ExampleJSON string
	ContentType string // the media type ExampleJSON was derived from, e.g. "application/json" or "application/xml"
	Params      []Param
}

// IsExternalRefsAllowed lets a $ref point at another document (by absolute
// URL, or a relative path when the spec was loaded from a file/URL rather
// than pasted text) instead of only within the same document. AirMock's
// import flow only ever receives raw pasted content with no base
// file/URL to resolve a *relative* external $ref against, so only
// absolute-URL external refs actually resolve today — a real but narrower
// gap than "no external $ref support at all".
func Parse(data []byte) ([]ScaffoldOperation, error) {
	loader := openapi3.NewLoader()
	loader.IsExternalRefsAllowed = true
	doc, err := loader.LoadFromData(data)
	if err != nil {
		return nil, fmt.Errorf("load OpenAPI document: %w", err)
	}
	if doc.Paths == nil {
		return nil, nil
	}

	basePath := basePathFromServers(doc.Servers)

	var ops []ScaffoldOperation
	for _, path := range doc.Paths.InMatchingOrder() {
		item := doc.Paths.Find(path)
		if item == nil {
			continue
		}
		for method, op := range item.Operations() {
			ops = append(ops, buildOperation(method, basePath+path, op))
		}
	}
	return ops, nil
}

// basePathFromServers extracts the path component (e.g. "/v2") of the
// document's first declared server URL, if any — OpenAPI operations are
// only ever documented relative to that base, so a spec declaring
// `servers: [{url: "https://api.example.com/v2"}]` and a path of "/orders"
// describes a real endpoint at "/v2/orders", not "/orders".
func basePathFromServers(servers openapi3.Servers) string {
	if len(servers) == 0 || servers[0] == nil {
		return ""
	}
	u, err := url.Parse(servers[0].URL)
	if err != nil {
		return ""
	}
	base := strings.TrimSuffix(u.Path, "/")
	if base == "" || strings.Contains(base, "{") {
		// A templated base path (e.g. "/{tenant}/v2") has no fixed value to
		// prepend without a chosen server variable default; skip rather than
		// emit an unmatchable literal "{tenant}" path segment.
		return ""
	}
	if !strings.HasPrefix(base, "/") {
		// A relative server URL with no leading slash (e.g. "servers:
		// [{url: 'v2'}]" — legal OpenAPI, url.Parse("v2").Path == "v2" with
		// no host) used to produce a scaffolded path like "v2/orders"
		// instead of "/v2/orders". chi.Mux.MethodFunc panics on any pattern
		// not starting with '/'; registerRestRoute's recover swallows that
		// panic per-mock, so the import call still reported 201 Created
		// while the mock silently never got a route at all.
		base = "/" + base
	}
	return base
}

func buildOperation(method, path string, op *openapi3.Operation) ScaffoldOperation {
	name := op.OperationID
	if name == "" {
		name = method + " " + path
	}

	params := paramsFromOperation(op)
	statusCode, contentType, exampleJSON := firstSuccessResponse(op.Responses)

	return ScaffoldOperation{
		Name:        name,
		Method:      strings.ToUpper(method),
		Path:        path,
		StatusCode:  statusCode,
		ExampleJSON: exampleJSON,
		ContentType: contentType,
		Params:      params,
	}
}

func paramsFromOperation(op *openapi3.Operation) []Param {
	var params []Param
	for _, ref := range op.Parameters {
		if ref.Value == nil {
			continue
		}
		p := ref.Value
		params = append(params, Param{In: p.In, Name: p.Name, Required: p.Required})
	}

	if op.RequestBody == nil || op.RequestBody.Value == nil {
		return params
	}
	mt := firstMediaType(op.RequestBody.Value.Content)
	if mt == nil || mt.Schema == nil || mt.Schema.Value == nil {
		return params
	}
	for _, name := range mt.Schema.Value.Required {
		params = append(params, Param{In: "body", Name: name, Required: true})
	}
	return params
}

// firstMediaType prefers "application/json" (the common case, and the only
// one ExampleJSON/exampleFromSchema render as anything richer than an
// opaque stub string), falling back to whatever content type IS declared
// so a spec that only documents e.g. "application/xml" or
// "application/x-www-form-urlencoded" still yields a scaffolded body and
// required-field list instead of silently producing nothing.
func firstMediaType(content openapi3.Content) *openapi3.MediaType {
	if mt := content.Get(jsonContentType); mt != nil {
		return mt
	}
	for _, key := range sortedContentKeys(content) {
		return content[key]
	}
	return nil
}

func sortedContentKeys(content openapi3.Content) []string {
	keys := make([]string, 0, len(content))
	for k := range content {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// firstSuccessResponse picks the lowest 2xx status code declared and
// renders its example (explicit example/examples, else synthesized from
// its schema) alongside the actual media type it came from — a spec
// documenting only e.g. "application/xml" responses should scaffold a mock
// serving Content-Type: application/xml, not a hardcoded JSON assumption.
func firstSuccessResponse(responses *openapi3.Responses) (int, string, string) {
	if responses == nil {
		return 200, jsonContentType, "{}"
	}

	bestCode := 0
	var bestRef *openapi3.ResponseRef
	for _, key := range responses.Keys() {
		code, err := strconv.Atoi(key)
		if err != nil || code < 200 || code >= 300 {
			continue
		}
		if bestRef == nil || code < bestCode {
			bestCode, bestRef = code, responses.Value(key)
		}
	}
	if bestRef == nil || bestRef.Value == nil {
		return 200, jsonContentType, "{}"
	}

	contentType, mt := firstMediaTypeWithKey(bestRef.Value.Content)
	if mt == nil {
		return bestCode, jsonContentType, "{}"
	}
	return bestCode, contentType, renderExample(mt)
}

func firstMediaTypeWithKey(content openapi3.Content) (string, *openapi3.MediaType) {
	if mt := content.Get(jsonContentType); mt != nil {
		return jsonContentType, mt
	}
	for _, key := range sortedContentKeys(content) {
		return key, content[key]
	}
	return "", nil
}

func renderExample(mt *openapi3.MediaType) string {
	if mt.Example != nil {
		if b, err := json.Marshal(mt.Example); err == nil {
			return string(b)
		}
	}
	for _, exRef := range mt.Examples {
		if exRef.Value != nil && exRef.Value.Value != nil {
			if b, err := json.Marshal(exRef.Value.Value); err == nil {
				return string(b)
			}
		}
	}
	if mt.Schema != nil && mt.Schema.Value != nil {
		if b, err := json.Marshal(exampleFromSchema(mt.Schema.Value, 0)); err == nil {
			return string(b)
		}
	}
	return "{}"
}

// exampleFromSchema synthesizes a plausible stub value from a schema when
// the spec gives no explicit example — depth-limited since schemas can
// reference themselves recursively.
func exampleFromSchema(schema *openapi3.Schema, depth int) any {
	if schema == nil || depth > 6 {
		return nil
	}
	if schema.Example != nil {
		return schema.Example
	}
	if len(schema.Enum) > 0 {
		return schema.Enum[0]
	}
	if schema.Type == nil {
		return nil
	}

	switch {
	case schema.Type.Is("object"):
		obj := map[string]any{}
		for name, propRef := range schema.Properties {
			if propRef != nil && propRef.Value != nil {
				obj[name] = exampleFromSchema(propRef.Value, depth+1)
			}
		}
		return obj
	case schema.Type.Is("array"):
		if schema.Items != nil && schema.Items.Value != nil {
			return []any{exampleFromSchema(schema.Items.Value, depth+1)}
		}
		return []any{}
	case schema.Type.Is("string"):
		return "string"
	case schema.Type.Is("integer"):
		return 0
	case schema.Type.Is("number"):
		return 0
	case schema.Type.Is("boolean"):
		return false
	default:
		return nil
	}
}
