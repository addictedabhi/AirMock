package wsdl

import (
	"strings"
	"testing"
	"time"
)

// wsdlWithInlineSchema wraps a <xsd:schema> fragment in a minimal
// <definitions><types> so BuildSchemaSet can parse it the same way it
// would a real WSDL's own inline schema.
func wsdlWithInlineSchema(schema string) string {
	return `<?xml version="1.0"?>
<definitions>
  <types>` + schema + `</types>
</definitions>`
}

// TestRenderElementScalarLeafNoComment guards the plain case: a required
// (no minOccurs, no maxOccurs — both default to 1) scalar-typed element
// gets no comment at all, just <name>?</name>.
func TestRenderElementScalarLeafNoComment(t *testing.T) {
	wsdl := wsdlWithInlineSchema(`<xsd:schema targetNamespace="urn:svc">
    <xsd:element name="req" type="tns:reqType"/>
    <xsd:complexType name="reqType">
      <xsd:sequence>
        <xsd:element name="imsi" type="xsd:string"/>
      </xsd:sequence>
    </xsd:complexType>
  </xsd:schema>`)
	set, err := BuildSchemaSet([]byte(wsdl), nil)
	if err != nil {
		t.Fatalf("BuildSchemaSet: %v", err)
	}
	tag, ns, inner, ok := set.RenderElement("req", "  ")
	if !ok {
		t.Fatal("expected req to resolve")
	}
	if tag != "req" || ns != "urn:svc" {
		t.Fatalf("expected tag=req ns=urn:svc, got tag=%q ns=%q", tag, ns)
	}
	if strings.Contains(inner, "<!--") {
		t.Errorf("expected no comment for a required scalar, got %q", inner)
	}
	if !strings.Contains(inner, "<imsi>?</imsi>") {
		t.Errorf("expected a scalar placeholder for imsi, got %q", inner)
	}
}

// TestRenderElementOptionalScalarGetsOptionalComment guards minOccurs="0"
// (maxOccurs unset, defaults to 1) rendering as "Optional:", not folded
// into a repetitions comment.
func TestRenderElementOptionalScalarGetsOptionalComment(t *testing.T) {
	wsdl := wsdlWithInlineSchema(`<xsd:schema targetNamespace="urn:svc">
    <xsd:element name="req" type="tns:reqType"/>
    <xsd:complexType name="reqType">
      <xsd:sequence>
        <xsd:element name="algorithm" type="xsd:string" minOccurs="0"/>
      </xsd:sequence>
    </xsd:complexType>
  </xsd:schema>`)
	set, _ := BuildSchemaSet([]byte(wsdl), nil)
	_, _, inner, ok := set.RenderElement("req", "  ")
	if !ok {
		t.Fatal("expected req to resolve")
	}
	if !strings.Contains(inner, "<!--Optional:-->\n  <algorithm>?</algorithm>") {
		t.Errorf("expected an Optional: comment directly before algorithm, got %q", inner)
	}
}

// TestRenderElementRepeatingCombinesMinMaxNotOptional guards the real
// pattern this feature was built for: minOccurs="0" maxOccurs="50" must
// render as one combined "0 to 50 repetitions:" comment, never a separate
// "Optional:" comment alongside it, and only ONE instance of the element
// is emitted (not 50 copies).
func TestRenderElementRepeatingCombinesMinMaxNotOptional(t *testing.T) {
	wsdl := wsdlWithInlineSchema(`<xsd:schema targetNamespace="urn:svc">
    <xsd:element name="req" type="tns:reqType"/>
    <xsd:complexType name="reqType">
      <xsd:sequence>
        <xsd:element name="apn" type="tns:apnType" minOccurs="0" maxOccurs="50"/>
      </xsd:sequence>
    </xsd:complexType>
    <xsd:complexType name="apnType">
      <xsd:sequence>
        <xsd:element name="apnName" type="xsd:string"/>
      </xsd:sequence>
    </xsd:complexType>
  </xsd:schema>`)
	set, _ := BuildSchemaSet([]byte(wsdl), nil)
	_, _, inner, ok := set.RenderElement("req", "  ")
	if !ok {
		t.Fatal("expected req to resolve")
	}
	if !strings.Contains(inner, "<!--0 to 50 repetitions:-->") {
		t.Errorf("expected a combined '0 to 50 repetitions:' comment, got %q", inner)
	}
	if strings.Contains(inner, "Optional") {
		t.Errorf("expected no separate Optional comment alongside repetitions, got %q", inner)
	}
	if strings.Count(inner, "<apn>") != 1 {
		t.Errorf("expected exactly one <apn> instance despite maxOccurs=50, got %q", inner)
	}
	if !strings.Contains(inner, "<apnName>?</apnName>") {
		t.Errorf("expected apn's own nested apnName, got %q", inner)
	}
}

// TestRenderElementNestedChildrenAreUnprefixed guards the exact namespace
// convention seen in a real WSDL's own generated sample: only the
// top-level resolved element gets a namespace prefix (via the tag/ns the
// caller declares), every nested child — however deep — stays unprefixed.
// RenderElement itself never emits a prefix on the inner content; this
// pins that the recursive rendering plainly never adds one either.
func TestRenderElementNestedChildrenAreUnprefixed(t *testing.T) {
	wsdl := wsdlWithInlineSchema(`<xsd:schema targetNamespace="urn:svc">
    <xsd:element name="req" type="tns:reqType"/>
    <xsd:complexType name="reqType">
      <xsd:sequence>
        <xsd:element name="secondaryService" type="tns:secType" minOccurs="0"/>
      </xsd:sequence>
    </xsd:complexType>
    <xsd:complexType name="secType">
      <xsd:sequence>
        <xsd:element name="secondaryMsisdn" type="xsd:string"/>
      </xsd:sequence>
    </xsd:complexType>
  </xsd:schema>`)
	set, _ := BuildSchemaSet([]byte(wsdl), nil)
	_, _, inner, _ := set.RenderElement("req", "  ")
	if strings.Contains(inner, ":secondaryService") || strings.Contains(inner, ":secondaryMsisdn") {
		t.Errorf("expected nested elements to stay unprefixed, got %q", inner)
	}
	if !strings.Contains(inner, "<secondaryService>\n") {
		t.Errorf("expected a nested secondaryService block, got %q", inner)
	}
}

// TestRenderElementUnknownNameFallsBackNotOK confirms a part whose element
// hint doesn't match anything in the schema set just reports ok=false, so
// the caller can fall back to its own flat placeholder rather than panic
// or silently render nothing.
func TestRenderElementUnknownNameFallsBackNotOK(t *testing.T) {
	set, _ := BuildSchemaSet([]byte(wsdlWithInlineSchema(`<xsd:schema/>`)), nil)
	_, _, _, ok := set.RenderElement("doesNotExist", "  ")
	if ok {
		t.Fatal("expected ok=false for an unresolvable element name")
	}
}

// TestBuildSchemaSetMergesExtraSchemaFile guards the whole reason
// BuildSchemaSet takes extraSchemas at all: a WSDL that splits its schema
// out via <xsd:include>/<xsd:import> into a separate *.xsd file (a real,
// common pattern — this reader doesn't follow that reference on disk
// itself, so the caller must read and supply the file's own content).
func TestBuildSchemaSetMergesExtraSchemaFile(t *testing.T) {
	mainWSDL := wsdlWithInlineSchema(`<xsd:schema targetNamespace="urn:svc">
    <xsd:element name="req" type="tns:reqType"/>
  </xsd:schema>`)
	externalSchema := `<?xml version="1.0"?>
<xsd:schema xmlns:xsd="http://www.w3.org/2001/XMLSchema" xmlns:tns="urn:svc" targetNamespace="urn:svc">
  <xsd:complexType name="reqType">
    <xsd:sequence>
      <xsd:element name="imsi" type="xsd:string"/>
    </xsd:sequence>
  </xsd:complexType>
</xsd:schema>`
	set, err := BuildSchemaSet([]byte(mainWSDL), [][]byte{[]byte(externalSchema)})
	if err != nil {
		t.Fatalf("BuildSchemaSet: %v", err)
	}
	_, _, inner, ok := set.RenderElement("req", "  ")
	if !ok {
		t.Fatal("expected req to resolve using the externally-supplied schema's complexType")
	}
	if !strings.Contains(inner, "<imsi>?</imsi>") {
		t.Errorf("expected imsi resolved from the external schema file, got %q", inner)
	}
}

// TestRenderElementSelfReferentialSchemaTerminates guards the cycle guard:
// a complexType whose own sequence (indirectly) references itself must
// terminate as a scalar leaf on the repeat, not recurse forever.
func TestRenderElementSelfReferentialSchemaTerminates(t *testing.T) {
	wsdl := wsdlWithInlineSchema(`<xsd:schema targetNamespace="urn:svc">
    <xsd:element name="node" type="tns:nodeType"/>
    <xsd:complexType name="nodeType">
      <xsd:sequence>
        <xsd:element name="value" type="xsd:string"/>
        <xsd:element name="child" type="tns:nodeType" minOccurs="0"/>
      </xsd:sequence>
    </xsd:complexType>
  </xsd:schema>`)
	set, _ := BuildSchemaSet([]byte(wsdl), nil)
	done := make(chan string, 1)
	go func() {
		_, _, inner, _ := set.RenderElement("node", "  ")
		done <- inner
	}()
	select {
	case inner := <-done:
		if !strings.Contains(inner, "<value>?</value>") {
			t.Errorf("expected the top-level value to still render, got %q", inner)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("RenderElement did not terminate on a self-referential schema")
	}
}
