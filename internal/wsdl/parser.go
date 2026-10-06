// Package wsdl is a minimal, hand-rolled WSDL reader — enough to enumerate
// a service's operations and their SOAPAction values so AirMock can
// auto-scaffold one mock per operation. Deliberately not a client-codegen
// library like gowsdl (that solves a different problem: generating typed
// Go client code, not mock scaffolding), and deliberately tolerant of
// namespace prefixes by matching on local element names only.
package wsdl

import (
	"bytes"
	"encoding/xml"
	"fmt"
	"strings"
)

type definitions struct {
	XMLName   xml.Name      `xml:"definitions"`
	Name      string        `xml:"name,attr"`
	PortTypes []portType    `xml:"portType"`
	Bindings  []binding     `xml:"binding"`
	Services  []wsdlService `xml:"service"`
	Messages  []message     `xml:"message"`
}

type wsdlService struct {
	Name  string     `xml:"name,attr"`
	Ports []wsdlPort `xml:"port"`
}

// message/part back a portType operation's <input message="tns:X"/> — used
// only to scaffold one placeholder element per expected parameter in a
// generated request body, not to resolve the part's real structure (this
// reader deliberately doesn't parse <types>/XSD, see the package doc
// comment).
type message struct {
	Name  string `xml:"name,attr"`
	Parts []part `xml:"part"`
}

type part struct {
	Name string `xml:"name,attr"`
	// Type/Element are mutually exclusive in practice (rpc/encoded style
	// declares a part's primitive type directly; document/literal style
	// points at a schema element instead) — whichever is set becomes the
	// part's type hint, stripped of its namespace prefix for readability.
	Type    string `xml:"type,attr"`
	Element string `xml:"element,attr"`
}

type wsdlPort struct {
	// Address matches local element name only, so it finds a SOAP 1.1
	// <soap:address> or a SOAP 1.2 <soap12:address> alike (see the package
	// doc comment on namespace-prefix tolerance).
	Address soapAddress `xml:"address"`
}

type soapAddress struct {
	Location string `xml:"location,attr"`
}

type portType struct {
	Name       string      `xml:"name,attr"`
	Operations []operation `xml:"operation"`
}

type operation struct {
	Name  string       `xml:"name,attr"`
	Input operationRef `xml:"input"`
}

type operationRef struct {
	Message string `xml:"message,attr"`
}

type binding struct {
	Name       string             `xml:"name,attr"`
	Operations []bindingOperation `xml:"operation"`
}

type bindingOperation struct {
	Name          string        `xml:"name,attr"`
	SOAPOperation soapOperation `xml:"operation"`
	Input         bindingInput  `xml:"input"`
}

type soapOperation struct {
	SOAPAction string `xml:"soapAction,attr"`
}

// bindingInput carries the binding's own SOAP header declarations for an
// operation's input — a real WSDL commonly splits one input message's
// parts between the SOAP body and one or more SOAP headers (e.g. an auth
// token part sent as a header, alongside the actual request payload as the
// body), and a generated request needs to place each part correctly rather
// than dumping every part into the body regardless.
type bindingInput struct {
	Headers []soapHeaderRef `xml:"header"`
}

// soapHeaderRef is one <soap:header message="tns:X" part="Y"/> — Message
// here is redundant with the operation's own portType input message in
// every real-world case seen so far (a header part always belongs to the
// same message as the rest of the operation's input), but is still read so
// header parts can be matched by (message, part) rather than assuming that.
type soapHeaderRef struct {
	Message string `xml:"message,attr"`
	Part    string `xml:"part,attr"`
}

// Operation is one scaffoldable SOAP operation: its name (from portType)
// and the SOAPAction clients must send to invoke it (from binding), if any.
// BindingName disambiguates the rare case where more than one binding
// declares a DIFFERENT SOAPAction for the same operation name (e.g.
// separate SOAP 1.1 and 1.2 bindings for one portType) — left empty (the
// overwhelmingly common single-binding case), same as before this field
// existed. Previously a flat map keyed only by operation name meant the
// second such binding silently overwrote the first's SOAPAction with no
// way to choose, or even see, both.
type Operation struct {
	Name        string
	SOAPAction  string
	BindingName string
	// InputParts lists this operation's expected input parameters — one per
	// <message><part> the operation's <input message="..."/> resolves to,
	// each with a short type/element hint (not a real schema-resolved
	// structure). Used to scaffold a request body with one placeholder
	// element per expected parameter instead of a single opaque "fill this
	// in" comment. Empty when the WSDL declares no input message for the
	// operation, or no <message> matches it.
	InputParts []Part
}

// Part is one <message><part> — a request parameter's name, namespace-
// prefix-stripped. Exactly one of Type/Element is normally set (rpc/
// encoded style declares a part's primitive type directly; document/
// literal style, the modern common case, points at a schema element
// instead) — Element, when set, can be looked up in a SchemaSet (see
// schema.go) to recursively resolve the part's real nested structure
// rather than just a flat placeholder; Type is used as a display-only hint
// when it can't be (or wasn't attempted). Header is true when the binding
// declares this part as a SOAP header (<soap:header message="..."
// part="..."/>) rather than body content, so a generated request places it
// in <soapenv:Header> instead of alongside the actual body parts.
type Part struct {
	Name    string
	Type    string
	Element string
	Header  bool
}

// Document is the full result of reading a WSDL: its declared service name,
// the endpoint URL of its first <service><port> (empty if the WSDL declares
// none — common for a hand-trimmed excerpt, or a contract-first WSDL meant
// to be hosted somewhere the author hasn't decided yet), and its
// scaffoldable operations.
type Document struct {
	Name        string
	EndpointURL string
	Operations  []Operation
}

// ParseDocument reads a WSDL document into a Document. Parse (below) is the
// same read, returning just the Operations slice for callers that don't
// need the name/endpoint.
func ParseDocument(data []byte) (*Document, error) {
	var defs definitions
	if err := xml.Unmarshal(data, &defs); err != nil {
		if root := rootElementName(data); root != "" && root != "definitions" {
			// encoding/xml's own error here is "expected element type
			// <definitions> but have <soapui-project>" (or whatever the
			// actual root is) — technically accurate but reads as an
			// internal error rather than "you pasted the wrong kind of
			// document", most commonly a whole SOAP Project XML export
			// pasted where a plain WSDL was expected.
			hint := ""
			if root == "soapui-project" {
				hint = " — a SOAP Project XML export needs its own \"Import SOAP Project XML\" instead"
			}
			return nil, fmt.Errorf("expected a WSDL document (root element <definitions>), but found <%s>%s", root, hint)
		}
		return nil, err
	}

	actionsByOp := collectBindingActions(defs.Bindings)
	partsByMessage := messagePartsByName(defs.Messages, collectHeaderParts(defs.Bindings))

	out := []Operation{}
	seen := map[string]bool{}
	for _, pt := range defs.PortTypes {
		for _, op := range pt.Operations {
			if seen[op.Name] {
				continue
			}
			seen[op.Name] = true
			parts := partsByMessage[localName(op.Input.Message)]
			out = append(out, operationsFor(op.Name, actionsByOp[op.Name], parts)...)
		}
	}

	return &Document{Name: documentName(defs), EndpointURL: firstServiceEndpoint(defs.Services), Operations: out}, nil
}

func firstServiceEndpoint(services []wsdlService) string {
	for _, svc := range services {
		for _, port := range svc.Ports {
			if port.Address.Location != "" {
				return port.Address.Location
			}
		}
	}
	return ""
}

// documentName picks the first declared service's name over the
// definitions element's own — a real service name is user-facing (client
// codegen tools name generated classes/interfaces after it), so it's
// almost always the more specific, meaningful choice, while @name on
// <definitions> is often left at whatever placeholder the authoring tool
// defaulted to (a real-world WSDL export seen in the wild: <definitions
// name="Untitled">, with a properly named <service> right alongside it).
// Falls back to the definitions name when there's no service at all, then
// "" (left for the caller to fall back on) when neither is set.
func documentName(defs definitions) string {
	for _, svc := range defs.Services {
		if svc.Name != "" {
			return svc.Name
		}
	}
	return defs.Name
}

// rootElementName returns the local name of data's root XML element, or ""
// if none can be found (empty/malformed input) — used only to build a
// friendlier "wrong document type" error than encoding/xml's own, which
// names the expected/found element types but reads as an internal error.
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

// localName strips a namespace prefix ("tns:GetOrderRequest" -> "GetOrderRequest"),
// matching this package's general tolerance of namespace prefixes throughout.
func localName(qname string) string {
	if i := strings.LastIndex(qname, ":"); i >= 0 {
		return qname[i+1:]
	}
	return qname
}

func messagePartsByName(msgs []message, headerParts map[string]map[string]bool) map[string][]Part {
	out := make(map[string][]Part, len(msgs))
	for _, m := range msgs {
		parts := make([]Part, 0, len(m.Parts))
		for _, p := range m.Parts {
			parts = append(parts, Part{
				Name:    p.Name,
				Type:    localName(p.Type),
				Element: localName(p.Element),
				Header:  headerParts[m.Name][p.Name],
			})
		}
		out[m.Name] = parts
	}
	return out
}

// collectHeaderParts returns, per message name, the set of part names the
// binding marks as a SOAP header (<soap:header message="tns:X" part="Y"/>)
// rather than body content.
func collectHeaderParts(bindings []binding) map[string]map[string]bool {
	out := map[string]map[string]bool{}
	for _, b := range bindings {
		for _, op := range b.Operations {
			for _, h := range op.Input.Headers {
				msg, part := localName(h.Message), h.Part
				if msg == "" || part == "" {
					continue
				}
				if out[msg] == nil {
					out[msg] = map[string]bool{}
				}
				out[msg][part] = true
			}
		}
	}
	return out
}

// Parse reads a WSDL document and returns its operations, portType names
// deduplicated against binding-declared SOAPActions by operation name — or,
// when an operation genuinely has more than one distinct SOAPAction across
// bindings, one Operation per distinct action with BindingName set so the
// caller can scaffold a separate, disambiguated mock for each rather than
// losing all but one.
func Parse(data []byte) ([]Operation, error) {
	doc, err := ParseDocument(data)
	if err != nil {
		return nil, err
	}
	return doc.Operations, nil
}

type bindingAction struct {
	bindingName string
	soapAction  string
}

// collectBindingActions groups each operation name's distinct SOAPActions
// by the binding(s) that declared them — multiple bindings agreeing on the
// same SOAPAction for an operation collapse to one entry, since that's not
// actually a conflict.
func collectBindingActions(bindings []binding) map[string][]bindingAction {
	actionsByOp := map[string][]bindingAction{}
	for _, b := range bindings {
		for _, op := range b.Operations {
			if !hasAction(actionsByOp[op.Name], op.SOAPOperation.SOAPAction) {
				actionsByOp[op.Name] = append(actionsByOp[op.Name], bindingAction{bindingName: b.Name, soapAction: op.SOAPOperation.SOAPAction})
			}
		}
	}
	return actionsByOp
}

func hasAction(actions []bindingAction, soapAction string) bool {
	for _, a := range actions {
		if a.soapAction == soapAction {
			return true
		}
	}
	return false
}

// operationsFor builds the Operation(s) for one portType operation name:
// exactly one, with no BindingName, for the common 0-or-1-distinct-action
// case (preserving the exact previous output shape); one per action, with
// BindingName set, when there's more than one distinct SOAPAction. parts
// (this operation's resolved input message parts, if any) is the same for
// every variant produced — the input message doesn't vary per binding.
func operationsFor(name string, actions []bindingAction, parts []Part) []Operation {
	if len(actions) <= 1 {
		action := ""
		if len(actions) == 1 {
			action = actions[0].soapAction
		}
		return []Operation{{Name: name, SOAPAction: action, InputParts: parts}}
	}
	out := make([]Operation, 0, len(actions))
	for _, a := range actions {
		out = append(out, Operation{Name: name, SOAPAction: a.soapAction, BindingName: a.bindingName, InputParts: parts})
	}
	return out
}
