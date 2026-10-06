package api

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/apiclient"
	"github.com/addictedabhi/airmock/internal/apiclient/postman"
	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/storage"
	"github.com/addictedabhi/airmock/internal/wslock"
)

func newTestAPIClientRouter(t *testing.T) chi.Router {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	r := chi.NewRouter()
	r.Route("/api/apiclient", NewAPIClientHandler(apiclient.NewStore(db), certs.NewStore(db), nil, wslock.New()).Routes)
	return r
}

func TestCodeSnippetDispatchesEachLanguage(t *testing.T) {
	r := newTestAPIClientRouter(t)
	spec := map[string]any{"method": "GET", "url": "https://example.com"}

	cases := []struct {
		language string
		want     string
	}{
		{"curl", "curl -X GET"},
		{"js", "fetch("},
		{"python", "import requests"},
		{"go", "package main"},
		{"", "curl -X GET"}, // empty language defaults to curl, same as curlExport's own behavior
	}
	for _, tt := range cases {
		rec := doJSON(t, r, "POST", "/api/apiclient/snippet", map[string]any{"spec": spec, "language": tt.language})
		if rec.Code != 200 {
			t.Fatalf("language %q: expected 200, got %d: %s", tt.language, rec.Code, rec.Body.String())
		}
		var body map[string]string
		if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
			t.Fatalf("language %q: decode response: %v", tt.language, err)
		}
		if len(body["code"]) == 0 {
			t.Fatalf("language %q: expected non-empty code, got %+v", tt.language, body)
		}
		if !strings.Contains(body["code"], tt.want) {
			t.Fatalf("language %q: expected code to contain %q, got %q", tt.language, tt.want, body["code"])
		}
	}
}

func TestCodeSnippetRejectsUnknownLanguage(t *testing.T) {
	r := newTestAPIClientRouter(t)
	rec := doJSON(t, r, "POST", "/api/apiclient/snippet", map[string]any{
		"spec":     map[string]any{"method": "GET", "url": "https://example.com"},
		"language": "ruby",
	})
	if rec.Code != 400 {
		t.Fatalf("expected 400 for an unknown language, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestImportEnvironmentCreatesEnvironmentFromPostmanFile guards against a
// real gap: there was no way to import a Postman *.postman_environment.json
// export at all, only a collection.
func TestImportEnvironmentCreatesEnvironmentFromPostmanFile(t *testing.T) {
	r := newTestAPIClientRouter(t)
	postmanEnv := `{"name": "Staging", "values": [{"key": "baseUrl", "value": "https://staging.example.com", "enabled": true}]}`
	rec := doJSON(t, r, "POST", "/api/apiclient/environments/import", map[string]any{"postmanJson": postmanEnv})
	if rec.Code != 201 {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}

	rec2 := doJSON(t, r, "GET", "/api/apiclient/environments", nil)
	var envs []map[string]any
	if err := json.Unmarshal(rec2.Body.Bytes(), &envs); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(envs) != 1 || envs[0]["name"] != "Staging" {
		t.Fatalf("expected the imported environment, got %+v", envs)
	}
	vars, _ := envs[0]["variables"].(map[string]any)
	if vars["baseUrl"] != "https://staging.example.com" {
		t.Fatalf("expected the imported variable, got %+v", vars)
	}
}

// TestWSLoadTestRouteIsWired guards against a real gap: /loadtest only ever
// exercised the HTTP Execute path, so there was no way to load-test a WS
// endpoint at all — an unreachable address is enough to confirm the route
// reaches apiclient.RunWSLoadTest (rather than 404ing or panicking) and
// that its result shape decodes as expected, without needing a live WS
// server in this handler-level test.
func TestWSLoadTestRouteIsWired(t *testing.T) {
	r := newTestAPIClientRouter(t)
	rec := doJSON(t, r, "POST", "/api/apiclient/ws-loadtest", map[string]any{
		"spec":          map[string]any{"url": "ws://127.0.0.1:1"},
		"concurrency":   1,
		"totalRequests": 3,
	})
	if rec.Code != 200 {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result apiclient.LoadTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if result.TotalRequests != 3 || result.Statuses.CountError != 3 {
		t.Fatalf("expected all 3 exchanges to fail at the dial level, got %+v", result)
	}
}

// --- workspaces ---

func TestListWorkspacesIncludesDefaultAndCreated(t *testing.T) {
	r := newTestAPIClientRouter(t)

	rec := doJSON(t, r, "POST", "/api/apiclient/workspaces", map[string]any{"name": "Acme"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created apiclient.Workspace
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created workspace: %v", err)
	}
	if created.ID == "" || created.Name != "Acme" {
		t.Fatalf("expected a populated workspace, got %+v", created)
	}

	listRec := doJSON(t, r, "GET", "/api/apiclient/workspaces", nil)
	var list []apiclient.Workspace
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	// migration 000014 seeds a "Default" workspace in every fresh store, plus
	// the one just created above.
	if len(list) != 2 {
		t.Fatalf("expected 2 workspaces (Default + Acme), got %+v", list)
	}
	names := map[string]bool{}
	for _, w := range list {
		names[w.Name] = true
	}
	if !names["Default"] || !names["Acme"] {
		t.Fatalf("expected Default and Acme in the list, got %+v", list)
	}
}

func TestCreateWorkspaceRejectsDuplicateName(t *testing.T) {
	r := newTestAPIClientRouter(t)
	first := doJSON(t, r, "POST", "/api/apiclient/workspaces", map[string]any{"name": "Acme"})
	if first.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", first.Code, first.Body.String())
	}

	dup := doJSON(t, r, "POST", "/api/apiclient/workspaces", map[string]any{"name": "Acme"})
	if dup.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a duplicate workspace name, got %d: %s", dup.Code, dup.Body.String())
	}
}

func TestDeleteWorkspaceRemovesItFromList(t *testing.T) {
	r := newTestAPIClientRouter(t)
	created := doJSON(t, r, "POST", "/api/apiclient/workspaces", map[string]any{"name": "Temp"})
	var ws apiclient.Workspace
	if err := json.Unmarshal(created.Body.Bytes(), &ws); err != nil {
		t.Fatalf("decode: %v", err)
	}

	del := doJSON(t, r, "DELETE", "/api/apiclient/workspaces/"+ws.ID, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", del.Code, del.Body.String())
	}

	listRec := doJSON(t, r, "GET", "/api/apiclient/workspaces", nil)
	var list []apiclient.Workspace
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode: %v", err)
	}
	for _, w := range list {
		if w.ID == ws.ID {
			t.Fatalf("expected the deleted workspace to be gone from the list, got %+v", list)
		}
	}
}

// TestDeleteWorkspaceNotFound needs a second real workspace to exist first:
// Store.DeleteWorkspace counts every workspace in the install (not just ones
// matching the given id) and refuses to go below 1 — with only the
// migration-seeded "Default" workspace present, deleting any bogus id would
// be masked by that "last workspace" guard (ErrLastWorkspace) rather than
// ever reaching the real not-found path.
func TestDeleteWorkspaceNotFound(t *testing.T) {
	r := newTestAPIClientRouter(t)
	doJSON(t, r, "POST", "/api/apiclient/workspaces", map[string]any{"name": "Other"})

	rec := doJSON(t, r, "DELETE", "/api/apiclient/workspaces/does-not-exist", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- collections ---

func TestListCollectionsScopedByWorkspaceVersusAll(t *testing.T) {
	r := newTestAPIClientRouter(t)

	wsRec := doJSON(t, r, "POST", "/api/apiclient/workspaces", map[string]any{"name": "Other"})
	var ws apiclient.Workspace
	if err := json.Unmarshal(wsRec.Body.Bytes(), &ws); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}

	doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{"name": "InDefault"})
	doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{"name": "InOther", "workspaceId": ws.ID})

	allRec := doJSON(t, r, "GET", "/api/apiclient/collections", nil)
	var all []apiclient.Collection
	if err := json.Unmarshal(allRec.Body.Bytes(), &all); err != nil {
		t.Fatalf("decode all: %v", err)
	}
	if len(all) != 2 {
		t.Fatalf("expected 2 collections across all workspaces, got %+v", all)
	}

	scopedRec := doJSON(t, r, "GET", "/api/apiclient/collections?workspaceId="+ws.ID, nil)
	var scoped []apiclient.Collection
	if err := json.Unmarshal(scopedRec.Body.Bytes(), &scoped); err != nil {
		t.Fatalf("decode scoped: %v", err)
	}
	if len(scoped) != 1 || scoped[0].Name != "InOther" {
		t.Fatalf("expected only InOther scoped to the other workspace, got %+v", scoped)
	}
}

func TestCreateCollectionRejectsDuplicateNameInSameWorkspaceButAllowsInDifferent(t *testing.T) {
	r := newTestAPIClientRouter(t)

	wsRec := doJSON(t, r, "POST", "/api/apiclient/workspaces", map[string]any{"name": "Other"})
	var ws apiclient.Workspace
	if err := json.Unmarshal(wsRec.Body.Bytes(), &ws); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}

	first := doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{"name": "Shared"})
	if first.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", first.Code, first.Body.String())
	}

	dup := doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{"name": "Shared"})
	if dup.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a duplicate name in the same workspace, got %d: %s", dup.Code, dup.Body.String())
	}

	otherWorkspace := doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{"name": "Shared", "workspaceId": ws.ID})
	if otherWorkspace.Code != http.StatusCreated {
		t.Fatalf("expected the same name to be allowed in a different workspace, got %d: %s", otherWorkspace.Code, otherWorkspace.Body.String())
	}
}

func TestGetCollectionSuccessAndNotFound(t *testing.T) {
	r := newTestAPIClientRouter(t)
	createRec := doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{"name": "Coll"})
	var created apiclient.Collection
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	getRec := doJSON(t, r, "GET", "/api/apiclient/collections/"+created.ID, nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var got apiclient.Collection
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("decode got: %v", err)
	}
	if got.Name != "Coll" {
		t.Fatalf("expected Coll, got %+v", got)
	}

	notFound := doJSON(t, r, "GET", "/api/apiclient/collections/does-not-exist", nil)
	if notFound.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", notFound.Code, notFound.Body.String())
	}
}

func TestUpdateCollectionSuccessNotFoundAndDuplicateNameOnRename(t *testing.T) {
	r := newTestAPIClientRouter(t)
	c1 := doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{"name": "First"})
	var coll1 apiclient.Collection
	if err := json.Unmarshal(c1.Body.Bytes(), &coll1); err != nil {
		t.Fatalf("decode coll1: %v", err)
	}

	c2 := doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{"name": "Second"})
	if c2.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", c2.Code, c2.Body.String())
	}

	upd := doJSON(t, r, "PUT", "/api/apiclient/collections/"+coll1.ID, map[string]any{"name": "Renamed"})
	if upd.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", upd.Code, upd.Body.String())
	}
	var updated apiclient.Collection
	if err := json.Unmarshal(upd.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated: %v", err)
	}
	if updated.Name != "Renamed" {
		t.Fatalf("expected the renamed collection, got %+v", updated)
	}

	nf := doJSON(t, r, "PUT", "/api/apiclient/collections/does-not-exist", map[string]any{"name": "X"})
	if nf.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", nf.Code, nf.Body.String())
	}

	dup := doJSON(t, r, "PUT", "/api/apiclient/collections/"+coll1.ID, map[string]any{"name": "Second"})
	if dup.Code != http.StatusConflict {
		t.Fatalf("expected 409 renaming into a name already used by the Second collection, got %d: %s", dup.Code, dup.Body.String())
	}
}

func TestDeleteCollectionSuccessAndNotFound(t *testing.T) {
	r := newTestAPIClientRouter(t)
	created := doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{"name": "ToDelete"})
	var coll apiclient.Collection
	if err := json.Unmarshal(created.Body.Bytes(), &coll); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	del := doJSON(t, r, "DELETE", "/api/apiclient/collections/"+coll.ID, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", del.Code, del.Body.String())
	}

	getRec := doJSON(t, r, "GET", "/api/apiclient/collections/"+coll.ID, nil)
	if getRec.Code != http.StatusNotFound {
		t.Fatalf("expected the collection to be gone, got %d", getRec.Code)
	}

	nf := doJSON(t, r, "DELETE", "/api/apiclient/collections/does-not-exist", nil)
	if nf.Code != http.StatusNotFound {
		t.Fatalf("expected 404 deleting a nonexistent collection, got %d: %s", nf.Code, nf.Body.String())
	}
}

// TestExportCollectionReturnsPostmanJSON checks both the download framing
// (Content-Disposition naming the collection) and that the body is a real
// Postman Collection v2.1 document by round-tripping it back through
// postman.Import, rather than just asserting on raw JSON keys.
func TestExportCollectionReturnsPostmanJSON(t *testing.T) {
	r := newTestAPIClientRouter(t)
	created := doJSON(t, r, "POST", "/api/apiclient/collections", map[string]any{
		"name": "Exportable",
		"items": []map[string]any{
			{"type": "request", "name": "Get Item", "request": map[string]any{"method": "GET", "url": "https://example.com/items"}},
		},
	})
	if created.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", created.Code, created.Body.String())
	}
	var coll apiclient.Collection
	if err := json.Unmarshal(created.Body.Bytes(), &coll); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	exp := doJSON(t, r, "GET", "/api/apiclient/collections/"+coll.ID+"/export", nil)
	if exp.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", exp.Code, exp.Body.String())
	}
	cd := exp.Header().Get("Content-Disposition")
	if !strings.Contains(cd, "Exportable.postman_collection.json") {
		t.Fatalf("expected a Content-Disposition download header naming the collection, got %q", cd)
	}

	roundTripped, err := postman.Import(exp.Body.Bytes())
	if err != nil {
		t.Fatalf("postman.Import(export output): %v", err)
	}
	if roundTripped.Name != "Exportable" || len(roundTripped.Items) != 1 || roundTripped.Items[0].Name != "Get Item" {
		t.Fatalf("expected the export to round-trip via postman.Import, got %+v", roundTripped)
	}
}

func TestExportCollectionNotFound(t *testing.T) {
	r := newTestAPIClientRouter(t)
	rec := doJSON(t, r, "GET", "/api/apiclient/collections/does-not-exist/export", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestImportCollectionCreatesCollectionFromPostmanJSON is the one path of
// /collections/import that had no test coverage at all: a pasted Postman
// v2.1 collection JSON must actually create and persist a real collection,
// not just be parsed.
func TestImportCollectionCreatesCollectionFromPostmanJSON(t *testing.T) {
	r := newTestAPIClientRouter(t)
	postmanJSON := `{
		"info": {"name": "Imported Collection", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"},
		"item": [
			{"name": "List Items", "request": {"method": "GET", "url": {"raw": "https://example.com/items"}}}
		]
	}`

	rec := doJSON(t, r, "POST", "/api/apiclient/collections/import", map[string]any{"postmanJson": postmanJSON})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created apiclient.Collection
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Name != "Imported Collection" || len(created.Items) != 1 || created.Items[0].Name != "List Items" {
		t.Fatalf("expected the parsed postman collection to be created, got %+v", created)
	}

	listRec := doJSON(t, r, "GET", "/api/apiclient/collections", nil)
	var list []apiclient.Collection
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("expected the imported collection to be persisted, got %+v", list)
	}
}

// TestImportCollectionDedupesNameCollision guards createCollectionDeduped's
// retry-on-collision behavior for the collections/import path specifically
// (TestImportEnvironmentCreatesEnvironmentFromPostmanFile already covers the
// environments/import sibling, but nothing exercised this one).
func TestImportCollectionDedupesNameCollision(t *testing.T) {
	r := newTestAPIClientRouter(t)
	postmanJSON := `{"info": {"name": "Widgets", "schema": "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"}, "item": []}`

	first := doJSON(t, r, "POST", "/api/apiclient/collections/import", map[string]any{"postmanJson": postmanJSON})
	if first.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", first.Code, first.Body.String())
	}

	second := doJSON(t, r, "POST", "/api/apiclient/collections/import", map[string]any{"postmanJson": postmanJSON})
	if second.Code != http.StatusCreated {
		t.Fatalf("expected the colliding import to still succeed (deduped), got %d: %s", second.Code, second.Body.String())
	}
	var created apiclient.Collection
	if err := json.Unmarshal(second.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if created.Name != "Widgets (2)" {
		t.Fatalf("expected the second import to be renamed 'Widgets (2)', got %q", created.Name)
	}
}

// TestImportSoapUICollectionCreatesCollectionFromProjectXML mirrors
// TestImportCollectionCreatesCollectionFromPostmanJSON for the SoapUI XML
// sibling — a pasted SoapUI project export must create a real, persisted
// collection with its TestSuite/TestCase structure preserved as nested
// folders.
func TestImportSoapUICollectionCreatesCollectionFromProjectXML(t *testing.T) {
	r := newTestAPIClientRouter(t)
	projectXML := `<con:soapui-project xmlns:con="http://eviware.com/soapui/config" name="Imported SoapUI Project">
  <con:testSuite name="Suite 1">
    <con:testCase name="Case 1">
      <con:testStep name="List Items" type="restrequest">
        <con:config xsi:type="con:RestRequestStep">
          <con:restRequest name="List Items" method="GET">
            <con:endpoint>https://example.com/items</con:endpoint>
          </con:restRequest>
        </con:config>
      </con:testStep>
    </con:testCase>
  </con:testSuite>
</con:soapui-project>`

	rec := doJSON(t, r, "POST", "/api/apiclient/collections/import-soapui", map[string]any{"projectXml": projectXML})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created apiclient.Collection
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Name != "Imported SoapUI Project" {
		t.Fatalf("expected the project name to carry over, got %q", created.Name)
	}
	if len(created.Items) != 1 || created.Items[0].Name != "Suite 1" {
		t.Fatalf("expected one top-level folder named 'Suite 1', got %+v", created.Items)
	}
	caseFolder := created.Items[0]
	if len(caseFolder.Items) != 1 || caseFolder.Items[0].Name != "Case 1" {
		t.Fatalf("expected one nested folder named 'Case 1', got %+v", caseFolder.Items)
	}
	step := caseFolder.Items[0].Items
	if len(step) != 1 || step[0].Name != "List Items" || step[0].Request == nil || step[0].Request.URL != "https://example.com/items" {
		t.Fatalf("expected the REST test step to import as a request item, got %+v", step)
	}

	listRec := doJSON(t, r, "GET", "/api/apiclient/collections", nil)
	var list []apiclient.Collection
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("expected the imported collection to be persisted, got %+v", list)
	}
}

// TestImportSoapUICollectionCarriesOverHeadersAndMockExample guards the
// interface-call import path end to end through the HTTP handler: a
// project with no TestSuite (only a saved interface call) must still
// import a request item, and that item must carry the Content-Type/
// SOAPAction headers a SOAP call actually needs plus the matching
// mockService operation's response as a saved Example — both were
// previously dropped entirely (no headers were ever set on an imported
// SOAP request, and mock responses were never cross-referenced into an
// Example).
func TestImportSoapUICollectionCarriesOverHeadersAndMockExample(t *testing.T) {
	r := newTestAPIClientRouter(t)
	projectXML := `<con:soapui-project xmlns:con="http://eviware.com/soapui/config" name="Billing Project">
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
    <con:operation name="GetInvoice">
      <con:call name="Request 1">
        <con:endpoint>https://example.com/billing</con:endpoint>
        <con:request><![CDATA[<soapenv:Envelope><soapenv:Body><GetInvoiceRequest><id>1</id></GetInvoiceRequest></soapenv:Body></soapenv:Envelope>]]></con:request>
      </con:call>
    </con:operation>
  </con:interface>
  <con:mockService name="Billing Mock" path="/mockBilling">
    <con:mockOperation name="GetInvoiceMock" interface="BillingSoap" operation="GetInvoice">
      <con:response name="Response 1" httpResponseStatus="200">
        <con:responseContent><![CDATA[<soapenv:Envelope><soapenv:Body><GetInvoiceResponse><total>42</total></GetInvoiceResponse></soapenv:Body></soapenv:Envelope>]]></con:responseContent>
      </con:response>
    </con:mockOperation>
  </con:mockService>
</con:soapui-project>`

	rec := doJSON(t, r, "POST", "/api/apiclient/collections/import-soapui", map[string]any{"projectXml": projectXML})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created apiclient.Collection
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if len(created.Items) != 1 || created.Items[0].Name != "BillingSoap" {
		t.Fatalf("expected one top-level folder named after the interface, got %+v", created.Items)
	}
	opFolder := created.Items[0]
	if len(opFolder.Items) != 1 || opFolder.Items[0].Name != "GetInvoice" {
		t.Fatalf("expected one nested folder named after the operation, got %+v", opFolder.Items)
	}
	reqItem := opFolder.Items[0].Items
	if len(reqItem) != 1 || reqItem[0].Name != "Request 1" || reqItem[0].Request == nil {
		t.Fatalf("expected one request item named after the call, got %+v", reqItem)
	}
	item := reqItem[0]

	headerValue := func(key string) (string, bool) {
		for _, h := range item.Request.Headers {
			if h.Key == key {
				return h.Value, true
			}
		}
		return "", false
	}
	if ct, ok := headerValue("Content-Type"); !ok || ct != "text/xml; charset=utf-8" {
		t.Fatalf("expected a Content-Type header, got %+v", item.Request.Headers)
	}
	if sa, ok := headerValue("SOAPAction"); !ok || sa != "urn:billing#GetInvoice" {
		t.Fatalf("expected the SOAPAction header from the embedded WSDL, got %+v", item.Request.Headers)
	}

	if len(item.Examples) != 1 {
		t.Fatalf("expected the matching mock's response to carry over as one Example, got %+v", item.Examples)
	}
	if want := "<total>42</total>"; !strings.Contains(item.Examples[0].Body, want) {
		t.Fatalf("expected the mock response body to carry over (containing %q), got %q", want, item.Examples[0].Body)
	}
}

// TestImportWSDLCollectionCreatesOneRequestPerOperation guards the plain-
// WSDL (no SoapUI project wrapper) collection import path: the document's
// own name carries over as the collection name, its <service><port>
// endpoint is used as every request's URL since none was supplied, and each
// portType operation becomes one request item carrying the Content-Type/
// SOAPAction headers a SOAP call needs.
func TestImportWSDLCollectionCreatesOneRequestPerOperation(t *testing.T) {
	r := newTestAPIClientRouter(t)
	wsdlXML := `<?xml version="1.0"?>
<definitions name="BillingService"
    xmlns="http://schemas.xmlsoap.org/wsdl/"
    xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/">
  <portType name="BillingPortType">
    <operation name="GetInvoice"/>
  </portType>
  <binding name="BillingBinding" type="BillingPortType">
    <operation name="GetInvoice">
      <soap:operation soapAction="urn:billing#GetInvoice"/>
    </operation>
  </binding>
  <service name="BillingService">
    <port name="BillingPort" binding="BillingBinding">
      <soap:address location="https://billing.example.com/soap"/>
    </port>
  </service>
</definitions>`

	rec := doJSON(t, r, "POST", "/api/apiclient/collections/import-wsdl", map[string]any{"wsdlContent": wsdlXML})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created apiclient.Collection
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Name != "BillingService" {
		t.Fatalf("expected the WSDL's own name to carry over, got %q", created.Name)
	}
	if len(created.Items) != 1 || created.Items[0].Name != "GetInvoice" || created.Items[0].Request == nil {
		t.Fatalf("expected one request item named after the operation, got %+v", created.Items)
	}
	item := created.Items[0]
	if item.Request.URL != "https://billing.example.com/soap" {
		t.Fatalf("expected the WSDL's own service endpoint as the URL, got %q", item.Request.URL)
	}
	if item.Request.Method != http.MethodPost {
		t.Fatalf("expected a POST request, got %q", item.Request.Method)
	}

	headerValue := func(key string) (string, bool) {
		for _, h := range item.Request.Headers {
			if h.Key == key {
				return h.Value, true
			}
		}
		return "", false
	}
	if ct, ok := headerValue("Content-Type"); !ok || ct != "text/xml; charset=utf-8" {
		t.Fatalf("expected a Content-Type header, got %+v", item.Request.Headers)
	}
	if sa, ok := headerValue("SOAPAction"); !ok || sa != "urn:billing#GetInvoice" {
		t.Fatalf("expected the SOAPAction header from the binding, got %+v", item.Request.Headers)
	}
	if !strings.Contains(item.Request.Body, "GetInvoice") {
		t.Fatalf("expected the stub request envelope to reference the operation name, got %q", item.Request.Body)
	}
}

// TestImportWSDLCollectionExplicitURLOverridesServiceEndpoint confirms a
// user-supplied URL wins over the WSDL's own declared endpoint — useful
// when the WSDL documents a production address but the user wants requests
// pointed at a local AirMock mock instead.
func TestImportWSDLCollectionExplicitURLOverridesServiceEndpoint(t *testing.T) {
	r := newTestAPIClientRouter(t)
	wsdlXML := `<?xml version="1.0"?>
<definitions name="X" xmlns="http://schemas.xmlsoap.org/wsdl/" xmlns:soap="http://schemas.xmlsoap.org/wsdl/soap/">
  <portType name="PT"><operation name="Ping"/></portType>
  <service name="X"><port name="P" binding="B"><soap:address location="https://prod.example.com/soap"/></port></service>
</definitions>`

	rec := doJSON(t, r, "POST", "/api/apiclient/collections/import-wsdl", map[string]any{
		"wsdlContent": wsdlXML,
		"url":         "http://localhost:8081/mock-soap",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created apiclient.Collection
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if got := created.Items[0].Request.URL; got != "http://localhost:8081/mock-soap" {
		t.Fatalf("expected the explicit URL to override the WSDL's own endpoint, got %q", got)
	}
}

// TestImportWSDLCollectionScaffoldsOnePlaceholderPerInputPart guards the
// "complete request body" upgrade: when the WSDL declares an <input
// message="..."/> that resolves to a <message><part> list, the generated
// request body must carry one placeholder element per part (with its
// type/element hint in a comment) instead of the single opaque "fill this
// in" comment used when a WSDL declares no parts at all.
func TestImportWSDLCollectionScaffoldsOnePlaceholderPerInputPart(t *testing.T) {
	r := newTestAPIClientRouter(t)
	wsdlXML := `<?xml version="1.0"?>
<definitions name="OrderService" xmlns:tns="urn:orders">
  <message name="GetOrderRequest">
    <part name="orderId" type="tns:string"/>
    <part name="includeItems" element="tns:IncludeItemsFlag"/>
  </message>
  <portType name="OrderPortType">
    <operation name="GetOrder">
      <input message="tns:GetOrderRequest"/>
    </operation>
  </portType>
</definitions>`

	rec := doJSON(t, r, "POST", "/api/apiclient/collections/import-wsdl", map[string]any{"wsdlContent": wsdlXML})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created apiclient.Collection
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	body := created.Items[0].Request.Body
	for _, want := range []string{"<orderId>?</orderId>", "string", "<includeItems>?</includeItems>", "IncludeItemsFlag"} {
		if !strings.Contains(body, want) {
			t.Errorf("expected body to contain %q, got %q", want, body)
		}
	}
	if strings.Contains(body, "fill in request parameters here") {
		t.Errorf("expected the per-part placeholders to replace the generic comment, got %q", body)
	}
}

// TestImportWSDLCollectionPlacesHeaderPartsInSoapHeader guards a real
// pattern seen in an actual enterprise WSDL: an operation's input message
// has both a body part and a part the binding explicitly declares as a
// SOAP header (<soap:header message="..." part="..."/>). The generated
// request must place the header part inside <soapenv:Header>, not dumped
// into <soapenv:Body> alongside the actual payload — a real endpoint
// expecting it as a header wouldn't find it in the body.
func TestImportWSDLCollectionPlacesHeaderPartsInSoapHeader(t *testing.T) {
	r := newTestAPIClientRouter(t)
	wsdlXML := `<?xml version="1.0"?>
<definitions name="SvcService" xmlns:tns="urn:svc">
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

	rec := doJSON(t, r, "POST", "/api/apiclient/collections/import-wsdl", map[string]any{"wsdlContent": wsdlXML})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created apiclient.Collection
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	body := created.Items[0].Request.Body

	headerSection := body[strings.Index(body, "<soapenv:Header>"):strings.Index(body, "</soapenv:Header>")]
	bodySection := body[strings.Index(body, "<soapenv:Body>"):]

	if !strings.Contains(headerSection, "<authHeader>?</authHeader>") {
		t.Errorf("expected authHeader placeholder inside <soapenv:Header>, got header section %q", headerSection)
	}
	if strings.Contains(bodySection, "authHeader") {
		t.Errorf("expected authHeader NOT to also appear in <soapenv:Body>, got body section %q", bodySection)
	}
	if !strings.Contains(bodySection, "<parameters>?</parameters>") {
		t.Errorf("expected parameters placeholder inside <soapenv:Body>, got body section %q", bodySection)
	}
}

// TestImportWSDLCollectionExplicitNameOverridesDetectedName confirms a
// user-supplied name wins over whatever the WSDL itself would otherwise
// resolve to (definitions name or service name) — for the rarer WSDL with
// no name anywhere, or when the user just wants something else.
func TestImportWSDLCollectionExplicitNameOverridesDetectedName(t *testing.T) {
	r := newTestAPIClientRouter(t)
	wsdlXML := `<?xml version="1.0"?>
<definitions name="DetectedName">
  <portType name="PT"><operation name="Ping"/></portType>
</definitions>`

	rec := doJSON(t, r, "POST", "/api/apiclient/collections/import-wsdl", map[string]any{
		"wsdlContent": wsdlXML,
		"name":        "My Custom Name",
	})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", rec.Code, rec.Body.String())
	}
	var created apiclient.Collection
	if err := json.Unmarshal(rec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode created: %v", err)
	}
	if created.Name != "My Custom Name" {
		t.Fatalf("expected the explicit name to override the detected one, got %q", created.Name)
	}
}

// TestImportWSDLCollectionRejectsEmptyWSDL guards the same "nothing to
// import" case importWSDL (mocks) already rejects.
func TestImportWSDLCollectionRejectsEmptyWSDL(t *testing.T) {
	r := newTestAPIClientRouter(t)
	rec := doJSON(t, r, "POST", "/api/apiclient/collections/import-wsdl", map[string]any{
		"wsdlContent": `<?xml version="1.0"?><definitions></definitions>`,
	})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a WSDL with no operations, got %d: %s", rec.Code, rec.Body.String())
	}
}

// --- environments ---

func TestCreateEnvironmentRejectsDuplicateNameButAllowsInDifferentWorkspace(t *testing.T) {
	r := newTestAPIClientRouter(t)

	wsRec := doJSON(t, r, "POST", "/api/apiclient/workspaces", map[string]any{"name": "Other"})
	var ws apiclient.Workspace
	if err := json.Unmarshal(wsRec.Body.Bytes(), &ws); err != nil {
		t.Fatalf("decode workspace: %v", err)
	}

	first := doJSON(t, r, "POST", "/api/apiclient/environments", map[string]any{"name": "Staging", "variables": map[string]string{"x": "1"}})
	if first.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", first.Code, first.Body.String())
	}

	dup := doJSON(t, r, "POST", "/api/apiclient/environments", map[string]any{"name": "Staging"})
	if dup.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a duplicate environment name, got %d: %s", dup.Code, dup.Body.String())
	}

	otherWorkspace := doJSON(t, r, "POST", "/api/apiclient/environments", map[string]any{"name": "Staging", "workspaceId": ws.ID})
	if otherWorkspace.Code != http.StatusCreated {
		t.Fatalf("expected the same name to be allowed in a different workspace, got %d: %s", otherWorkspace.Code, otherWorkspace.Body.String())
	}
}

func TestUpdateEnvironmentSuccessNotFoundAndDuplicateNameOnRename(t *testing.T) {
	r := newTestAPIClientRouter(t)
	e1 := doJSON(t, r, "POST", "/api/apiclient/environments", map[string]any{"name": "Dev"})
	var env1 apiclient.Environment
	if err := json.Unmarshal(e1.Body.Bytes(), &env1); err != nil {
		t.Fatalf("decode env1: %v", err)
	}

	e2 := doJSON(t, r, "POST", "/api/apiclient/environments", map[string]any{"name": "Prod"})
	if e2.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", e2.Code, e2.Body.String())
	}

	upd := doJSON(t, r, "PUT", "/api/apiclient/environments/"+env1.ID, map[string]any{
		"name":      "Dev2",
		"variables": map[string]string{"baseUrl": "https://dev.example.com"},
	})
	if upd.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", upd.Code, upd.Body.String())
	}
	var updated apiclient.Environment
	if err := json.Unmarshal(upd.Body.Bytes(), &updated); err != nil {
		t.Fatalf("decode updated: %v", err)
	}
	if updated.Name != "Dev2" || updated.Variables["baseUrl"] != "https://dev.example.com" {
		t.Fatalf("expected the updated environment, got %+v", updated)
	}

	nf := doJSON(t, r, "PUT", "/api/apiclient/environments/does-not-exist", map[string]any{"name": "X"})
	if nf.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", nf.Code, nf.Body.String())
	}

	dup := doJSON(t, r, "PUT", "/api/apiclient/environments/"+env1.ID, map[string]any{"name": "Prod"})
	if dup.Code != http.StatusConflict {
		t.Fatalf("expected 409 renaming into a name already used by the Prod environment, got %d: %s", dup.Code, dup.Body.String())
	}
}

func TestDeleteEnvironmentSuccessAndNotFound(t *testing.T) {
	r := newTestAPIClientRouter(t)
	created := doJSON(t, r, "POST", "/api/apiclient/environments", map[string]any{"name": "ToDelete"})
	var env apiclient.Environment
	if err := json.Unmarshal(created.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode created: %v", err)
	}

	del := doJSON(t, r, "DELETE", "/api/apiclient/environments/"+env.ID, nil)
	if del.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", del.Code, del.Body.String())
	}

	listRec := doJSON(t, r, "GET", "/api/apiclient/environments", nil)
	var list []apiclient.Environment
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 0 {
		t.Fatalf("expected the environment to be gone, got %+v", list)
	}

	nf := doJSON(t, r, "DELETE", "/api/apiclient/environments/does-not-exist", nil)
	if nf.Code != http.StatusNotFound {
		t.Fatalf("expected 404 deleting a nonexistent environment, got %d: %s", nf.Code, nf.Body.String())
	}
}

// --- execute / loadtest / curl / ws-exchange ---

// TestExecuteRunsAgainstRealServerAndReturnsResponse mirrors what
// internal/apiclient/runner_test.go already does at the package level
// (e.g. TestExecuteAgainstRealServerSubstitutesVarsAndCapturesResponse), but
// now driven through the HTTP handler to confirm the /execute route
// actually decodes the request and returns Execute's real result.
func TestExecuteRunsAgainstRealServerAndReturnsResponse(t *testing.T) {
	r := newTestAPIClientRouter(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("X-Api-Key") != "secret123" {
			t.Errorf("expected the substituted header value, got %q", req.Header.Get("X-Api-Key"))
		}
		w.WriteHeader(201)
		w.Write([]byte(`{"ok":true}`))
	}))
	defer srv.Close()

	rec := doJSON(t, r, "POST", "/api/apiclient/execute", map[string]any{
		"spec": map[string]any{
			"method":  "GET",
			"url":     srv.URL,
			"headers": []map[string]any{{"key": "X-Api-Key", "value": "{{apiKey}}"}},
		},
		"variables": map[string]string{"apiKey": "secret123"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result apiclient.ExecutionResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.StatusCode != 201 || result.Body != `{"ok":true}` {
		t.Fatalf("expected the real server's response to come back through the handler, got %+v", result)
	}
}

// TestExecuteCancellingTheRequestContextAbortsTheOutboundCall guards
// against a real gap: the /execute handler used to call apiclient.Execute
// (a plain context.Background() call), so aborting the browser's fetch —
// the API client's "Force stop" button — had no way to actually stop the
// real outbound network call the server was making; it would run to
// completion (or its full 30s timeout) regardless. Now the handler passes
// r.Context() through to ExecuteContext, so cancelling the INCOMING
// request's context (what a real aborted fetch does to it, since net/http
// cancels a handler's r.Context() when the client disconnects) must make
// the handler return promptly instead of waiting for the slow target.
func TestExecuteCancellingTheRequestContextAbortsTheOutboundCall(t *testing.T) {
	r := newTestAPIClientRouter(t)
	unblock := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		<-unblock
	}))
	defer srv.Close()
	defer close(unblock) // LIFO: closes before srv.Close() waits for the handler to return

	body, _ := json.Marshal(map[string]any{"spec": map[string]any{"method": "GET", "url": srv.URL}})
	ctx, cancel := context.WithCancel(context.Background())
	req := httptest.NewRequest("POST", "/api/apiclient/execute", bytes.NewReader(body)).WithContext(ctx)
	rec := httptest.NewRecorder()

	done := make(chan struct{})
	go func() {
		r.ServeHTTP(rec, req)
		close(done)
	}()

	time.Sleep(100 * time.Millisecond)
	cancel()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("expected the handler to return promptly once the request context was cancelled")
	}
}

// TestLoadTestAggregatesResults drives /loadtest against a real httptest
// server with a small concurrency/totalRequests so the run finishes
// instantly, and checks the aggregate LoadTestResult shape decodes as
// expected — mirroring apiclient.RunLoadTest's own package-level tests, now
// through the HTTP handler.
func TestLoadTestAggregatesResults(t *testing.T) {
	r := newTestAPIClientRouter(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()

	rec := doJSON(t, r, "POST", "/api/apiclient/loadtest", map[string]any{
		"spec":          map[string]any{"method": "GET", "url": srv.URL},
		"concurrency":   2,
		"totalRequests": 5,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result apiclient.LoadTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.TotalRequests != 5 {
		t.Fatalf("expected 5 total requests, got %+v", result)
	}
	if result.Statuses.Count2xx != 5 || result.Statuses.CountError != 0 {
		t.Fatalf("expected all 5 requests to succeed as 2xx, got %+v", result.Statuses)
	}
}

// TestLoadTestPersistsRunHistory guards against a real gap: /loadtest used
// to only return the final summary, with no persisted record of the run at
// all — this checks that a run against a saved item actually shows up in
// that item's history list afterward.
func TestLoadTestPersistsRunHistory(t *testing.T) {
	r := newTestAPIClientRouter(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := doJSON(t, r, "POST", "/api/apiclient/loadtest", map[string]any{
		"spec":          map[string]any{"method": "GET", "url": srv.URL},
		"concurrency":   2,
		"totalRequests": 5,
		"itemId":        "item-1",
		"collectionId":  "coll-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, r, "GET", "/api/apiclient/loadtest-runs?itemId=item-1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 listing history, got %d: %s", rec.Code, rec.Body.String())
	}
	var list []apiclient.LoadTestRunSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 || list[0].TotalRequests != 5 {
		t.Fatalf("expected the run to be persisted under item-1, got %+v", list)
	}
}

// TestLoadTestRunHistoryRoutesRoundTrip exercises the three new history
// routes end to end: list -> get -> delete -> 404 afterward.
func TestLoadTestRunHistoryRoutesRoundTrip(t *testing.T) {
	r := newTestAPIClientRouter(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := doJSON(t, r, "POST", "/api/apiclient/loadtest", map[string]any{
		"spec":          map[string]any{"method": "GET", "url": srv.URL},
		"concurrency":   1,
		"totalRequests": 1,
		"itemId":        "item-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 running the load test, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, r, "GET", "/api/apiclient/loadtest-runs?itemId=item-1", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 listing history, got %d: %s", rec.Code, rec.Body.String())
	}
	var list []apiclient.LoadTestRunSummary
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 run in history, got %d", len(list))
	}
	runID := list[0].ID

	rec = doJSON(t, r, "GET", "/api/apiclient/loadtest-runs/"+runID, nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 getting one run, got %d: %s", rec.Code, rec.Body.String())
	}
	var full apiclient.LoadTestRun
	if err := json.Unmarshal(rec.Body.Bytes(), &full); err != nil {
		t.Fatalf("decode full run: %v", err)
	}
	if full.Result == nil || full.Result.TotalRequests != 1 {
		t.Fatalf("expected the full run's result, got %+v", full)
	}

	rec = doJSON(t, r, "DELETE", "/api/apiclient/loadtest-runs/"+runID, nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting the run, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, r, "GET", "/api/apiclient/loadtest-runs/"+runID, nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 after delete, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestExportLoadTestRunAsJSONAndCSV covers the download-link feature: the
// response from running a test already carries runId (set by
// apiclient.Store.SaveLoadTestRun on the shared result), and both export
// formats work off the persisted run looked up by that id.
func TestExportLoadTestRunAsJSONAndCSV(t *testing.T) {
	r := newTestAPIClientRouter(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	rec := doJSON(t, r, "POST", "/api/apiclient/loadtest", map[string]any{
		"spec":          map[string]any{"method": "GET", "url": srv.URL},
		"concurrency":   1,
		"totalRequests": 3,
		"itemId":        "item-1",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 running the load test, got %d: %s", rec.Code, rec.Body.String())
	}
	var liveResult apiclient.LoadTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &liveResult); err != nil {
		t.Fatalf("decode live result: %v", err)
	}
	if liveResult.RunID == "" {
		t.Fatal("expected the live response to already carry a non-empty runId")
	}

	rec = doJSON(t, r, "GET", "/api/apiclient/loadtest-runs/"+liveResult.RunID+"/export?format=json", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 exporting as JSON, got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "application/json" {
		t.Fatalf("expected JSON content type, got %q", ct)
	}
	if cd := rec.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment") || !strings.HasSuffix(cd, `.json"`) {
		t.Fatalf("expected an attachment .json filename, got %q", cd)
	}
	var exported apiclient.LoadTestRun
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatalf("decode exported JSON: %v", err)
	}
	if exported.ID != liveResult.RunID || len(exported.Result.Samples) != 3 {
		t.Fatalf("expected the full run with its 3 samples, got %+v", exported)
	}

	rec = doJSON(t, r, "GET", "/api/apiclient/loadtest-runs/"+liveResult.RunID+"/export", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 exporting as CSV (default), got %d: %s", rec.Code, rec.Body.String())
	}
	if ct := rec.Header().Get("Content-Type"); ct != "text/csv" {
		t.Fatalf("expected CSV content type, got %q", ct)
	}
	body := rec.Body.String()
	if !strings.Contains(body, liveResult.RunID) {
		t.Fatalf("expected the CSV summary row to contain the run id, got:\n%s", body)
	}
	if strings.Count(body, "\n") < 6 { // header + summary + blank + header + 3 sample rows
		t.Fatalf("expected a summary block plus 3 sample rows, got:\n%s", body)
	}

	rec = doJSON(t, r, "GET", "/api/apiclient/loadtest-runs/does-not-exist/export", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 exporting an unknown run, got %d", rec.Code)
	}
}

// TestExportLoadTestResultIncludesFullResponsesWhenDetailed covers the
// Detailed-run download path: response bodies/headers only ever exist in
// memory (never persisted), so this posts a result carrying them back to
// the server and expects them to actually appear in both export formats —
// proof that exportLoadTestRun (which only ever reads from the DB) could
// never have served this, and this separate endpoint is what does.
func TestExportLoadTestResultIncludesFullResponsesWhenDetailed(t *testing.T) {
	r := newTestAPIClientRouter(t)
	result := apiclient.LoadTestResult{
		TotalRequests:  1,
		RequestsPerSec: 12.5,
		Statuses:       apiclient.StatusCounts{Count2xx: 1},
		Detailed:       true,
		RunID:          "run-1",
		Samples: []apiclient.LoadTestSample{
			{
				Index: 0, ElapsedMs: 5, LatencyMs: 5, StatusCode: 200,
				ResponseBody:    `{"hello":"world"}`,
				ResponseHeaders: map[string][]string{"Content-Type": {"application/json"}},
			},
		},
	}
	body, err := json.Marshal(map[string]any{"result": &result})
	if err != nil {
		t.Fatalf("marshal request: %v", err)
	}

	rec := doJSON(t, r, "POST", "/api/apiclient/loadtest-export?format=json", json.RawMessage(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 exporting detailed result as JSON, got %d: %s", rec.Code, rec.Body.String())
	}
	var exported apiclient.LoadTestResult
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatalf("decode exported JSON: %v", err)
	}
	if len(exported.Samples) != 1 || exported.Samples[0].ResponseBody != `{"hello":"world"}` {
		t.Fatalf("expected the response body to round-trip, got %+v", exported.Samples)
	}

	rec = doJSON(t, r, "POST", "/api/apiclient/loadtest-export", json.RawMessage(body))
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 exporting detailed result as CSV (default), got %d: %s", rec.Code, rec.Body.String())
	}
	csvBody := rec.Body.String()
	if !strings.Contains(csvBody, "12.5") {
		t.Fatalf("expected the TPS (requestsPerSec) in the CSV summary row, got:\n%s", csvBody)
	}
	if !strings.Contains(csvBody, `"""hello"":""world"""`) && !strings.Contains(csvBody, `{""hello"":""world""}`) {
		t.Fatalf("expected the response body to appear in the CSV sample row, got:\n%s", csvBody)
	}
	if !strings.Contains(csvBody, "application/json") {
		t.Fatalf("expected the response headers to appear in the CSV sample row, got:\n%s", csvBody)
	}

	rec = doJSON(t, r, "POST", "/api/apiclient/loadtest-export", map[string]any{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 with no result in the body, got %d", rec.Code)
	}
}

// TestCurlImportExportRoundTrip checks a curl command parses into a
// RequestSpec via /curl-import, and that spec renders back into an
// equivalent curl command via /curl-export.
func TestCurlImportExportRoundTrip(t *testing.T) {
	r := newTestAPIClientRouter(t)

	importRec := doJSON(t, r, "POST", "/api/apiclient/curl-import", map[string]any{
		"curl": `curl -X POST -H "Content-Type: application/json" --data-raw '{"name":"widget"}' https://example.com/items`,
	})
	if importRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", importRec.Code, importRec.Body.String())
	}
	var spec apiclient.RequestSpec
	if err := json.Unmarshal(importRec.Body.Bytes(), &spec); err != nil {
		t.Fatalf("decode imported spec: %v", err)
	}
	if spec.Method != "POST" || spec.URL != "https://example.com/items" || spec.Body != `{"name":"widget"}` {
		t.Fatalf("expected the curl command parsed into a RequestSpec, got %+v", spec)
	}

	exportRec := doJSON(t, r, "POST", "/api/apiclient/curl-export", spec)
	if exportRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", exportRec.Code, exportRec.Body.String())
	}
	var exported map[string]string
	if err := json.Unmarshal(exportRec.Body.Bytes(), &exported); err != nil {
		t.Fatalf("decode exported curl: %v", err)
	}
	if !strings.Contains(exported["curl"], "https://example.com/items") || !strings.Contains(exported["curl"], "widget") {
		t.Fatalf("expected the exported curl command to reproduce the request, got %q", exported["curl"])
	}

	reimported := doJSON(t, r, "POST", "/api/apiclient/curl-import", map[string]any{"curl": exported["curl"]})
	var reimportedSpec apiclient.RequestSpec
	if err := json.Unmarshal(reimported.Body.Bytes(), &reimportedSpec); err != nil {
		t.Fatalf("decode reimported spec: %v", err)
	}
	if reimportedSpec.Method != "POST" || reimportedSpec.URL != "https://example.com/items" {
		t.Fatalf("expected the exported curl to re-import to an equivalent spec, got %+v", reimportedSpec)
	}
}

// TestWSExchangeRouteIsWired mirrors TestWSLoadTestRouteIsWired's own
// reasoning: an unreachable ws:// address is enough to confirm the route
// reaches apiclient.ExchangeWS and that its result shape decodes as
// expected, without needing a live WS server in this handler-level test.
func TestWSExchangeRouteIsWired(t *testing.T) {
	r := newTestAPIClientRouter(t)
	rec := doJSON(t, r, "POST", "/api/apiclient/ws-exchange", map[string]any{
		"spec": map[string]any{"url": "ws://127.0.0.1:1"},
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var result apiclient.WSExchangeResult
	if err := json.Unmarshal(rec.Body.Bytes(), &result); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if result.Connected || result.Error == "" {
		t.Fatalf("expected a dial-level failure against an unreachable address, got %+v", result)
	}
}
