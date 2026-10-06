// Package traffic scaffolds REST mocks from traffic captured OUTSIDE this
// AirMock instance — a browser devtools "Export HAR" file, or a Postman
// collection whose requests carry saved example responses — mirroring how
// internal/openapi and internal/wsdl scaffold mocks from a spec document,
// just from real recorded exchanges instead of a spec. This is the reverse
// direction of the hit-log's own "promote to mock" action (which turns
// traffic THIS instance captured into a mock); here the traffic came from
// somewhere else entirely.
package traffic

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
)

// ScaffoldMock is one candidate REST mock derived from a captured exchange —
// deliberately the same shape as internal/openapi.ScaffoldOperation's REST
// fields. Unlike a formal OpenAPI schema, captured traffic has no declared
// "required" list — RequestBody carries the one real request that WAS
// captured, so the caller can still derive a best-effort required-field
// set from whatever top-level keys actually showed up (see
// internal/web/api/trafficimport.go's validationRulesFromSampleBody),
// rather than the request body simply being discarded as it was before.
type ScaffoldMock struct {
	Name            string
	Method          string
	Path            string // path only, no query — one mock per endpoint regardless of how many distinct query values were captured
	StatusCode      int
	ResponseHeaders map[string]string
	ResponseBody    string
	RequestBody     string // the captured request payload, if any — JSON only (best-effort; empty for GET/non-JSON)
}

const contentTypeHeader = "Content-Type"

// harFile mirrors just the fields of the HAR 1.2 format
// (http://www.softwareishard.com/blog/har-12-spec/) that scaffolding needs.
type harFile struct {
	Log struct {
		Entries []struct {
			Request struct {
				Method   string `json:"method"`
				URL      string `json:"url"`
				PostData struct {
					Text string `json:"text"`
				} `json:"postData"`
			} `json:"request"`
			Response struct {
				Status  int `json:"status"`
				Headers []struct {
					Name  string `json:"name"`
					Value string `json:"value"`
				} `json:"headers"`
				Content struct {
					MimeType string `json:"mimeType"`
					Text     string `json:"text"`
					Encoding string `json:"encoding"`
				} `json:"content"`
			} `json:"response"`
		} `json:"entries"`
	} `json:"log"`
}

// ParseHAR scaffolds one mock per distinct method+path found in a HAR file —
// the first captured exchange for a given endpoint wins when it was hit more
// than once (e.g. repeated polling), so the result is deterministic rather
// than "whichever happened to be last in the file."
func ParseHAR(data []byte) ([]ScaffoldMock, error) {
	var har harFile
	if err := json.Unmarshal(data, &har); err != nil {
		return nil, fmt.Errorf("parse HAR file: %w", err)
	}
	if len(har.Log.Entries) == 0 {
		return nil, fmt.Errorf("no entries found in HAR file")
	}

	seen := map[string]bool{}
	var out []ScaffoldMock
	for _, e := range har.Log.Entries {
		m, key, ok := scaffoldFromHAREntry(e)
		if !ok || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("no requests with both a method and URL found in HAR file")
	}
	return out, nil
}

// harEntry is harFile.Log.Entries' element type, named here purely so
// scaffoldFromHAREntry can take it as a parameter (an inline anonymous
// struct literal can't be spelled as a function's parameter type).
type harEntry = struct {
	Request struct {
		Method   string `json:"method"`
		URL      string `json:"url"`
		PostData struct {
			Text string `json:"text"`
		} `json:"postData"`
	} `json:"request"`
	Response struct {
		Status  int `json:"status"`
		Headers []struct {
			Name  string `json:"name"`
			Value string `json:"value"`
		} `json:"headers"`
		Content struct {
			MimeType string `json:"mimeType"`
			Text     string `json:"text"`
			Encoding string `json:"encoding"`
		} `json:"content"`
	} `json:"response"`
}

// scaffoldFromHAREntry builds one ScaffoldMock from a single HAR entry, or
// reports ok=false for an entry with no usable method/URL — split out of
// ParseHAR purely to keep that function's cognitive complexity down as one
// straight-line loop rather than a loop full of nested conditionals.
func scaffoldFromHAREntry(e harEntry) (m ScaffoldMock, key string, ok bool) {
	if e.Request.Method == "" || e.Request.URL == "" {
		return ScaffoldMock{}, "", false
	}
	path := pathOnly(e.Request.URL)
	if path == "" {
		return ScaffoldMock{}, "", false
	}
	method := strings.ToUpper(e.Request.Method)

	body := e.Response.Content.Text
	if e.Response.Content.Encoding == "base64" && body != "" {
		if decoded, err := base64.StdEncoding.DecodeString(body); err == nil {
			body = string(decoded)
		}
	}
	status := e.Response.Status
	if status == 0 {
		status = 200
	}
	return ScaffoldMock{
		Name:            method + " " + path,
		Method:          method,
		Path:            path,
		StatusCode:      status,
		ResponseHeaders: firstContentType(e.Response.Content.MimeType),
		ResponseBody:    body,
		RequestBody:     e.Request.PostData.Text,
	}, method + " " + path, true
}

// postmanExamplesCollection is a minimal, scaffold-only view of a Postman
// v2.1 collection — distinct from internal/apiclient/postman's Item (which
// models a request to RUN, not a saved example response to SERVE), since
// only saved examples carry the response bodies a mock needs.
type postmanExamplesCollection struct {
	Item []postmanExampleItem `json:"item"`
}

type postmanExampleItem struct {
	Name     string               `json:"name"`
	Item     []postmanExampleItem `json:"item,omitempty"` // folders
	Request  *postmanExampleReq   `json:"request,omitempty"`
	Response []postmanExampleResp `json:"response,omitempty"`
}

type postmanExampleReq struct {
	Method string            `json:"method"`
	URL    postmanExampleURL `json:"url"`
	Body   *struct {
		Raw string `json:"raw"`
	} `json:"body,omitempty"`
}

// postmanExampleURL accepts either Postman's structured URL object or (some
// exporters emit this) a bare string, without needing two separate types.
type postmanExampleURL struct {
	Raw string
}

func (u *postmanExampleURL) UnmarshalJSON(b []byte) error {
	var raw string
	if err := json.Unmarshal(b, &raw); err == nil {
		u.Raw = raw
		return nil
	}
	var obj struct {
		Raw string `json:"raw"`
	}
	if err := json.Unmarshal(b, &obj); err != nil {
		return err
	}
	u.Raw = obj.Raw
	return nil
}

type postmanExampleResp struct {
	Code   int    `json:"code"`
	Body   string `json:"body"`
	Header []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"header"`
}

// ParsePostmanExamples scaffolds one mock per request item that has at least
// one saved example response, walking folders recursively. Items with no
// saved example (a bare request with nothing ever sent/saved) are skipped —
// there's no response to scaffold from, unlike internal/apiclient/postman's
// Import, which only cares about the request side.
func ParsePostmanExamples(data []byte) ([]ScaffoldMock, error) {
	var col postmanExamplesCollection
	if err := json.Unmarshal(data, &col); err != nil {
		return nil, fmt.Errorf("parse Postman collection: %w", err)
	}
	out := collectPostmanExamples(col.Item)
	if len(out) == 0 {
		return nil, fmt.Errorf("no requests with a saved example response found in this collection")
	}
	return out, nil
}

func collectPostmanExamples(items []postmanExampleItem) []ScaffoldMock {
	var out []ScaffoldMock
	for _, it := range items {
		if len(it.Item) > 0 {
			out = append(out, collectPostmanExamples(it.Item)...)
			continue
		}
		if it.Request == nil || len(it.Response) == 0 {
			continue
		}
		path := pathOnly(it.Request.URL.Raw)
		if path == "" {
			continue
		}
		ex := it.Response[0]
		status := ex.Code
		if status == 0 {
			status = 200
		}
		name := it.Name
		if name == "" {
			name = strings.ToUpper(it.Request.Method) + " " + path
		}
		var requestBody string
		if it.Request.Body != nil {
			requestBody = it.Request.Body.Raw
		}
		out = append(out, ScaffoldMock{
			Name:            name,
			Method:          strings.ToUpper(it.Request.Method),
			Path:            path,
			StatusCode:      status,
			ResponseHeaders: headersFromPostmanExample(ex.Header),
			ResponseBody:    ex.Body,
			RequestBody:     requestBody,
		})
	}
	return out
}

func headersFromPostmanExample(headers []struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}) map[string]string {
	out := map[string]string{}
	for _, h := range headers {
		// Content-Length would be wrong once the body is re-rendered through
		// AirMock's own template engine; Content-Type is the one header
		// worth carrying over so the response Content-Type still matches.
		if strings.EqualFold(h.Key, contentTypeHeader) {
			out[contentTypeHeader] = h.Value
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func firstContentType(mimeType string) map[string]string {
	if mimeType == "" {
		return nil
	}
	// HAR's mimeType sometimes carries a "; charset=..." suffix a mock
	// response header shouldn't necessarily repeat verbatim, but keeping it
	// as-is is still a faithful reproduction of what was actually captured.
	return map[string]string{contentTypeHeader: mimeType}
}

// pathOnly reduces a full request URL (which may include {{vars}} in the
// Postman case) down to just its path — mocks match on path, not query, so
// two captures of the same endpoint with different query values still
// scaffold to one mock.
func pathOnly(raw string) string {
	// Postman raw URLs can contain {{baseUrl}}-style variables url.Parse
	// chokes on as a host; stripping anything before the first real "/"
	// after "://" (or, if there's no scheme, using the string as-is) gets a
	// clean path either way.
	cleaned := raw
	switch {
	case strings.Contains(cleaned, "://"):
		idx := strings.Index(cleaned, "://")
		rest := cleaned[idx+3:]
		if slash := strings.Index(rest, "/"); slash >= 0 {
			cleaned = rest[slash:]
		} else {
			cleaned = "/"
		}
	case strings.HasPrefix(cleaned, "{{"):
		// A Postman raw URL built on an environment variable, e.g.
		// "{{baseUrl}}/orders/123" — no "://" to key off, so strip the
		// leading "{{...}}" token instead and treat the rest as the path.
		if end := strings.Index(cleaned, "}}"); end >= 0 {
			cleaned = cleaned[end+2:]
		}
	}
	parsed, err := url.Parse(cleaned)
	if err != nil || parsed.Path == "" {
		return ""
	}
	return parsed.Path
}
