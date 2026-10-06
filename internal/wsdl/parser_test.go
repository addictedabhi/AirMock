package wsdl

import (
	"strings"
	"testing"
)

const fixtureWSDL = `<?xml version="1.0" encoding="UTF-8"?>
<wsdl:definitions name="OrderService"
    xmlns:wsdl="http://schemas.xmlsoap.org/wsdl/"
    xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/"
    xmlns:tns="urn:orders"
    targetNamespace="urn:orders">

  <wsdl:portType name="OrderServicePortType">
    <wsdl:operation name="GetOrder">
      <wsdl:input message="tns:GetOrderRequest"/>
      <wsdl:output message="tns:GetOrderResponse"/>
    </wsdl:operation>
    <wsdl:operation name="CreateOrder">
      <wsdl:input message="tns:CreateOrderRequest"/>
      <wsdl:output message="tns:CreateOrderResponse"/>
    </wsdl:operation>
  </wsdl:portType>

  <wsdl:binding name="OrderServiceBinding" type="tns:OrderServicePortType">
    <soap:binding style="document" transport="http://schemas.xmlsoap.org/soap/http"/>
    <wsdl:operation name="GetOrder">
      <soap:operation soapAction="urn:orders#GetOrder"/>
      <wsdl:input><soap:body use="literal"/></wsdl:input>
      <wsdl:output><soap:body use="literal"/></wsdl:output>
    </wsdl:operation>
    <wsdl:operation name="CreateOrder">
      <soap:operation soapAction="urn:orders#CreateOrder"/>
      <wsdl:input><soap:body use="literal"/></wsdl:input>
      <wsdl:output><soap:body use="literal"/></wsdl:output>
    </wsdl:operation>
  </wsdl:binding>
</wsdl:definitions>`

func TestParseFixtureWSDLReturnsExpectedOperations(t *testing.T) {
	ops, err := Parse([]byte(fixtureWSDL))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 operations, got %d: %+v", len(ops), ops)
	}

	want := map[string]string{
		"GetOrder":    "urn:orders#GetOrder",
		"CreateOrder": "urn:orders#CreateOrder",
	}
	for _, op := range ops {
		wantAction, ok := want[op.Name]
		if !ok {
			t.Errorf("unexpected operation %q", op.Name)
			continue
		}
		if op.SOAPAction != wantAction {
			t.Errorf("operation %q: expected SOAPAction %q, got %q", op.Name, wantAction, op.SOAPAction)
		}
	}
}

// TestParseMultipleBindingsWithDifferentActionsAreAllPreserved guards
// against a real gap: actionByName used to be a flat map keyed only by
// operation name, so a second binding declaring a DIFFERENT SOAPAction for
// the same operation (e.g. a separate SOAP 1.2 binding alongside a SOAP 1.1
// one) silently overwrote the first with no way to see or choose both.
func TestParseMultipleBindingsWithDifferentActionsAreAllPreserved(t *testing.T) {
	const multiBinding = `<?xml version="1.0"?>
<definitions>
  <portType name="X">
    <operation name="GetOrder"/>
  </portType>
  <binding name="Soap11Binding">
    <operation name="GetOrder"><operation soapAction="urn:orders#GetOrder11"/></operation>
  </binding>
  <binding name="Soap12Binding">
    <operation name="GetOrder"><operation soapAction="urn:orders#GetOrder12"/></operation>
  </binding>
</definitions>`
	ops, err := Parse([]byte(multiBinding))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 2 {
		t.Fatalf("expected 2 operations (one per distinct binding action), got %d: %+v", len(ops), ops)
	}
	seen := map[string]string{}
	for _, op := range ops {
		if op.Name != "GetOrder" {
			t.Errorf("unexpected operation name %q", op.Name)
		}
		if op.BindingName == "" {
			t.Errorf("expected BindingName to be set when more than one binding disagrees, got %+v", op)
		}
		seen[op.BindingName] = op.SOAPAction
	}
	if seen["Soap11Binding"] != "urn:orders#GetOrder11" || seen["Soap12Binding"] != "urn:orders#GetOrder12" {
		t.Fatalf("expected both bindings' distinct actions preserved, got %+v", seen)
	}
}

// TestParseMultipleBindingsAgreeingOnSameActionCollapseToOne confirms two
// bindings that happen to declare the SAME SOAPAction for an operation
// (not actually a conflict) still produce just one Operation, matching the
// single-binding shape exactly (no spurious BindingName).
func TestParseMultipleBindingsAgreeingOnSameActionCollapseToOne(t *testing.T) {
	const agreeingBindings = `<?xml version="1.0"?>
<definitions>
  <portType name="X">
    <operation name="Ping"/>
  </portType>
  <binding name="BindingA">
    <operation name="Ping"><operation soapAction="urn:svc#Ping"/></operation>
  </binding>
  <binding name="BindingB">
    <operation name="Ping"><operation soapAction="urn:svc#Ping"/></operation>
  </binding>
</definitions>`
	ops, err := Parse([]byte(agreeingBindings))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 1 || ops[0].SOAPAction != "urn:svc#Ping" || ops[0].BindingName != "" {
		t.Fatalf("expected exactly one Operation with no BindingName, got %+v", ops)
	}
}

// TestParseDocumentCarriesOverNameAndServiceEndpoint guards the richer
// ParseDocument API added for collection import (Parse itself only ever
// returned Operations, which callers that just want mocks still use) — the
// definitions element's own name and the first <service><port>'s address
// must both come through, since a collection import uses them as the
// collection name and the default request URL.
func TestParseDocumentCarriesOverNameAndServiceEndpoint(t *testing.T) {
	doc, err := ParseDocument([]byte(fixtureWSDL))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	if doc.Name != "OrderService" {
		t.Fatalf("expected definitions name to carry over, got %q", doc.Name)
	}
	if doc.EndpointURL != "" {
		t.Fatalf("fixtureWSDL declares no <service>, expected an empty endpoint, got %q", doc.EndpointURL)
	}
	if len(doc.Operations) != 2 {
		t.Fatalf("expected the same 2 operations Parse returns, got %d", len(doc.Operations))
	}

	const withService = `<?xml version="1.0"?>
<definitions name="X" xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/">
  <portType name="PT"><operation name="Ping"/></portType>
  <service name="X">
    <port name="P1" binding="B1"><soap:address location="https://first.example.com/soap"/></port>
    <port name="P2" binding="B2"><soap:address location="https://second.example.com/soap"/></port>
  </service>
</definitions>`
	doc2, err := ParseDocument([]byte(withService))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	if doc2.EndpointURL != "https://first.example.com/soap" {
		t.Fatalf("expected the first service port's endpoint, got %q", doc2.EndpointURL)
	}
}

// TestParseDocumentFallsBackToServiceNameWhenDefinitionsHasNone guards the
// name fallback added for collection import: plenty of real WSDLs omit
// @name on <definitions> (only targetNamespace is required), so the first
// declared service's name is the next-best stand-in.
func TestParseDocumentFallsBackToServiceNameWhenDefinitionsHasNone(t *testing.T) {
	const noDefinitionsName = `<?xml version="1.0"?>
<definitions>
  <portType name="PT"><operation name="Ping"/></portType>
  <service name="PingService"><port name="P" binding="B"/></service>
</definitions>`
	doc, err := ParseDocument([]byte(noDefinitionsName))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	if doc.Name != "PingService" {
		t.Fatalf("expected fallback to the service name, got %q", doc.Name)
	}
}

// TestParseDocumentResolvesOperationInputParts guards InputParts, added so
// a collection import can scaffold one placeholder element per expected
// parameter instead of a single opaque comment: the operation's <input
// message="tns:X"/> must resolve to the matching <message><part> entries,
// each keeping its own type/element hint with the namespace prefix
// stripped.
func TestParseDocumentResolvesOperationInputParts(t *testing.T) {
	const withParts = `<?xml version="1.0"?>
<definitions xmlns:tns="urn:orders">
  <message name="GetOrderRequest">
    <part name="orderId" type="tns:string"/>
    <part name="includeItems" element="tns:IncludeItemsFlag"/>
  </message>
  <portType name="PT">
    <operation name="GetOrder">
      <input message="tns:GetOrderRequest"/>
    </operation>
  </portType>
</definitions>`
	ops, err := Parse([]byte(withParts))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 1 {
		t.Fatalf("expected 1 operation, got %d", len(ops))
	}
	parts := ops[0].InputParts
	if len(parts) != 2 {
		t.Fatalf("expected 2 input parts, got %+v", parts)
	}
	if parts[0].Name != "orderId" || parts[0].Type != "string" {
		t.Errorf("expected orderId part with type hint 'string' (prefix stripped), got %+v", parts[0])
	}
	if parts[1].Name != "includeItems" || parts[1].Element != "IncludeItemsFlag" {
		t.Errorf("expected includeItems part with element hint 'IncludeItemsFlag' (prefix stripped), got %+v", parts[1])
	}
}

// TestParseDocumentOperationWithNoMatchingMessageHasEmptyInputParts confirms
// an operation whose <input message="..."/> doesn't match any declared
// <message> (or has none at all) just gets an empty InputParts, not an
// error — the fixtureWSDL cases already exercise this implicitly, but this
// pins the behavior directly for a bare operation with no input at all.
func TestParseDocumentOperationWithNoMatchingMessageHasEmptyInputParts(t *testing.T) {
	const noMessage = `<?xml version="1.0"?>
<definitions>
  <portType name="PT"><operation name="Ping"/></portType>
</definitions>`
	ops, err := Parse([]byte(noMessage))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 1 || len(ops[0].InputParts) != 0 {
		t.Fatalf("expected 1 operation with no input parts, got %+v", ops)
	}
}

// TestParseDocumentPrefersServiceNameOverDefinitionsName guards a real
// case: a TIBCO-generated WSDL export with <definitions name="Untitled">
// (the authoring tool's own unhelpful placeholder) right alongside a
// properly named <service name="VodafoneGDSPWebservice">. The service name
// is user-facing and near-always the more specific choice, so it wins even
// though the definitions name isn't empty.
func TestParseDocumentPrefersServiceNameOverDefinitionsName(t *testing.T) {
	const untitled = `<?xml version="1.0"?>
<definitions name="Untitled">
  <portType name="PT"><operation name="Ping"/></portType>
  <service name="VodafoneGDSPWebservice"><port name="P" binding="B"/></service>
</definitions>`
	doc, err := ParseDocument([]byte(untitled))
	if err != nil {
		t.Fatalf("ParseDocument: %v", err)
	}
	if doc.Name != "VodafoneGDSPWebservice" {
		t.Fatalf("expected the service name to win over the generic definitions name, got %q", doc.Name)
	}
}

// TestParseDocumentSplitsHeaderPartsFromBodyParts guards a real pattern
// seen in an actual enterprise WSDL: an operation's input message has two
// parts, one declared as the SOAP body and the other explicitly as a SOAP
// header (<soap:header message="tns:X" part="Y"/>) — the header part must
// come back with Header==true so a generated request can place it in
// <soapenv:Header> instead of dumping it into the body alongside the
// actual payload.
func TestParseDocumentSplitsHeaderPartsFromBodyParts(t *testing.T) {
	const withHeaderPart = `<?xml version="1.0"?>
<definitions xmlns:tns="urn:svc">
  <message name="DoThing">
    <part name="parameters" element="tns:DoThingRequest"/>
    <part name="authHeader" element="tns:AuthHeader"/>
  </message>
  <portType name="PT">
    <operation name="DoThing"><input message="tns:DoThing"/></operation>
  </portType>
  <binding name="B" type="tns:PT">
    <operation name="DoThing">
      <input>
        <soap:body xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/" parts="parameters"/>
        <soap:header xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/" message="tns:DoThing" part="authHeader"/>
      </input>
    </operation>
  </binding>
</definitions>`
	ops, err := Parse([]byte(withHeaderPart))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 1 || len(ops[0].InputParts) != 2 {
		t.Fatalf("expected 1 operation with 2 input parts, got %+v", ops)
	}
	byName := map[string]Part{}
	for _, p := range ops[0].InputParts {
		byName[p.Name] = p
	}
	if byName["parameters"].Header {
		t.Errorf("expected 'parameters' to NOT be marked as a header part, got %+v", byName["parameters"])
	}
	if !byName["authHeader"].Header {
		t.Errorf("expected 'authHeader' to be marked as a header part, got %+v", byName["authHeader"])
	}
}

func TestParseOperationWithoutBindingHasEmptySOAPAction(t *testing.T) {
	const minimal = `<?xml version="1.0"?>
<definitions>
  <portType name="X">
    <operation name="Ping"/>
  </portType>
</definitions>`
	ops, err := Parse([]byte(minimal))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if len(ops) != 1 || ops[0].Name != "Ping" || ops[0].SOAPAction != "" {
		t.Fatalf("expected one Ping operation with empty SOAPAction, got %+v", ops)
	}
}

// TestParseDocumentOnSoapUIProjectGivesFriendlyError guards the mirror
// image of soapui's own mix-up guard: a full SOAP Project XML export (root
// <soapui-project>, a completely different schema from a plain WSDL)
// pasted into the WSDL import — encoding/xml's own error here ("expected
// element type <definitions> but have <soapui-project>") is technically
// accurate but reads as an internal error, not "wrong document type, use
// Import SOAP Project XML instead".
func TestParseDocumentOnSoapUIProjectGivesFriendlyError(t *testing.T) {
	const soapUIProject = `<?xml version="1.0"?><con:soapui-project xmlns:con="http://eviware.com/soapui/config"/>`
	_, err := ParseDocument([]byte(soapUIProject))
	if err == nil {
		t.Fatal("expected an error for a SOAP Project XML export, got none")
	}
	if !strings.Contains(err.Error(), "<soapui-project>") || !strings.Contains(err.Error(), "Import SOAP Project XML") {
		t.Fatalf("expected a friendly error naming <soapui-project> and pointing at Import SOAP Project XML, got %q", err.Error())
	}
}

// TestParseDocumentOnUnrelatedXMLFallsBackToRawError confirms a genuinely
// unrelated root element still surfaces an error, just without a false
// "use Import SOAP Project XML" hint.
func TestParseDocumentOnUnrelatedXMLFallsBackToRawError(t *testing.T) {
	_, err := ParseDocument([]byte(`<?xml version="1.0"?><rss><channel/></rss>`))
	if err == nil {
		t.Fatal("expected an error for an unrelated document, got none")
	}
	if strings.Contains(err.Error(), "Import SOAP Project XML") {
		t.Fatalf("expected no SoapUI hint for an unrelated root element, got %q", err.Error())
	}
}
