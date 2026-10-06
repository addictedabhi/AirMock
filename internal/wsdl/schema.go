package wsdl

import (
	"encoding/xml"
	"fmt"
	"strings"
)

// xsdElement is one <xsd:element> — either a top-level schema element
// (resolvable by name via a message part's own element="..." reference),
// or a child inside a complexType's sequence/choice/all (Name+Type, plus
// MinOccurs/MaxOccurs governing how a generated stub comments it).
// Deliberately doesn't model <xsd:attribute> or complexContent/extension —
// out of scope for "enough to scaffold a stub body", see the package doc
// comment's same scoping call for the rest of this reader.
type xsdElement struct {
	Name      string `xml:"name,attr"`
	Type      string `xml:"type,attr"`
	Ref       string `xml:"ref,attr"`
	MinOccurs string `xml:"minOccurs,attr"`
	MaxOccurs string `xml:"maxOccurs,attr"`
	// ComplexType is set for an inline anonymous type declared directly on
	// the element (<xsd:element name="X"><xsd:complexType>...) rather than
	// via a separate named type="..." reference — both real-world shapes
	// are supported.
	ComplexType *xsdComplexType `xml:"complexType"`
}

type xsdComplexType struct {
	Name     string    `xml:"name,attr"`
	Sequence *xsdGroup `xml:"sequence"`
	Choice   *xsdGroup `xml:"choice"`
	All      *xsdGroup `xml:"all"`
}

type xsdGroup struct {
	Elements []xsdElement `xml:"element"`
}

type xsdSchema struct {
	XMLName         xml.Name         `xml:"schema"`
	TargetNamespace string           `xml:"targetNamespace,attr"`
	Elements        []xsdElement     `xml:"element"`
	ComplexTypes    []xsdComplexType `xml:"complexType"`
}

// wsdlTypesHolder mirrors <wsdl:types>, which can hold one or more
// <xsd:schema> blocks — splitting a schema across multiple <schema>
// elements inside one WSDL is legal, if uncommon.
type wsdlTypesHolder struct {
	Schemas []xsdSchema `xml:"schema"`
}

// resolved is one schema element together with the namespace it belongs
// to, returned by lookups so a caller can mint an xmlns prefix for it.
type resolvedElement struct {
	el        *xsdElement
	namespace string
}

// SchemaSet is the combined pool of every <xsd:element>/<xsd:complexType>
// a WSDL knows about — its own inline <wsdl:types>, plus any extra schema
// documents the caller supplies for a WSDL that splits its schema out via
// <xsd:include>/<xsd:import> (this reader doesn't follow those references
// on disk itself — see BuildSchemaSet). Matches elements/types by local
// name only across every absorbed schema, same namespace-prefix tolerance
// as the rest of this package; real-world name collisions across
// distinct, unrelated schemas absorbed into one set are rare enough not to
// guard against here.
type SchemaSet struct {
	elements     map[string]resolvedElement
	complexTypes map[string]*xsdComplexType
}

// BuildSchemaSet parses mainWSDL's own <wsdl:types> plus each of
// extraSchemas (a standalone *.xsd document, root <xsd:schema>) into one
// combined SchemaSet. A malformed extra schema is skipped rather than
// failing the whole set — a stub body generated from partial schema
// knowledge is more useful than refusing the import outright over one bad
// companion file.
func BuildSchemaSet(mainWSDL []byte, extraSchemas [][]byte) (*SchemaSet, error) {
	var defs struct {
		Types wsdlTypesHolder `xml:"types"`
	}
	if err := xml.Unmarshal(mainWSDL, &defs); err != nil {
		return nil, err
	}
	set := &SchemaSet{elements: map[string]resolvedElement{}, complexTypes: map[string]*xsdComplexType{}}
	for _, s := range defs.Types.Schemas {
		set.absorb(s)
	}
	for _, raw := range extraSchemas {
		var s xsdSchema
		if xml.Unmarshal(raw, &s) == nil {
			set.absorb(s)
		}
	}
	return set, nil
}

func (s *SchemaSet) absorb(schema xsdSchema) {
	for i := range schema.Elements {
		el := schema.Elements[i]
		s.elements[el.Name] = resolvedElement{el: &el, namespace: schema.TargetNamespace}
	}
	for i := range schema.ComplexTypes {
		ct := schema.ComplexTypes[i]
		s.complexTypes[ct.Name] = &ct
	}
}

// maxNestingDepth backstops the cycle guard (a self-referential schema
// tracked per-branch already prevents true infinite recursion, see
// renderChildren's visited set) and keeps a pathologically deep schema's
// generated stub from growing unbounded.
const maxNestingDepth = 20

// RenderElement renders a placeholder XML fragment for a top-level schema
// element by name (a message part's own element="..." reference), each
// line indented by indent, recursively expanding through complex types —
// the element's own name/namespace (for the caller to wrap and declare an
// xmlns prefix for) plus its inner content. ok is false when name isn't a
// known element in this set, so the caller can fall back to its own flat
// placeholder instead.
func (s *SchemaSet) RenderElement(name string, indent string) (tagName, namespace, inner string, ok bool) {
	res, found := s.elements[name]
	if !found {
		return "", "", "", false
	}
	var b strings.Builder
	s.renderChildren(&b, *res.el, indent, map[string]bool{})
	return res.el.Name, res.namespace, b.String(), true
}

// renderChildren writes one <!--comment--><name>...</name> block per child
// element of el's own type (inline ComplexType, or the named type its Type
// attribute resolves to) — a scalar leaf when the type isn't a known
// complexType, a recursive nested block otherwise. visited tracks complex
// type names already open on this branch (not globally), so a genuinely
// self-referential schema terminates as a scalar leaf instead of looping
// forever; maxNestingDepth is a second, depth-based backstop.
func (s *SchemaSet) renderChildren(b *strings.Builder, el xsdElement, indent string, visited map[string]bool) {
	ct := s.complexTypeOf(el)
	if ct == nil || visited[ct.Name] || len(visited) >= maxNestingDepth {
		return
	}
	if ct.Name != "" {
		visited = cloneVisited(visited)
		visited[ct.Name] = true
	}
	for _, child := range groupElements(ct) {
		s.renderOneChild(b, child, indent, visited)
	}
}

func (s *SchemaSet) renderOneChild(b *strings.Builder, child xsdElement, indent string, visited map[string]bool) {
	if child.Ref != "" && child.Name == "" {
		if res, ok := s.elements[localName(child.Ref)]; ok {
			refChild := *res.el
			refChild.MinOccurs, refChild.MaxOccurs = child.MinOccurs, child.MaxOccurs
			child = refChild
		}
	}
	if comment := occursComment(child.MinOccurs, child.MaxOccurs); comment != "" {
		fmt.Fprintf(b, "%s<!--%s-->\n", indent, comment)
	}
	childCT := s.complexTypeOf(child)
	if childCT == nil {
		fmt.Fprintf(b, "%s<%s>?</%s>\n", indent, child.Name, child.Name)
		return
	}
	fmt.Fprintf(b, "%s<%s>\n", indent, child.Name)
	s.renderChildren(b, child, indent+"  ", visited)
	fmt.Fprintf(b, "%s</%s>\n", indent, child.Name)
}

// complexTypeOf resolves an element's own inline complexType if it has
// one, else looks up its type="..." attribute (namespace-stripped) in the
// complexTypes registry — nil when neither applies (a scalar/simple type,
// built-in or otherwise; this reader doesn't parse <xsd:simpleType>, since
// for stub-generation purposes anything that isn't a known complex type is
// treated the same way: a leaf placeholder).
func (s *SchemaSet) complexTypeOf(el xsdElement) *xsdComplexType {
	if el.ComplexType != nil {
		return el.ComplexType
	}
	if el.Type == "" {
		return nil
	}
	return s.complexTypes[localName(el.Type)]
}

func groupElements(ct *xsdComplexType) []xsdElement {
	switch {
	case ct.Sequence != nil:
		return ct.Sequence.Elements
	case ct.Choice != nil:
		// Each choice member is inherently "pick one, not required" from a
		// stub-filler's perspective regardless of its own declared
		// minOccurs, so occursComment always sees minOccurs=0 for these —
		// handled by the caller forcing MinOccurs="0" below.
		out := make([]xsdElement, len(ct.Choice.Elements))
		for i, e := range ct.Choice.Elements {
			e.MinOccurs = "0"
			out[i] = e
		}
		return out
	case ct.All != nil:
		return ct.All.Elements
	default:
		return nil
	}
}

func cloneVisited(v map[string]bool) map[string]bool {
	out := make(map[string]bool, len(v)+1)
	for k := range v {
		out[k] = true
	}
	return out
}

// occursComment renders the same "Optional:" / "N to M repetitions:"
// convention as SoapUI's own generated blank-sample requests: minOccurs=0
// with maxOccurs<=1 (the default when unset) is "Optional:"; maxOccurs>1
// (a number, or "unbounded") combines both bounds into one "repetitions:"
// comment instead of a separate "Optional:" — matching a real-world
// example seen with minOccurs="0" maxOccurs="50" rendering as exactly
// "0 to 50 repetitions:", never "Optional:" and "repetitions:" together.
// Empty when the element is required and doesn't repeat (no comment).
func occursComment(minOccurs, maxOccurs string) string {
	min := minOccurs
	if min == "" {
		min = "1"
	}
	max := maxOccurs
	if max == "" {
		max = "1"
	}
	if max != "1" {
		return fmt.Sprintf("%s to %s repetitions:", min, max)
	}
	if min == "0" {
		return "Optional:"
	}
	return ""
}
