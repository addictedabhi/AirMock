// Package soapui reads a SoapUI project export (a *.xml file, SoapUI's own
// project-save format — a completely different schema from a WSDL, despite
// a SoapUI project usually also embedding one) into two things AirMock
// already knows how to use: a Collection (from the project's TestSuites'
// request-shaped TestSteps) and a set of mock scaffolds (from the
// project's MockServices), one per mock operation — the SoapUI counterpart
// of importing a Postman collection and importing a WSDL, respectively.
//
// Deliberately not a full SoapUI project model: only the elements needed
// to extract "what request did this step send" and "what response should
// this mock operation return" are modeled, matching internal/wsdl's own
// "enough to scaffold mocks, not a complete client" scope. Struct tags
// match on LOCAL element name only (e.g. `xml:"testStep"`, not
// `xml:"con:testStep"`) — encoding/xml already ignores an element's
// namespace prefix/URI when a tag has none of its own, so this reads a
// real project's "con:"-prefixed elements (from SoapUI's
// http://eviware.com/soapui/config namespace) without needing that URI
// declared anywhere, the same tolerance internal/wsdl relies on for WSDL's
// own namespace prefixes.
package soapui

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strconv"
	"strings"

	"github.com/google/uuid"

	"github.com/addictedabhi/airmock/internal/apiclient"
	"github.com/addictedabhi/airmock/internal/wsdl"
)

type project struct {
	XMLName          xml.Name          `xml:"soapui-project"`
	Name             string            `xml:"name,attr"`
	TestSuites       []testSuite       `xml:"testSuite"`
	Interfaces       []soapInterface   `xml:"interface"`
	MockServices     []mockService     `xml:"mockService"`
	RestMockServices []restMockService `xml:"restMockService"`
}

// wrongDocumentTypeError checks whether data's root element is something
// other than <soapui-project> and, if so, returns a friendlier error than
// encoding/xml's own "expected element type <soapui-project> but have
// <X>" — technically accurate but reads as an internal error rather than
// "you pasted the wrong kind of document". Most commonly a plain WSDL
// (root <definitions>) pasted where a full SOAP Project XML export was
// expected — the two commonly travel together (a SOAP Project XML export
// embeds a WSDL) but are entirely different schemas, see the package doc
// comment. Returns nil when the root can't be determined at all (empty/
// malformed input) or genuinely is <soapui-project> (so the caller falls
// back to encoding/xml's own error, whatever else made it fail).
func wrongDocumentTypeError(data []byte) error {
	root := rootElementName(data)
	if root == "" || root == "soapui-project" {
		return nil
	}
	hint := ""
	if root == "definitions" {
		hint = " — a plain WSDL needs its own \"Import WSDL\" instead"
	}
	return fmt.Errorf("expected a SOAP Project XML export (root element <soapui-project>), but found <%s>%s", root, hint)
}

func rootElementName(data []byte) string {
	dec := xml.NewDecoder(bytes.NewReader(data))
	for {
		tok, err := dec.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}

// soapInterface is the WSDL-derived interface a project's operations and
// their saved sample requests hang off of — a real project commonly has
// NO testSuite at all (nothing automated, just manual "try it" requests),
// in which case these interface-level calls are the ONLY requests worth
// carrying into a Collection.
type soapInterface struct {
	Name       string          `xml:"name,attr"`
	Operations []soapOperation `xml:"operation"`
	// DefinitionCache holds the real WSDL SoapUI embedded when it imported
	// this interface — the only place a call's actual SOAPAction value
	// lives; the SoapUI project config itself never repeats it per
	// operation/call.
	DefinitionCache *definitionCache `xml:"definitionCache"`
}

type definitionCache struct {
	Parts []definitionPart `xml:"part"`
}

type definitionPart struct {
	Content string `xml:"content"`
}

type soapOperation struct {
	Name  string     `xml:"name,attr"`
	Calls []soapCall `xml:"call"`
}

// soapCall is one saved sample request for an operation (SoapUI lets you
// keep several, e.g. "Request 1", "Request 2", each with its own endpoint
// override) — every one becomes its own collection item rather than only
// the first, since unlike a mock's canned response there's no single
// "the" request to prefer here.
type soapCall struct {
	Name     string `xml:"name,attr"`
	Endpoint string `xml:"endpoint"`
	Request  string `xml:"request"`
}

type testSuite struct {
	Name      string     `xml:"name,attr"`
	TestCases []testCase `xml:"testCase"`
}

type testCase struct {
	Name      string     `xml:"name,attr"`
	TestSteps []testStep `xml:"testStep"`
}

// testStep's Type attribute is SoapUI's own step-kind discriminator
// ("request" for a SOAP call, "restrequest" for a REST call, "groovy",
// "delay", "properties", "transfer", ... for everything else) — only the
// two request-shaped kinds have an endpoint+body worth carrying into a
// collection, so every other kind is silently skipped rather than
// half-imported as a meaningless empty request.
type testStep struct {
	Name   string     `xml:"name,attr"`
	Type   string     `xml:"type,attr"`
	Config stepConfig `xml:"config"`
}

type stepConfig struct {
	// SoapUI's own schema really does nest a "request" config object (with
	// name/endpoint/settings) inside the testStep's "request" element,
	// which itself holds ANOTHER "request" child carrying the raw XML
	// envelope body — the two never collide since encoding/xml matches
	// each level's children independently, but it reads oddly at a glance
	// if you don't already know that's how SoapUI itself nests it.
	Request     *soapRequestConfig `xml:"request"`
	RestRequest *restRequestConfig `xml:"restRequest"`
}

type soapRequestConfig struct {
	Endpoint string `xml:"endpoint"`
	Request  string `xml:"request"`
}

type restRequestConfig struct {
	Method   string `xml:"method,attr"`
	Endpoint string `xml:"endpoint"`
	Request  string `xml:"request"`
}

type mockService struct {
	Name           string          `xml:"name,attr"`
	Path           string          `xml:"path,attr"`
	MockOperations []mockOperation `xml:"mockOperation"`
}

type mockOperation struct {
	Name      string `xml:"name,attr"`
	Interface string `xml:"interface,attr"`
	Operation string `xml:"operation,attr"`
	// Confirmed against a real SoapUI export: despite the containing
	// element being <con:mockOperation>, each of its canned responses is
	// named plainly <con:response> — NOT <con:mockResponse> as the name
	// "mockOperation" might suggest.
	MockResponses []mockResponse `xml:"response"`
}

// mockResponse — a mock operation can carry several (SoapUI dispatches
// between them via a script or round-robin), but AirMock's own mock model
// is one static response per mock, so ImportMockServices always takes the
// first — same simplification internal/wsdl's stub envelope already makes
// (one canned response per operation), just choosing an existing response
// body instead of inventing a stub one.
type mockResponse struct {
	Name               string `xml:"name,attr"`
	HTTPResponseStatus string `xml:"httpResponseStatus,attr"`
	ResponseContent    string `xml:"responseContent"`
}

// restMockService is a completely separate top-level construct from
// mockService — SoapUI models REST mocking and SOAP mocking as two
// unrelated schemas rather than one generic "mockService", so both must be
// parsed independently and merged by ImportMockServices.
type restMockService struct {
	Name    string           `xml:"name,attr"`
	Path    string           `xml:"path,attr"`
	Actions []restMockAction `xml:"restMockAction"`
}

type restMockAction struct {
	Name         string         `xml:"name,attr"`
	Method       string         `xml:"method,attr"`
	ResourcePath string         `xml:"resourcePath,attr"`
	Responses    []mockResponse `xml:"response"`
}

// cleanBody trims a request/response body and normalizes a cosmetic SoapUI
// quirk: its own auto-generated "blank sample request" template (the one
// listing every field with an "Optional:" comment) writes literal `\r\n`/
// `\r` text in place of real line breaks in some versions, rather than an
// actual carriage return byte — a real SOAP/REST body never legitimately
// contains that literal two-character escape sequence, so it's always safe
// to fold back into a newline here.
func cleanBody(s string) string {
	// Confirmed byte-for-byte against a real export: the literal sequence
	// is backslash + 'r' immediately followed by a REAL newline byte (never
	// a literal \r\n foursome, and never a literal \r on its own) — folding
	// that 3-byte sequence into a single real newline first avoids leaving
	// a blank line behind; the second, catch-all replace only ever fires
	// for a literal \r with no following real newline (line-final, say).
	s = strings.ReplaceAll(s, "\\r\n", "\n")
	s = strings.ReplaceAll(s, `\r`, "\n")
	return strings.TrimSpace(s)
}

// soapActionsByOperation extracts each operation's real SOAPAction value
// from the WSDL SoapUI embedded alongside the interface — reusing
// internal/wsdl's own parser (the same one importWSDL uses) rather than
// re-deriving SOAPAction from the SoapUI config, which never repeats it
// anywhere itself. Merges every interface's operations into one map since
// operation names are unique enough in practice for this best-effort
// lookup; a WSDL that fails to parse (or isn't embedded at all) just
// leaves those operations without a SOAPAction, same as today.
func soapActionsByOperation(ifaces []soapInterface) map[string]string {
	out := map[string]string{}
	for _, iface := range ifaces {
		if iface.DefinitionCache == nil {
			continue
		}
		for _, part := range iface.DefinitionCache.Parts {
			ops, err := wsdl.Parse([]byte(part.Content))
			if err != nil {
				continue
			}
			for _, op := range ops {
				if op.SOAPAction != "" {
					out[op.Name] = op.SOAPAction
				}
			}
		}
	}
	return out
}

// firstMockResponseByOperation indexes each SOAP mock operation's first
// canned response by operation name, so a collection request imported from
// the SAME project's interface calls can carry the matching mock's
// response along as a saved Example — the request and its mock share an
// operation name even though SoapUI stores them in completely unrelated
// places (con:interface vs con:mockService).
func firstMockResponseByOperation(services []mockService) map[string]mockResponse {
	out := map[string]mockResponse{}
	for _, ms := range services {
		for _, op := range ms.MockOperations {
			if len(op.MockResponses) == 0 {
				continue
			}
			name := op.Operation
			if name == "" {
				name = op.Name
			}
			if _, exists := out[name]; !exists {
				out[name] = op.MockResponses[0]
			}
		}
	}
	return out
}

func exampleFromMockResponse(mr mockResponse) apiclient.Example {
	return apiclient.Example{
		ID:         uuid.NewString(),
		Name:       "Mock response",
		StatusCode: statusCodeOrDefault(mr.HTTPResponseStatus),
		Headers:    map[string]string{"Content-Type": "text/xml; charset=utf-8"},
		Body:       cleanBody(mr.ResponseContent),
	}
}

// ImportCollection parses a SoapUI project export into a Collection: one
// top-level folder per TestSuite, one nested folder per TestCase, one
// request Item per request-shaped TestStep — preserving the project's own
// organization rather than flattening every step into one list, since a
// real SoapUI project commonly groups related calls into test cases on
// purpose. Interface-level sample requests (each operation's own saved
// "call"s) are imported the same way, one top-level folder per interface —
// a project with no TestSuites at all (nothing automated, only manually
// saved requests) still yields a non-empty collection.
func ImportCollection(data []byte) (*apiclient.Collection, error) {
	var p project
	if err := xml.Unmarshal(data, &p); err != nil {
		if friendly := wrongDocumentTypeError(data); friendly != nil {
			return nil, friendly
		}
		return nil, fmt.Errorf("parse soapui project: %w", err)
	}
	soapActions := soapActionsByOperation(p.Interfaces)
	mockExamples := firstMockResponseByOperation(p.MockServices)

	items := make([]apiclient.Item, 0, len(p.TestSuites)+len(p.Interfaces))
	for _, ts := range p.TestSuites {
		items = append(items, apiclient.Item{
			Type:  apiclient.ItemFolder,
			Name:  ts.Name,
			Items: testCasesToItems(ts.TestCases),
		})
	}
	for _, iface := range p.Interfaces {
		folder := operationsToItems(iface.Operations, soapActions, mockExamples)
		if len(folder) == 0 {
			continue
		}
		items = append(items, apiclient.Item{Type: apiclient.ItemFolder, Name: iface.Name, Items: folder})
	}
	return &apiclient.Collection{Name: p.Name, Items: items}, nil
}

func operationsToItems(ops []soapOperation, soapActions map[string]string, mockExamples map[string]mockResponse) []apiclient.Item {
	items := make([]apiclient.Item, 0, len(ops))
	for _, op := range ops {
		var example *apiclient.Example
		if mr, ok := mockExamples[op.Name]; ok {
			e := exampleFromMockResponse(mr)
			example = &e
		}
		calls := callsToItems(op.Calls, soapActions[op.Name], example)
		if len(calls) == 0 {
			continue
		}
		items = append(items, apiclient.Item{Type: apiclient.ItemFolder, Name: op.Name, Items: calls})
	}
	return items
}

func callsToItems(calls []soapCall, soapAction string, example *apiclient.Example) []apiclient.Item {
	items := make([]apiclient.Item, 0, len(calls))
	for _, c := range calls {
		// SOAP-over-HTTP requires Content-Type and (for operation dispatch
		// on the receiving end) SOAPAction — neither is optional for the
		// request to actually work against a real endpoint or an AirMock
		// SOAP mock, so both are set here rather than left for the user to
		// discover and add by hand.
		headers := []apiclient.KV{{Key: "Content-Type", Value: "text/xml; charset=utf-8"}}
		if soapAction != "" {
			headers = append(headers, apiclient.KV{Key: "SOAPAction", Value: soapAction})
		}
		item := apiclient.Item{
			Type: apiclient.ItemRequest,
			Name: c.Name,
			// A SOAP call is always POST, same convention as a testStep's
			// own SOAP request (see requestSpecForStep) — the body is the
			// raw envelope XML SoapUI had saved for this call.
			Request: &apiclient.RequestSpec{Method: "POST", URL: c.Endpoint, Body: cleanBody(c.Request), RawContentType: "xml", Headers: headers},
		}
		if example != nil {
			ex := *example
			ex.ID = uuid.NewString()
			item.Examples = []apiclient.Example{ex}
		}
		items = append(items, item)
	}
	return items
}

func testCasesToItems(cases []testCase) []apiclient.Item {
	items := make([]apiclient.Item, 0, len(cases))
	for _, tc := range cases {
		items = append(items, apiclient.Item{
			Type:  apiclient.ItemFolder,
			Name:  tc.Name,
			Items: testStepsToItems(tc.TestSteps),
		})
	}
	return items
}

func testStepsToItems(steps []testStep) []apiclient.Item {
	items := make([]apiclient.Item, 0, len(steps))
	for _, step := range steps {
		spec := requestSpecForStep(step)
		if spec == nil {
			continue
		}
		items = append(items, apiclient.Item{Type: apiclient.ItemRequest, Name: step.Name, Request: spec})
	}
	return items
}

func requestSpecForStep(step testStep) *apiclient.RequestSpec {
	switch {
	case step.Config.RestRequest != nil:
		rr := step.Config.RestRequest
		method := rr.Method
		if method == "" {
			method = "GET"
		}
		return &apiclient.RequestSpec{Method: strings.ToUpper(method), URL: rr.Endpoint, Body: cleanBody(rr.Request)}
	case step.Config.Request != nil:
		// A SOAP call step: always POST (SOAP-over-HTTP's own convention,
		// same as every AirMock SOAP mock/request already assumes), body is
		// the raw envelope XML SoapUI had saved for this step.
		sr := step.Config.Request
		return &apiclient.RequestSpec{Method: "POST", URL: sr.Endpoint, Body: cleanBody(sr.Request), RawContentType: "xml"}
	default:
		return nil
	}
}

// MockServiceScaffold is one mock operation/action ready to become an
// AirMock mock — the SoapUI-mock-import counterpart of
// internal/wsdl.Operation, carrying a canned response body instead of
// leaving one to be stubbed. Protocol distinguishes the two unrelated
// SoapUI mocking schemas this scaffolds from: "soap" (a mockService
// operation, dispatched by OperationName on a shared POST endpoint) and
// "rest" (a restMockService action, dispatched by its own Method+PathPattern).
type MockServiceScaffold struct {
	ServiceName   string // the owning (rest)mockService's name, for a disambiguating suffix if operation/action names collide across services
	Protocol      string // "soap" | "rest"
	OperationName string // soap only
	SOAPAction    string // soap only — from the project's own embedded WSDL, when present
	Method        string // rest only
	PathPattern   string
	ResponseBody  string
	StatusCode    int
}

// ImportMockServices parses a SoapUI project export's MockServices and
// RestMockServices into one scaffold per operation/action, each carrying
// its first response's body (see mockResponse's own doc comment on why
// only the first). A service with no operations/actions, or one with no
// responses at all, is skipped — nothing meaningful to scaffold a mock
// from either way.
func ImportMockServices(data []byte) ([]MockServiceScaffold, error) {
	var p project
	if err := xml.Unmarshal(data, &p); err != nil {
		if friendly := wrongDocumentTypeError(data); friendly != nil {
			return nil, friendly
		}
		return nil, fmt.Errorf("parse soapui project: %w", err)
	}
	soapActions := soapActionsByOperation(p.Interfaces)

	out := []MockServiceScaffold{}
	for _, ms := range p.MockServices {
		out = append(out, soapScaffolds(ms, soapActions)...)
	}
	for _, rms := range p.RestMockServices {
		out = append(out, restScaffolds(rms)...)
	}
	return out, nil
}

func soapScaffolds(ms mockService, soapActions map[string]string) []MockServiceScaffold {
	path := ms.Path
	if path == "" {
		path = "/" + ms.Name
	}
	out := []MockServiceScaffold{}
	for _, op := range ms.MockOperations {
		if len(op.MockResponses) == 0 {
			continue
		}
		name := op.Operation
		if name == "" {
			name = op.Name
		}
		resp := op.MockResponses[0]
		out = append(out, MockServiceScaffold{
			ServiceName:   ms.Name,
			Protocol:      "soap",
			OperationName: name,
			SOAPAction:    soapActions[name],
			PathPattern:   path,
			ResponseBody:  cleanBody(resp.ResponseContent),
			StatusCode:    statusCodeOrDefault(resp.HTTPResponseStatus),
		})
	}
	return out
}

func restScaffolds(rms restMockService) []MockServiceScaffold {
	out := []MockServiceScaffold{}
	for _, action := range rms.Actions {
		if len(action.Responses) == 0 {
			continue
		}
		path := action.ResourcePath
		if path == "" {
			path = action.Name
		}
		if path == "" {
			path = "/" + rms.Name
		}
		method := action.Method
		if method == "" {
			method = "GET"
		}
		resp := action.Responses[0]
		out = append(out, MockServiceScaffold{
			ServiceName:  rms.Name,
			Protocol:     "rest",
			Method:       strings.ToUpper(method),
			PathPattern:  path,
			ResponseBody: cleanBody(resp.ResponseContent),
			StatusCode:   statusCodeOrDefault(resp.HTTPResponseStatus),
		})
	}
	return out
}

// statusCodeOrDefault parses SoapUI's own httpResponseStatus attribute
// (absent on most hand-authored SOAP mock responses, since SOAP faults
// aside it's almost always 200), falling back to 200 for anything missing
// or unparseable rather than failing the whole import over it.
func statusCodeOrDefault(raw string) int {
	if code, err := strconv.Atoi(strings.TrimSpace(raw)); err == nil && code > 0 {
		return code
	}
	return 200
}
