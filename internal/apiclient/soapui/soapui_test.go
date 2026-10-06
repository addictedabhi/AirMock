package soapui

import (
	"strings"
	"testing"
)

// sampleProject approximates a real SoapUI project export closely enough
// to exercise both import paths: one TestSuite with one TestCase holding a
// SOAP request step and a REST request step (plus one non-request step
// type, to confirm it's skipped rather than half-imported), and one
// MockService with two operations (one with two mockResponses, to confirm
// only the first is taken).
const sampleProject = `<?xml version="1.0" encoding="UTF-8"?>
<con:soapui-project xmlns:con="http://eviware.com/soapui/config" name="Billing Project">
  <con:testSuite name="Smoke Tests">
    <con:testCase name="Happy path">
      <con:testStep name="Get Invoice" type="request">
        <con:config xsi:type="con:RequestStep">
          <con:request name="Get Invoice">
            <con:settings/>
            <con:encoding>UTF-8</con:encoding>
            <con:endpoint>http://localhost:8080/billing</con:endpoint>
            <con:request><![CDATA[<soapenv:Envelope><soapenv:Body><GetInvoice><id>1</id></GetInvoice></soapenv:Body></soapenv:Envelope>]]></con:request>
          </con:request>
        </con:config>
      </con:testStep>
      <con:testStep name="List Invoices" type="restrequest">
        <con:config xsi:type="con:RestRequestStep">
          <con:restRequest name="List Invoices" method="GET">
            <con:endpoint>http://localhost:8080/api/invoices</con:endpoint>
            <con:request></con:request>
          </con:restRequest>
        </con:config>
      </con:testStep>
      <con:testStep name="Wait a bit" type="delay">
        <con:config xsi:type="con:DelayStep">
          <delay>1000</delay>
        </con:config>
      </con:testStep>
    </con:testCase>
  </con:testSuite>
  <con:mockService name="Billing Mock" path="/mockBilling">
    <con:mockOperation name="GetInvoiceMock" interface="BillingSoap" operation="GetInvoice">
      <con:response name="Response 1" httpResponseStatus="200" encoding="UTF-8">
        <con:responseContent><![CDATA[<soapenv:Envelope><soapenv:Body><GetInvoiceResponse><total>42</total></GetInvoiceResponse></soapenv:Body></soapenv:Envelope>]]></con:responseContent>
      </con:response>
      <con:response name="Response 2 (unused)" httpResponseStatus="200" encoding="UTF-8">
        <con:responseContent><![CDATA[<soapenv:Envelope><soapenv:Body><GetInvoiceResponse><total>0</total></GetInvoiceResponse></soapenv:Body></soapenv:Envelope>]]></con:responseContent>
      </con:response>
    </con:mockOperation>
    <con:mockOperation name="NoResponsesMock" interface="BillingSoap" operation="Ping">
    </con:mockOperation>
  </con:mockService>
  <con:restMockService name="REST MockService 1" path="/">
    <con:restMockAction name="/account-migration" method="POST" resourcePath="/account-migration">
      <con:response name="Response 1" httpResponseStatus="201" mediaType="application/json">
        <con:responseContent><![CDATA[{"status":"migrated"}]]></con:responseContent>
      </con:response>
    </con:restMockAction>
  </con:restMockService>
</con:soapui-project>`

func TestImportCollectionBuildsSuiteAndCaseFolders(t *testing.T) {
	c, err := ImportCollection([]byte(sampleProject))
	if err != nil {
		t.Fatalf("ImportCollection: %v", err)
	}
	if c.Name != "Billing Project" {
		t.Fatalf("expected collection name %q, got %q", "Billing Project", c.Name)
	}
	if len(c.Items) != 1 || c.Items[0].Name != "Smoke Tests" {
		t.Fatalf("expected one top-level folder named %q, got %+v", "Smoke Tests", c.Items)
	}
	suite := c.Items[0]
	if len(suite.Items) != 1 || suite.Items[0].Name != "Happy path" {
		t.Fatalf("expected one nested folder named %q, got %+v", "Happy path", suite.Items)
	}
	testCaseItem := suite.Items[0]
	// "Wait a bit" (type="delay") must be skipped — only the two
	// request-shaped steps become collection items.
	if len(testCaseItem.Items) != 2 {
		t.Fatalf("expected exactly 2 request items (delay step skipped), got %d: %+v", len(testCaseItem.Items), testCaseItem.Items)
	}
}

func TestImportCollectionConvertsSOAPStep(t *testing.T) {
	c, err := ImportCollection([]byte(sampleProject))
	if err != nil {
		t.Fatalf("ImportCollection: %v", err)
	}
	step := c.Items[0].Items[0].Items[0]
	if step.Name != "Get Invoice" {
		t.Fatalf("expected first step named %q, got %q", "Get Invoice", step.Name)
	}
	if step.Request == nil {
		t.Fatal("expected a request spec")
	}
	if step.Request.Method != "POST" {
		t.Fatalf("expected SOAP step to import as POST, got %q", step.Request.Method)
	}
	if step.Request.URL != "http://localhost:8080/billing" {
		t.Fatalf("expected endpoint to carry over, got %q", step.Request.URL)
	}
	if step.Request.Body == "" || step.Request.RawContentType != "xml" {
		t.Fatalf("expected the SOAP envelope body to carry over as xml, got body=%q contentType=%q", step.Request.Body, step.Request.RawContentType)
	}
}

func TestImportCollectionConvertsRESTStep(t *testing.T) {
	c, err := ImportCollection([]byte(sampleProject))
	if err != nil {
		t.Fatalf("ImportCollection: %v", err)
	}
	step := c.Items[0].Items[0].Items[1]
	if step.Name != "List Invoices" {
		t.Fatalf("expected second step named %q, got %q", "List Invoices", step.Name)
	}
	if step.Request.Method != "GET" {
		t.Fatalf("expected REST step's method to carry over, got %q", step.Request.Method)
	}
	if step.Request.URL != "http://localhost:8080/api/invoices" {
		t.Fatalf("expected endpoint to carry over, got %q", step.Request.URL)
	}
}

func TestImportMockServicesTakesFirstResponseAndSkipsEmptyOperation(t *testing.T) {
	scaffolds, err := ImportMockServices([]byte(sampleProject))
	if err != nil {
		t.Fatalf("ImportMockServices: %v", err)
	}
	// NoResponsesMock has zero responses and must be skipped entirely; the
	// one restMockAction scaffolds separately (see
	// TestImportMockServicesScaffoldsRestMockActions), leaving exactly one
	// SOAP scaffold here.
	soapScaffolds := make([]MockServiceScaffold, 0, len(scaffolds))
	for _, s := range scaffolds {
		if s.Protocol == "soap" {
			soapScaffolds = append(soapScaffolds, s)
		}
	}
	if len(soapScaffolds) != 1 {
		t.Fatalf("expected exactly 1 SOAP scaffold (the response-less operation skipped), got %d: %+v", len(soapScaffolds), soapScaffolds)
	}
	s := soapScaffolds[0]
	if s.OperationName != "GetInvoice" {
		t.Fatalf("expected operation name %q, got %q", "GetInvoice", s.OperationName)
	}
	if s.PathPattern != "/mockBilling" {
		t.Fatalf("expected path %q, got %q", "/mockBilling", s.PathPattern)
	}
	if s.ResponseBody == "" {
		t.Fatal("expected a non-empty response body")
	}
	if want := "<total>42</total>"; !strings.Contains(s.ResponseBody, want) {
		t.Fatalf("expected the FIRST response's body (containing %q), got %q", want, s.ResponseBody)
	}
	if strings.Contains(s.ResponseBody, "<total>0</total>") {
		t.Fatalf("expected the second response to be ignored, got %q", s.ResponseBody)
	}
	if s.StatusCode != 200 {
		t.Fatalf("expected the response's httpResponseStatus to carry over as 200, got %d", s.StatusCode)
	}
}

// TestImportMockServicesScaffoldsRestMockActions guards restMockService
// support — a completely separate SoapUI schema from mockService (real
// SoapUI projects commonly carry both), previously not modeled at all.
func TestImportMockServicesScaffoldsRestMockActions(t *testing.T) {
	scaffolds, err := ImportMockServices([]byte(sampleProject))
	if err != nil {
		t.Fatalf("ImportMockServices: %v", err)
	}
	var rest *MockServiceScaffold
	for i, s := range scaffolds {
		if s.Protocol == "rest" {
			rest = &scaffolds[i]
		}
	}
	if rest == nil {
		t.Fatalf("expected a REST scaffold from the restMockService, got %+v", scaffolds)
	}
	if rest.Method != "POST" || rest.PathPattern != "/account-migration" {
		t.Fatalf("expected POST /account-migration, got %s %s", rest.Method, rest.PathPattern)
	}
	if rest.StatusCode != 201 {
		t.Fatalf("expected the REST response's httpResponseStatus to carry over as 201, got %d", rest.StatusCode)
	}
	if want := `"status":"migrated"`; !strings.Contains(rest.ResponseBody, want) {
		t.Fatalf("expected the REST response body to carry over (containing %q), got %q", want, rest.ResponseBody)
	}
}

func TestImportMockServicesDefaultsPathFromServiceName(t *testing.T) {
	const noPath = `<con:soapui-project xmlns:con="http://eviware.com/soapui/config" name="P">
  <con:mockService name="NoPathService">
    <con:mockOperation name="Op" operation="Op">
      <con:response name="R"><con:responseContent>ok</con:responseContent></con:response>
    </con:mockOperation>
  </con:mockService>
</con:soapui-project>`
	scaffolds, err := ImportMockServices([]byte(noPath))
	if err != nil {
		t.Fatalf("ImportMockServices: %v", err)
	}
	if len(scaffolds) != 1 || scaffolds[0].PathPattern != "/NoPathService" {
		t.Fatalf("expected path to default to /<service name>, got %+v", scaffolds)
	}
}

// realWorldExcerpt is trimmed directly from a real, user-supplied SoapUI
// project export (the exact element/attribute names and nesting a real
// export produces, unlike sampleProject above which is hand-authored) —
// this is what caught the original mockResponse-vs-response tag mismatch
// no amount of self-consistent hand-authored test data ever could, so it's
// kept here as a standing regression guard against reintroducing an
// assumed-rather-than-confirmed schema detail.
const realWorldExcerpt = `<con:soapui-project xmlns:con="http://eviware.com/soapui/config" name="A1 OCC">
  <con:interface xsi:type="con:WsdlInterface" id="8254c3fc-a9a7-4261-b2f1-3cf0b0842bf8" name="bwsPortTypeHttpBinding" type="wsdl" xmlns:xsi="http://www.w3.org/2001/XMLSchema-instance">
    <con:settings/>
    <con:definitionCache type="TEXT">
      <con:part>
        <con:url>file:/wsdl.wsdl</con:url>
        <con:content><![CDATA[<wsdl:definitions xmlns:wsdl="http://schemas.xmlsoap.org/wsdl/" xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/">
  <wsdl:portType name="bwsPortType">
    <wsdl:operation name="brianOccAction"/>
  </wsdl:portType>
  <wsdl:binding name="bwsPortTypeHttpBinding" type="bwsPortType">
    <wsdl:operation name="brianOccAction">
      <soap:operation soapAction="/esb/OCCProvisioning//brianOccAction/1"/>
    </wsdl:operation>
  </wsdl:binding>
</wsdl:definitions>]]></con:content>
        <con:type>http://schemas.xmlsoap.org/wsdl/</con:type>
      </con:part>
    </con:definitionCache>
    <con:operation id="70587321-c63b-4ff4-aa2e-a8a4dceef07b" name="brianOccAction" bindingOperationName="brianOccAction" type="Request-Response">
      <con:settings/>
      <con:call id="dcdcee74-e438-4d4b-b99b-49e78299d462" name="Request 1">
        <con:settings/>
        <con:encoding>UTF-8</con:encoding>
        <con:endpoint>https://esb-e.a1telekom.inside:8443/esb/si:a1ta-dev</con:endpoint>
        <con:request><![CDATA[<soapenv:Envelope><soapenv:Body>\r
   <brianOccActionRequest>\r
      <occAction/>\r
   </brianOccActionRequest>\r
</soapenv:Body></soapenv:Envelope>]]></con:request>
      </con:call>
    </con:operation>
  </con:interface>
  <con:mockService id="4a536d71-efaf-499b-9e1d-dab7be03353d" port="6601" path="/mockbwsPortTypeHttpBinding" host="root-PC" name="bwsPortTypeHttpBinding MockService" bindToHostOnly="false" docroot="">
    <con:settings/>
    <con:properties/>
    <con:mockOperation name="brianOccAction" id="88ae1c90-cb54-487d-9366-ccf55f0414ae" interface="bwsPortTypeHttpBinding" operation="brianOccAction">
      <con:settings/>
      <con:defaultResponse>Response 1</con:defaultResponse>
      <con:dispatchStyle>SEQUENCE</con:dispatchStyle>
      <con:response name="Response 1" id="c28526cc-0000-0000-0000-000000000000" httpResponseStatus="200" encoding="UTF-8">
        <con:settings/>
        <con:responseContent><![CDATA[<soapenv:Envelope><soapenv:Body><brianOccActionResponse><result>ok</result></brianOccActionResponse></soapenv:Body></soapenv:Envelope>]]></con:responseContent>
      </con:response>
      <con:dispatchConfig/>
    </con:mockOperation>
  </con:mockService>
  <con:restMockService id="rms-1" port="8044" path="/" host="root-PC" name="REST MockService 1" docroot="">
    <con:settings/>
    <con:properties/>
    <con:restMockAction name="/account-migration" method="POST" resourcePath="/account-migration" id="rma-1">
      <con:settings/>
      <con:defaultResponse>Response 1</con:defaultResponse>
      <con:dispatchStyle>SEQUENCE</con:dispatchStyle>
      <con:dispatchPath>${request.body}</con:dispatchPath>
      <con:response name="Response 1" id="r-1" httpResponseStatus="200" mediaType="application/json">
        <con:settings/>
        <con:responseContent><![CDATA[{"status":"migrated"}]]></con:responseContent>
      </con:response>
    </con:restMockAction>
  </con:restMockService>
</con:soapui-project>`

func TestImportMockServicesAgainstRealWorldExcerpt(t *testing.T) {
	scaffolds, err := ImportMockServices([]byte(realWorldExcerpt))
	if err != nil {
		t.Fatalf("ImportMockServices: %v", err)
	}
	if len(scaffolds) != 2 {
		t.Fatalf("expected 1 SOAP + 1 REST scaffold from the real-world excerpt, got %d: %+v", len(scaffolds), scaffolds)
	}
	var soap, rest *MockServiceScaffold
	for i := range scaffolds {
		switch scaffolds[i].Protocol {
		case "soap":
			soap = &scaffolds[i]
		case "rest":
			rest = &scaffolds[i]
		}
	}
	if soap == nil {
		t.Fatal("expected the brianOccAction mockOperation to scaffold a SOAP mock")
	}
	if soap.OperationName != "brianOccAction" || soap.PathPattern != "/mockbwsPortTypeHttpBinding" {
		t.Fatalf("expected brianOccAction at /mockbwsPortTypeHttpBinding, got %+v", soap)
	}
	if !strings.Contains(soap.ResponseBody, "<result>ok</result>") {
		t.Fatalf("expected the SOAP response body to carry over, got %q", soap.ResponseBody)
	}
	if want := "/esb/OCCProvisioning//brianOccAction/1"; soap.SOAPAction != want {
		t.Fatalf("expected the SOAPAction to be pulled from the interface's embedded WSDL binding (%q), got %q", want, soap.SOAPAction)
	}
	if rest == nil {
		t.Fatal("expected the /account-migration restMockAction to scaffold a REST mock")
	}
	if rest.Method != "POST" || rest.PathPattern != "/account-migration" {
		t.Fatalf("expected POST /account-migration, got %+v", rest)
	}
}

// TestImportCollectionAgainstRealWorldExcerptWithNoTestSuites guards
// interface-level call import — a real SoapUI project commonly has NO
// testSuite at all (nothing automated, only manually saved "try it"
// requests hanging off each operation), a case ImportCollection
// previously produced an empty collection for since it only ever read
// TestSuites.
func TestImportCollectionAgainstRealWorldExcerptWithNoTestSuites(t *testing.T) {
	c, err := ImportCollection([]byte(realWorldExcerpt))
	if err != nil {
		t.Fatalf("ImportCollection: %v", err)
	}
	if len(c.Items) != 1 || c.Items[0].Name != "bwsPortTypeHttpBinding" {
		t.Fatalf("expected one top-level folder named after the interface, got %+v", c.Items)
	}
	iface := c.Items[0]
	if len(iface.Items) != 1 || iface.Items[0].Name != "brianOccAction" {
		t.Fatalf("expected one nested folder named after the operation, got %+v", iface.Items)
	}
	op := iface.Items[0]
	if len(op.Items) != 1 || op.Items[0].Name != "Request 1" {
		t.Fatalf("expected one request item named after the call, got %+v", op.Items)
	}
	req := op.Items[0]
	if req.Request == nil || req.Request.Method != "POST" {
		t.Fatalf("expected a POST request spec, got %+v", req.Request)
	}
	if req.Request.URL != "https://esb-e.a1telekom.inside:8443/esb/si:a1ta-dev" {
		t.Fatalf("expected the call's endpoint to carry over, got %q", req.Request.URL)
	}
	if want := "<occAction/>"; !strings.Contains(req.Request.Body, want) {
		t.Fatalf("expected the call's saved request body to carry over (containing %q), got %q", want, req.Request.Body)
	}
	// This fixture's request body carries SoapUI's own auto-generated
	// "blank sample request" quirk — literal `\r` (backslash + r) text
	// before each line instead of a real line break — confirmed present in
	// the user's real export. It must come through as an actual newline,
	// not the literal two-character escape sequence.
	if strings.Contains(req.Request.Body, `\r`) {
		t.Fatalf("expected literal \\r to be normalized to a real newline, got %q", req.Request.Body)
	}
	if !strings.Contains(req.Request.Body, "\n") {
		t.Fatalf("expected the body to retain real line breaks, got %q", req.Request.Body)
	}
	// Confirmed byte-for-byte against the real file: the literal `\r` is
	// immediately followed by a REAL newline byte already in the source —
	// folding the literal into another newline on top of that must not
	// leave a blank line behind.
	if strings.Contains(req.Request.Body, "\n\n") {
		t.Fatalf("expected no blank lines from double-folding \\r plus the real newline that already follows it, got %q", req.Request.Body)
	}

	headerValue := func(key string) (string, bool) {
		for _, h := range req.Request.Headers {
			if h.Key == key {
				return h.Value, true
			}
		}
		return "", false
	}
	if ct, ok := headerValue("Content-Type"); !ok || ct != "text/xml; charset=utf-8" {
		t.Fatalf("expected a Content-Type header, got %+v", req.Request.Headers)
	}
	if sa, ok := headerValue("SOAPAction"); !ok || sa != "/esb/OCCProvisioning//brianOccAction/1" {
		t.Fatalf("expected the SOAPAction header pulled from the embedded WSDL, got %+v", req.Request.Headers)
	}

	// The interface call and the mockService operation share the operation
	// name "brianOccAction" despite living in unrelated parts of the
	// project — the mock's own response should come through as this
	// request's saved Example.
	if len(req.Examples) != 1 {
		t.Fatalf("expected the matching mock's response to carry over as one Example, got %+v", req.Examples)
	}
	ex := req.Examples[0]
	if ex.ID == "" {
		t.Fatal("expected the example to have a generated ID")
	}
	if ex.StatusCode != 200 {
		t.Fatalf("expected status 200 from the mock's httpResponseStatus, got %d", ex.StatusCode)
	}
	if want := "<result>ok</result>"; !strings.Contains(ex.Body, want) {
		t.Fatalf("expected the mock response body to carry over (containing %q), got %q", want, ex.Body)
	}
}

// TestImportCollectionOnPlainWSDLGivesFriendlyError guards the real mix-up
// that prompted this: a plain WSDL (root <definitions>, no <soapui-project>
// wrapper) pasted into the SoapUI import — encoding/xml's own error here
// ("expected element type <soapui-project> but have <definitions>") is
// technically accurate but reads as an internal error, not "wrong document
// type, use Import WSDL instead".
func TestImportCollectionOnPlainWSDLGivesFriendlyError(t *testing.T) {
	const plainWSDL = `<?xml version="1.0"?><definitions><portType name="X"/></definitions>`
	_, err := ImportCollection([]byte(plainWSDL))
	if err == nil {
		t.Fatal("expected an error for a plain WSDL, got none")
	}
	if !strings.Contains(err.Error(), "<definitions>") || !strings.Contains(err.Error(), "Import WSDL") {
		t.Fatalf("expected a friendly error naming <definitions> and pointing at Import WSDL, got %q", err.Error())
	}
}

// TestImportMockServicesOnPlainWSDLGivesFriendlyError is the same guard for
// the mock-scaffolding import path (ImportMockServices), which hits the
// identical parse failure independently of ImportCollection.
func TestImportMockServicesOnPlainWSDLGivesFriendlyError(t *testing.T) {
	const plainWSDL = `<?xml version="1.0"?><definitions><portType name="X"/></definitions>`
	_, err := ImportMockServices([]byte(plainWSDL))
	if err == nil {
		t.Fatal("expected an error for a plain WSDL, got none")
	}
	if !strings.Contains(err.Error(), "<definitions>") || !strings.Contains(err.Error(), "Import WSDL") {
		t.Fatalf("expected a friendly error naming <definitions> and pointing at Import WSDL, got %q", err.Error())
	}
}

// TestImportCollectionOnUnrelatedXMLFallsBackToRawError confirms a
// genuinely unrelated root element (neither <soapui-project> nor
// <definitions> — nothing this package has a specific hint for) still
// surfaces an error, just without a false "use Import WSDL" hint.
func TestImportCollectionOnUnrelatedXMLFallsBackToRawError(t *testing.T) {
	_, err := ImportCollection([]byte(`<?xml version="1.0"?><rss><channel/></rss>`))
	if err == nil {
		t.Fatal("expected an error for an unrelated document, got none")
	}
	if strings.Contains(err.Error(), "Import WSDL") {
		t.Fatalf("expected no WSDL hint for an unrelated root element, got %q", err.Error())
	}
}
