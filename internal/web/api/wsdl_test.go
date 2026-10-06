package api

import (
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/storage"
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
  </wsdl:portType>
  <wsdl:binding name="OrderServiceBinding" type="tns:OrderServicePortType">
    <soap:binding style="document" transport="http://schemas.xmlsoap.org/soap/http"/>
    <wsdl:operation name="GetOrder">
      <soap:operation soapAction="urn:orders#GetOrder"/>
      <wsdl:input><soap:body use="literal"/></wsdl:input>
      <wsdl:output><soap:body use="literal"/></wsdl:output>
    </wsdl:operation>
  </wsdl:binding>
</wsdl:definitions>`

func newTestWSDLRouter(t *testing.T) (chi.Router, *mock.Store) {
	t.Helper()
	engine.Register(&fakeEngine{name: "http"})
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	store := mock.NewStore(db)
	r := chi.NewRouter()
	r.Route("/api/wsdl", NewWSDLHandler(store).Routes)
	return r, store
}

// TestWSDLImportAssignsRequestedProject guards the same "import into a
// chosen Mock Project instead of always landing Ungrouped" behavior as the
// bulk JSON import — WSDL import scaffolds several mocks at once from one
// spec, so grouping them under one project on the way in matters even more.
func TestWSDLImportAssignsRequestedProject(t *testing.T) {
	r, store := newTestWSDLRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/wsdl/import", map[string]any{
		"wsdlContent": fixtureWSDL, "pathPattern": "/soap/orders", "projectId": "proj-1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].ProjectID != "proj-1" {
		t.Fatalf("expected the one scaffolded mock to be assigned to proj-1, got %+v", all)
	}
}

func TestWSDLImportDefaultsToUngroupedWithoutAProjectID(t *testing.T) {
	r, store := newTestWSDLRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/wsdl/import", map[string]any{
		"wsdlContent": fixtureWSDL, "pathPattern": "/soap/orders",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 || all[0].ProjectID != "" {
		t.Fatalf("expected the scaffolded mock to be ungrouped by default, got %+v", all)
	}
}

const fixtureSoapUIProject = `<con:soapui-project xmlns:con="http://eviware.com/soapui/config" name="Billing Project">
  <con:mockService name="Billing Mock" path="/mockBilling">
    <con:mockOperation name="GetInvoiceMock" interface="BillingSoap" operation="GetInvoice">
      <con:response name="Response 1" httpResponseStatus="200">
        <con:responseContent><![CDATA[<soapenv:Envelope><soapenv:Body><GetInvoiceResponse><total>42</total></GetInvoiceResponse></soapenv:Body></soapenv:Envelope>]]></con:responseContent>
      </con:response>
    </con:mockOperation>
  </con:mockService>
</con:soapui-project>`

// TestImportSoapUIMocksScaffoldsOneMockPerOperation is the mock-import
// counterpart of TestWSDLImportAssignsRequestedProject: a pasted SoapUI
// project export's MockService operations must scaffold real, enabled SOAP
// mocks carrying their own already-authored response body (not a generic
// stub, since — unlike a bare WSDL — a SoapUI mock operation actually has
// one to use).
func TestImportSoapUIMocksScaffoldsOneMockPerOperation(t *testing.T) {
	r, store := newTestWSDLRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/wsdl/import-soapui-mocks", map[string]any{
		"projectXml": fixtureSoapUIProject, "projectId": "proj-1",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly one scaffolded mock, got %+v", all)
	}
	m := all[0]
	if m.ProtocolType != "soap" || m.PathPattern != "/mockBilling" || m.OperationName != "GetInvoice" {
		t.Fatalf("expected a SOAP mock at /mockBilling for operation GetInvoice, got %+v", m)
	}
	if m.ProjectID != "proj-1" {
		t.Fatalf("expected the scaffolded mock to be assigned to proj-1, got %+v", m)
	}
	if !m.Enabled {
		t.Fatal("expected the scaffolded mock to be enabled")
	}
	if wantSub := "<total>42</total>"; m.Response.BodyTemplate == "" || !strings.Contains(m.Response.BodyTemplate, wantSub) {
		t.Fatalf("expected the mock's response to carry over the SoapUI mockResponse body (containing %q), got %q", wantSub, m.Response.BodyTemplate)
	}
}

const fixtureSoapUIProjectWithWSDL = `<con:soapui-project xmlns:con="http://eviware.com/soapui/config" name="Billing Project">
  <con:interface name="BillingSoap">
    <con:definitionCache>
      <con:part>
        <con:content><![CDATA[<wsdl:definitions xmlns:wsdl="http://schemas.xmlsoap.org/wsdl/" xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/">
  <wsdl:portType name="BillingSoapPortType">
    <wsdl:operation name="GetInvoice"/>
  </wsdl:portType>
  <wsdl:binding name="BillingSoapBinding" type="BillingSoapPortType">
    <wsdl:operation name="GetInvoice">
      <soap:operation soapAction="urn:billing#GetInvoice"/>
    </wsdl:operation>
  </wsdl:binding>
</wsdl:definitions>]]></con:content>
      </con:part>
    </con:definitionCache>
  </con:interface>
  <con:mockService name="Billing Mock" path="/mockBilling">
    <con:mockOperation name="GetInvoiceMock" interface="BillingSoap" operation="GetInvoice">
      <con:response name="Response 1" httpResponseStatus="200">
        <con:responseContent><![CDATA[<soapenv:Envelope><soapenv:Body><GetInvoiceResponse><total>42</total></GetInvoiceResponse></soapenv:Body></soapenv:Envelope>]]></con:responseContent>
      </con:response>
    </con:mockOperation>
  </con:mockService>
</con:soapui-project>`

// TestImportSoapUIMocksCarriesOverSOAPActionFromEmbeddedWSDL guards a real
// gap found via the UI's "how to test this" curl snippet: without a
// SOAPAction on the scaffolded mock.Definition, that snippet fell back to
// showing the bare operation name as a fake SOAPAction value instead of
// the real one a client actually needs to invoke the operation. SoapUI
// itself never repeats SOAPAction anywhere in its own project config — the
// only place it lives is the embedded WSDL SoapUI saved alongside the
// interface — so the importer must read it from there.
func TestImportSoapUIMocksCarriesOverSOAPActionFromEmbeddedWSDL(t *testing.T) {
	r, store := newTestWSDLRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/wsdl/import-soapui-mocks", map[string]any{
		"projectXml": fixtureSoapUIProjectWithWSDL,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly one scaffolded mock, got %+v", all)
	}
	if want := "urn:billing#GetInvoice"; all[0].SOAPAction != want {
		t.Fatalf("expected SOAPAction %q pulled from the embedded WSDL, got %q", want, all[0].SOAPAction)
	}
}

const fixtureSoapUIRestMockProject = `<con:soapui-project xmlns:con="http://eviware.com/soapui/config" name="Account Migration Project">
  <con:restMockService name="REST MockService 1" path="/">
    <con:restMockAction name="/account-migration" method="POST" resourcePath="/account-migration">
      <con:response name="Response 1" httpResponseStatus="201" mediaType="application/json">
        <con:responseContent><![CDATA[{"status":"migrated"}]]></con:responseContent>
      </con:response>
    </con:restMockAction>
  </con:restMockService>
</con:soapui-project>`

// TestImportSoapUIMocksScaffoldsRestMockAction guards restMockService
// import end to end through the HTTP handler — a completely separate
// SoapUI schema from mockService that real projects commonly carry
// alongside (or instead of) SOAP mocks, and one this handler didn't
// recognize at all until this fix.
func TestImportSoapUIMocksScaffoldsRestMockAction(t *testing.T) {
	r, store := newTestWSDLRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/wsdl/import-soapui-mocks", map[string]any{
		"projectXml": fixtureSoapUIRestMockProject,
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	all, err := store.List()
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(all) != 1 {
		t.Fatalf("expected exactly one scaffolded mock, got %+v", all)
	}
	m := all[0]
	if m.ProtocolType != "rest" || m.Method != "POST" || m.PathPattern != "/account-migration" {
		t.Fatalf("expected a REST mock at POST /account-migration, got %+v", m)
	}
	if m.Response.StatusCode != 201 {
		t.Fatalf("expected the response's httpResponseStatus to carry over as 201, got %d", m.Response.StatusCode)
	}
	if want := `"status":"migrated"`; !strings.Contains(m.Response.BodyTemplate, want) {
		t.Fatalf("expected the REST response body to carry over (containing %q), got %q", want, m.Response.BodyTemplate)
	}
}

func TestImportSoapUIMocksRejectsProjectWithNoMockServices(t *testing.T) {
	r, _ := newTestWSDLRouter(t)
	rec := doJSON(t, r, http.MethodPost, "/api/wsdl/import-soapui-mocks", map[string]any{
		"projectXml": `<con:soapui-project xmlns:con="http://eviware.com/soapui/config" name="Empty"></con:soapui-project>`,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a project with no mock services, got %d: %s", rec.Code, rec.Body.String())
	}
}
