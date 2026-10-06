// Package postman implements Postman Collection v2.1 as AirMock's
// collection interchange format. v2.1 has no native WebSocket-request
// concept, so a WS item round-trips through a tolerated custom
// "x-airmock-ws" key — plain Postman still reads the HTTP parts of an
// AirMock-exported collection fine; only re-importing into AirMock
// recovers the WS request in full.
package postman

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"

	"github.com/google/uuid"

	"github.com/addictedabhi/airmock/internal/apiclient"
)

const schemaURL = "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"

type Collection struct {
	Info     Info         `json:"info"`
	Item     []Item       `json:"item"`
	Variable []VariableKV `json:"variable,omitempty"`
}

// VariableKV is one entry in Postman's collection-level `variable` array —
// a distinct type from HeaderKV since a real Postman export's variable
// entries commonly include a "type" (usually "string") that headers/query
// params don't carry, and because the collection round-trip converts to/
// from a map (apiclient.Collection.Variables) rather than an ordered slice.
type VariableKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

type Info struct {
	Name   string `json:"name"`
	Schema string `json:"schema"`
}

type Item struct {
	Name       string       `json:"name"`
	Item       []Item       `json:"item,omitempty"`
	Request    *Request     `json:"request,omitempty"`
	XAirmockWS *WSExtension `json:"x-airmock-ws,omitempty"`
	// Response is Postman's own array of saved example responses for this
	// request — the "Examples" (SoapUI-language: mock responses) a user
	// can click through in Postman without re-sending the real request.
	// Cookie/_postman_previewlanguage aren't modeled: neither has an
	// apiclient.Example counterpart to round-trip into.
	Response []Response `json:"response,omitempty"`
}

// Response is one saved example response. OriginalRequest is read on
// import (harmless if absent — apiclient.Example has no place to keep it
// anyway) and always re-derived from the item's own live Request on
// export, matching how a real Postman-saved example's originalRequest is
// generally just a snapshot of the request as it was when saved.
type Response struct {
	Name            string     `json:"name"`
	OriginalRequest *Request   `json:"originalRequest,omitempty"`
	Status          string     `json:"status,omitempty"`
	Code            int        `json:"code"`
	Header          []HeaderKV `json:"header,omitempty"`
	Body            string     `json:"body,omitempty"`
}

// WSExtension is the tolerated custom key carrying a WebSocket request's
// shape — not part of the official v2.1 schema.
type WSExtension struct {
	URL     string     `json:"url"`
	Headers []HeaderKV `json:"headers,omitempty"`
	Message string     `json:"message,omitempty"`
}

type Request struct {
	Method string     `json:"method"`
	Header []HeaderKV `json:"header,omitempty"`
	URL    URL        `json:"url"`
	Body   *Body      `json:"body,omitempty"`
	Auth   *Auth      `json:"auth,omitempty"`
}

type HeaderKV struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Disabled bool   `json:"disabled,omitempty"`
}

type URL struct {
	Raw   string     `json:"raw"`
	Query []HeaderKV `json:"query,omitempty"`
}

// Body mirrors Postman's real request-body shape (mode/raw/urlencoded/
// formdata/options.raw.language) rather than a custom shape, so BodyMode,
// FormFields and RawContentType all round-trip and a plain Postman import
// of an AirMock export still shows the right body tab.
type Body struct {
	Mode       string       `json:"mode,omitempty"`
	Raw        string       `json:"raw,omitempty"`
	Options    *BodyOptions `json:"options,omitempty"`
	URLEncoded []HeaderKV   `json:"urlencoded,omitempty"`
	FormData   []FormDataKV `json:"formdata,omitempty"`
}

type BodyOptions struct {
	Raw *RawOptions `json:"raw,omitempty"`
}

type RawOptions struct {
	Language string `json:"language,omitempty"`
}

type FormDataKV struct {
	Key      string `json:"key"`
	Value    string `json:"value"`
	Type     string `json:"type,omitempty"`
	Disabled bool   `json:"disabled,omitempty"`
}

// Auth mirrors Postman's real per-request auth shape (a type discriminator
// plus a same-named array of key/value pairs) so an exported collection's
// Auth tab round-trips through AirMock and also displays correctly if
// opened in real Postman. OAuth2 is read-only on import (see authFromPostman)
// — AirMock has no OAuth2 client of its own (no token endpoint, no
// refresh), so there's nothing to export back out for it.
type Auth struct {
	Type   string   `json:"type"`
	Bearer []AuthKV `json:"bearer,omitempty"`
	Basic  []AuthKV `json:"basic,omitempty"`
	APIKey []AuthKV `json:"apikey,omitempty"`
	OAuth2 []AuthKV `json:"oauth2,omitempty"`
}

type AuthKV struct {
	Key   string `json:"key"`
	Value string `json:"value"`
	Type  string `json:"type,omitempty"`
}

// Export renders a Collection as Postman Collection v2.1 JSON.
func Export(c apiclient.Collection) ([]byte, error) {
	pc := Collection{
		Info:     Info{Name: c.Name, Schema: schemaURL},
		Item:     itemsToPostman(c.Items),
		Variable: variablesToPostman(c.Variables),
	}
	b, err := json.MarshalIndent(pc, "", "  ")
	if err != nil {
		return nil, fmt.Errorf("marshal postman collection: %w", err)
	}
	return b, nil
}

// Import parses a Postman Collection v2.1 JSON document into a Collection.
func Import(data []byte) (*apiclient.Collection, error) {
	var pc Collection
	if err := json.Unmarshal(data, &pc); err != nil {
		return nil, fmt.Errorf("parse postman collection: %w", err)
	}
	return &apiclient.Collection{
		Name:      pc.Info.Name,
		Items:     itemsFromPostman(pc.Item),
		Variables: variablesFromPostman(pc.Variable),
	}, nil
}

// variablesToPostman/variablesFromPostman convert between AirMock's flat
// map (matching how Environment.Variables already works, so the same
// SubstituteVars call sites work on either) and Postman's ordered
// {key,value,type} array — a real Postman collection commonly carries one
// of these for e.g. a base URL every request in the collection references,
// previously silently discarded on import.
func variablesToPostman(vars map[string]string) []VariableKV {
	if len(vars) == 0 {
		return nil
	}
	keys := make([]string, 0, len(vars))
	for k := range vars {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]VariableKV, len(keys))
	for i, k := range keys {
		out[i] = VariableKV{Key: k, Value: vars[k], Type: "string"}
	}
	return out
}

func variablesFromPostman(vars []VariableKV) map[string]string {
	if len(vars) == 0 {
		return nil
	}
	out := make(map[string]string, len(vars))
	for _, v := range vars {
		out[v.Key] = v.Value
	}
	return out
}

// environmentFile is Postman's separate *.postman_environment.json export
// shape — a different file/schema from a collection, downloaded and
// imported independently in real Postman too.
type environmentFile struct {
	Name   string           `json:"name"`
	Values []environmentVar `json:"values"`
}

type environmentVar struct {
	Key     string `json:"key"`
	Value   string `json:"value"`
	Enabled *bool  `json:"enabled,omitempty"`
}

// ImportEnvironment parses a Postman *.postman_environment.json document —
// previously there was no way to bring one in at all, only a collection.
// A variable explicitly marked disabled (enabled:false) is skipped: Postman
// keeps disabled variables present-but-inactive, but apiclient.Environment
// has no per-variable enabled flag to preserve that distinction, so
// carrying a disabled one over as if active would silently change what a
// substitution resolves to.
func ImportEnvironment(data []byte) (*apiclient.Environment, error) {
	var ef environmentFile
	if err := json.Unmarshal(data, &ef); err != nil {
		return nil, fmt.Errorf("parse postman environment: %w", err)
	}
	vars := make(map[string]string, len(ef.Values))
	for _, v := range ef.Values {
		if v.Enabled != nil && !*v.Enabled {
			continue
		}
		vars[v.Key] = v.Value
	}
	return &apiclient.Environment{Name: ef.Name, Variables: vars}, nil
}

func itemsToPostman(items []apiclient.Item) []Item {
	out := make([]Item, 0, len(items))
	for _, it := range items {
		switch it.Type {
		case apiclient.ItemFolder:
			out = append(out, Item{Name: it.Name, Item: itemsToPostman(it.Items)})
		case apiclient.ItemWSRequest:
			out = append(out, Item{Name: it.Name, XAirmockWS: wsToExtension(it.WSRequest)})
		default: // apiclient.ItemRequest
			req := requestToPostman(it.Request)
			out = append(out, Item{Name: it.Name, Request: req, Response: examplesToPostman(it.Examples, req)})
		}
	}
	return out
}

func itemsFromPostman(items []Item) []apiclient.Item {
	out := make([]apiclient.Item, 0, len(items))
	for _, it := range items {
		switch {
		case it.XAirmockWS != nil:
			out = append(out, apiclient.Item{Type: apiclient.ItemWSRequest, Name: it.Name, WSRequest: wsFromExtension(it.XAirmockWS)})
		case len(it.Item) > 0:
			out = append(out, apiclient.Item{Type: apiclient.ItemFolder, Name: it.Name, Items: itemsFromPostman(it.Item)})
		default:
			out = append(out, apiclient.Item{
				Type:     apiclient.ItemRequest,
				Name:     it.Name,
				Request:  requestFromPostman(it.Request),
				Examples: examplesFromPostman(it.Response),
			})
		}
	}
	return out
}

// examplesToPostman/examplesFromPostman convert between apiclient's saved
// Examples and Postman's own response[] array — previously entirely
// unmodeled (Item had no Response field at all), so a real Postman
// collection's saved examples (commonly the main reason a collection is
// worth exporting/sharing at all — happy path plus every documented error
// case) were silently dropped on import, and an AirMock collection's own
// saved Examples never appeared when exported back to Postman either.
func examplesToPostman(examples []apiclient.Example, req *Request) []Response {
	if len(examples) == 0 {
		return nil
	}
	out := make([]Response, len(examples))
	for i, ex := range examples {
		out[i] = Response{
			Name:            ex.Name,
			OriginalRequest: req,
			Status:          http.StatusText(ex.StatusCode),
			Code:            ex.StatusCode,
			Header:          headerMapToPostman(ex.Headers),
			Body:            ex.Body,
		}
	}
	return out
}

func examplesFromPostman(responses []Response) []apiclient.Example {
	if len(responses) == 0 {
		return nil
	}
	out := make([]apiclient.Example, len(responses))
	for i, r := range responses {
		out[i] = apiclient.Example{
			ID:         uuid.NewString(),
			Name:       r.Name,
			StatusCode: r.Code,
			Headers:    headerMapFromPostman(r.Header),
			Body:       r.Body,
		}
	}
	return out
}

// headerMapToPostman/headerMapFromPostman convert between apiclient's
// header map (Example.Headers) and Postman's ordered header array — a
// real response commonly repeats a header name (e.g. multiple "Vary"
// entries) more than once, which the map form can't preserve; unavoidable
// given Example.Headers' own type, not something introduced here.
func headerMapToPostman(h map[string]string) []HeaderKV {
	if len(h) == 0 {
		return nil
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]HeaderKV, len(keys))
	for i, k := range keys {
		out[i] = HeaderKV{Key: k, Value: h[k]}
	}
	return out
}

func headerMapFromPostman(hs []HeaderKV) map[string]string {
	if len(hs) == 0 {
		return nil
	}
	out := make(map[string]string, len(hs))
	for _, h := range hs {
		out[h.Key] = h.Value
	}
	return out
}

func requestToPostman(r *apiclient.RequestSpec) *Request {
	if r == nil {
		return nil
	}
	return &Request{
		Method: r.Method,
		Header: kvToPostman(r.Headers),
		URL:    URL{Raw: r.URL, Query: kvToPostman(r.Query)},
		Body:   bodyToPostman(r),
		Auth:   authToPostman(r.Auth),
	}
}

func bodyToPostman(r *apiclient.RequestSpec) *Body {
	switch r.BodyMode {
	case "none":
		return &Body{Mode: "none"}
	case "urlencoded":
		return &Body{Mode: "urlencoded", URLEncoded: kvToPostman(r.FormFields)}
	case "formdata":
		return &Body{Mode: "formdata", FormData: formDataToPostman(r.FormFields)}
	default: // "" - raw
		if r.Body == "" && r.RawContentType == "" {
			return nil
		}
		b := &Body{Mode: "raw", Raw: r.Body}
		if r.RawContentType != "" {
			b.Options = &BodyOptions{Raw: &RawOptions{Language: r.RawContentType}}
		}
		return b
	}
}

func requestFromPostman(r *Request) *apiclient.RequestSpec {
	if r == nil {
		return &apiclient.RequestSpec{Method: "GET"}
	}
	spec := &apiclient.RequestSpec{
		Method:  r.Method,
		URL:     r.URL.Raw,
		Headers: kvFromPostman(r.Header),
		Query:   kvFromPostman(r.URL.Query),
		Auth:    authFromPostman(r.Auth),
	}
	if r.Body != nil {
		switch r.Body.Mode {
		case "none":
			spec.BodyMode = "none"
		case "urlencoded":
			spec.BodyMode = "urlencoded"
			spec.FormFields = kvFromPostman(r.Body.URLEncoded)
		case "formdata":
			spec.BodyMode = "formdata"
			spec.FormFields = formDataFromPostman(r.Body.FormData)
		default: // "raw" or unset
			spec.Body = r.Body.Raw
			if r.Body.Options != nil && r.Body.Options.Raw != nil {
				spec.RawContentType = r.Body.Options.Raw.Language
			}
		}
	}
	return spec
}

func authToPostman(a *apiclient.Auth) *Auth {
	if a == nil || a.Type == "" || a.Type == apiclient.AuthNone {
		return nil
	}
	switch a.Type {
	case apiclient.AuthBearer:
		return &Auth{Type: "bearer", Bearer: []AuthKV{{Key: "token", Value: a.Token, Type: "string"}}}
	case apiclient.AuthBasic:
		return &Auth{Type: "basic", Basic: []AuthKV{
			{Key: "username", Value: a.Username, Type: "string"},
			{Key: "password", Value: a.Password, Type: "string"},
		}}
	case apiclient.AuthAPIKey:
		addTo := a.AddTo
		if addTo == "" {
			addTo = "header"
		}
		return &Auth{Type: "apikey", APIKey: []AuthKV{
			{Key: "key", Value: a.KeyName, Type: "string"},
			{Key: "value", Value: a.KeyValue, Type: "string"},
			{Key: "in", Value: addTo, Type: "string"},
		}}
	default:
		return nil
	}
}

func authFromPostman(a *Auth) *apiclient.Auth {
	if a == nil {
		return nil
	}
	switch a.Type {
	case "bearer":
		return &apiclient.Auth{Type: apiclient.AuthBearer, Token: authKVValue(a.Bearer, "token")}
	case "basic":
		return &apiclient.Auth{
			Type:     apiclient.AuthBasic,
			Username: authKVValue(a.Basic, "username"),
			Password: authKVValue(a.Basic, "password"),
		}
	case "apikey":
		addTo := authKVValue(a.APIKey, "in")
		if addTo == "" {
			addTo = "header"
		}
		return &apiclient.Auth{
			Type:     apiclient.AuthAPIKey,
			KeyName:  authKVValue(a.APIKey, "key"),
			KeyValue: authKVValue(a.APIKey, "value"),
			AddTo:    addTo,
		}
	case "oauth2":
		// AirMock has no OAuth2 client of its own — no token endpoint, no
		// refresh flow — but Postman's oauth2 auth params commonly include
		// an already-obtained "Access Token" (cached from a prior "Get New
		// Access Token" click), which is functionally just a bearer token
		// once a request is actually sent. Importing as AuthBearer with that
		// cached token means the request still authenticates correctly,
		// instead of the credentials being silently discarded entirely (the
		// previous behavior — falling to the `default` case below, same as
		// an auth type this importer had never heard of). If no cached
		// token is present (e.g. a client-credentials flow never run),
		// there's genuinely nothing usable to carry over, so this falls
		// through to AuthNone same as before.
		if token := authKVValue(a.OAuth2, "accessToken"); token != "" {
			return &apiclient.Auth{Type: apiclient.AuthBearer, Token: token}
		}
		return &apiclient.Auth{Type: apiclient.AuthNone}
	default:
		return &apiclient.Auth{Type: apiclient.AuthNone}
	}
}

func authKVValue(kvs []AuthKV, key string) string {
	for _, kv := range kvs {
		if kv.Key == key {
			return kv.Value
		}
	}
	return ""
}

func formDataToPostman(kvs []apiclient.KV) []FormDataKV {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]FormDataKV, len(kvs))
	for i, kv := range kvs {
		out[i] = FormDataKV{Key: kv.Key, Value: kv.Value, Type: "text", Disabled: kv.Disabled}
	}
	return out
}

func formDataFromPostman(kvs []FormDataKV) []apiclient.KV {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]apiclient.KV, len(kvs))
	for i, kv := range kvs {
		out[i] = apiclient.KV{Key: kv.Key, Value: kv.Value, Disabled: kv.Disabled}
	}
	return out
}

func wsToExtension(ws *apiclient.WSRequestSpec) *WSExtension {
	if ws == nil {
		return &WSExtension{}
	}
	return &WSExtension{URL: ws.URL, Headers: kvToPostman(ws.Headers), Message: ws.Message}
}

func wsFromExtension(ws *WSExtension) *apiclient.WSRequestSpec {
	if ws == nil {
		return &apiclient.WSRequestSpec{}
	}
	return &apiclient.WSRequestSpec{URL: ws.URL, Headers: kvFromPostman(ws.Headers), Message: ws.Message}
}

func kvToPostman(kvs []apiclient.KV) []HeaderKV {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]HeaderKV, len(kvs))
	for i, kv := range kvs {
		out[i] = HeaderKV{Key: kv.Key, Value: kv.Value, Disabled: kv.Disabled}
	}
	return out
}

func kvFromPostman(kvs []HeaderKV) []apiclient.KV {
	if len(kvs) == 0 {
		return nil
	}
	out := make([]apiclient.KV, len(kvs))
	for i, kv := range kvs {
		out[i] = apiclient.KV{Key: kv.Key, Value: kv.Value, Disabled: kv.Disabled}
	}
	return out
}
