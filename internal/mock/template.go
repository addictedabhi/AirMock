package mock

import (
	"bytes"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"text/template"
	"time"

	"github.com/Masterminds/sprig/v3"
	"github.com/brianvoe/gofakeit/v7"
)

// RequestContext is exposed to response templates as {{.Request.*}}.
type RequestContext struct {
	Body       any               // parsed JSON body, or nil/raw string if not JSON
	BodyBytes  []byte            // raw body, kept alongside Body for gjson-based callback URL extraction
	Query      map[string]string // first value per query param
	Header     map[string]string // first value per header
	PathParams map[string]string
}

type templateData struct {
	Request RequestContext
}

// RenderOptions scopes a single RenderBody call to whichever mock or
// scheduled event it's rendering for — needed so the counter()/csv()
// template functions know whose counters/CSV rows to read and write.
// OwnerID is a mock's ID or a scheduled event's ID (both plain UUID
// strings, so either can be passed without the render engine caring which
// kind of owner it is). DynamicValues is nil-safe: counter()/csv() return a
// template execution error (rather than panicking) if it's nil, which only
// happens for a caller that hasn't wired one up.
type RenderOptions struct {
	OwnerID       string
	DynamicValues DynamicValueSource
}

func funcMap(opts RenderOptions) template.FuncMap {
	fm := sprig.TxtFuncMap()
	fm["fake"] = func(kind string, args ...string) string { return fakeValue(kind, args...) }
	fm["counter"] = func(name string, step ...int64) (int64, error) {
		if opts.DynamicValues == nil {
			return 0, fmt.Errorf("counter %q: no dynamic-value source configured for this mock", name)
		}
		s := int64(1)
		if len(step) > 0 {
			s = step[0]
		}
		return opts.DynamicValues.Counter(opts.OwnerID, name, s)
	}
	// csvRow is lazily fetched at most once per RenderBody execution (one
	// funcMap() call = one template.Execute() pass) and reused by every
	// csv() call within it, so {{csv "name"}} and {{csv "email"}} in the
	// same template refer to the same underlying row rather than each
	// independently advancing/re-rolling the row selection.
	var csvRow map[string]string
	var csvFetched bool
	fm["csv"] = func(column string) (string, error) {
		if !csvFetched {
			csvFetched = true
			if opts.DynamicValues == nil {
				return "", fmt.Errorf("csv %q: no dynamic-value source configured for this mock", column)
			}
			row, ok, err := opts.DynamicValues.NextCSVRow(opts.OwnerID)
			if err != nil {
				return "", fmt.Errorf("csv %q: %w", column, err)
			}
			if ok {
				csvRow = row
			}
		}
		if csvRow == nil {
			return "", fmt.Errorf("csv %q: no CSV attached to this mock", column)
		}
		return csvRow[column], nil
	}
	return fm
}

// fakeValue backs the "fake" template function — kind selects which piece
// of fake data to generate; args are only consulted by kinds that take
// them ("number" reads an optional min/max pair). An unrecognized kind
// returns "" (unchanged from before this was widened past 4 kinds), so an
// existing {{fake "uuid"}}/{{fake "email"}}/{{fake "name"}}/{{fake "phone"}}
// call keeps behaving exactly as it did.
func fakeValue(kind string, args ...string) string {
	switch kind {
	case "uuid":
		return gofakeit.UUID()
	case "email":
		return gofakeit.Email()
	case "name":
		return gofakeit.Name()
	case "firstname":
		return gofakeit.FirstName()
	case "lastname":
		return gofakeit.LastName()
	case "phone":
		return gofakeit.Phone()
	case "address":
		return gofakeit.Address().Address
	case "city":
		return gofakeit.City()
	case "state":
		return gofakeit.State()
	case "zipcode":
		return gofakeit.Zip()
	case "country":
		return gofakeit.Country()
	case "company":
		return gofakeit.Company()
	case "jobtitle":
		return gofakeit.JobTitle()
	case "word":
		return gofakeit.Word()
	case "sentence":
		return gofakeit.Sentence()
	case "paragraph":
		return gofakeit.Paragraph()
	case "date":
		return gofakeit.Date().Format(time.RFC3339)
	case "pastdate":
		return gofakeit.PastDate().Format(time.RFC3339)
	case "futuredate":
		return gofakeit.FutureDate().Format(time.RFC3339)
	case "number":
		min, max := 0, 100
		if len(args) >= 1 {
			if v, err := strconv.Atoi(args[0]); err == nil {
				min = v
			}
		}
		if len(args) >= 2 {
			if v, err := strconv.Atoi(args[1]); err == nil {
				max = v
			}
		}
		return strconv.Itoa(gofakeit.Number(min, max))
	case "bool":
		return strconv.FormatBool(gofakeit.Bool())
	case "creditcardnumber":
		return gofakeit.CreditCardNumber(nil)
	case "ipv4":
		return gofakeit.IPv4Address()
	case "username":
		return gofakeit.Username()
	case "color":
		return gofakeit.Color()
	case "hexcolor":
		return gofakeit.HexColor()
	case "currency":
		return gofakeit.Currency().Short
	default:
		return ""
	}
}

// RenderBody executes a mock's BodyTemplate against the given request
// context, scoped to opts's owner for any counter()/csv() calls it makes.
func RenderBody(bodyTemplate string, reqCtx RequestContext, opts RenderOptions) (string, error) {
	tmpl, err := template.New("body").Funcs(funcMap(opts)).Parse(bodyTemplate)
	if err != nil {
		return "", fmt.Errorf("parse template: %w", err)
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, templateData{Request: reqCtx}); err != nil {
		return "", fmt.Errorf("execute template: %w", err)
	}
	return buf.String(), nil
}

// BuildRequestContext extracts a RequestContext from a live *http.Request,
// its already-read body bytes, and any path params already resolved by the router.
func BuildRequestContext(r *http.Request, bodyBytes []byte, pathParams map[string]string) RequestContext {
	ctx := RequestContext{
		BodyBytes:  bodyBytes,
		Query:      firstValues(r.URL.Query()),
		Header:     firstHeaderValues(r.Header),
		PathParams: pathParams,
	}

	if len(bodyBytes) > 0 {
		var parsed any
		if err := json.Unmarshal(bodyBytes, &parsed); err == nil {
			ctx.Body = normalizeJSONValue(parsed)
		} else {
			ctx.Body = string(bodyBytes)
		}
	}
	return ctx
}

// jsonMap/jsonArray are named wrappers around exactly what
// encoding/json.Unmarshal already produces for a JSON object/array
// (map[string]any / []any) — solely so they can implement fmt.Stringer.
// text/template's field/index access (`{{.Request.Body.orderId}}`) only
// cares about a value's reflect.Kind(), which is unchanged by naming the
// type, so drilling into a specific field keeps working exactly as before.
// What changes is a BARE `{{.Request.Body}}` (or a bare nested object/
// array field): without a Stringer, text/template falls back to fmt's
// default verb, which renders a map as "map[orderId:1234]" and a slice as
// "[1 2 3]" — technically correct but not what anyone actually wants
// showing up in a response/email body. Implementing String() as compact
// JSON instead makes bare usage render the way most people would expect.
type jsonMap map[string]any

func (m jsonMap) String() string {
	b, err := json.Marshal(map[string]any(m))
	if err != nil {
		return fmt.Sprintf("%v", map[string]any(m))
	}
	return string(b)
}

type jsonArray []any

func (a jsonArray) String() string {
	b, err := json.Marshal([]any(a))
	if err != nil {
		return fmt.Sprintf("%v", []any(a))
	}
	return string(b)
}

// normalizeJSONValue recursively rewraps every nested map/slice (not just
// the top level) so a bare reference to a nested object/array field prints
// as JSON too, not just Body as a whole.
func normalizeJSONValue(v any) any {
	switch t := v.(type) {
	case map[string]any:
		out := make(jsonMap, len(t))
		for k, val := range t {
			out[k] = normalizeJSONValue(val)
		}
		return out
	case []any:
		out := make(jsonArray, len(t))
		for i, val := range t {
			out[i] = normalizeJSONValue(val)
		}
		return out
	default:
		return v
	}
}

func firstValues(v map[string][]string) map[string]string {
	out := make(map[string]string, len(v))
	for k, vals := range v {
		if len(vals) > 0 {
			out[k] = vals[0]
		}
	}
	return out
}

func firstHeaderValues(h http.Header) map[string]string {
	out := make(map[string]string, len(h))
	for k, vals := range h {
		if len(vals) > 0 {
			out[k] = vals[0]
		}
	}
	return out
}
