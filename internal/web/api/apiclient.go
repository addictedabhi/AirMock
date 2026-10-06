package api

import (
	"archive/zip"
	"encoding/csv"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/apiclient"
	"github.com/addictedabhi/airmock/internal/apiclient/postman"
	"github.com/addictedabhi/airmock/internal/apiclient/soapui"
	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/wsdl"
	"github.com/addictedabhi/airmock/internal/wslock"
)

// HitLogger is the narrow slice of internal/hitlog.Store the API client
// needs — recording one entry per "Send"/Collection Runner call, the same
// way every mock engine records its own inbound hits, so outbound calls
// finally show up in Log History/Dashboard instead of only existing in the
// browser's own response viewer.
type HitLogger interface {
	Record(e *hitlog.Entry) error
}

type APIClientHandler struct {
	store     *apiclient.Store
	certStore *certs.Store
	hitLogger HitLogger
	tracker   *wslock.Tracker
}

func NewAPIClientHandler(store *apiclient.Store, certStore *certs.Store, hitLogger HitLogger, tracker *wslock.Tracker) *APIClientHandler {
	return &APIClientHandler{store: store, certStore: certStore, hitLogger: hitLogger, tracker: tracker}
}

// resolveClientCert looks up spec.ClientCertID (if set) in the certificate
// store and populates ClientCertPEM/ClientKeyPEM — the only way that
// material reaches ExecuteContext, since apiclient itself never touches the
// cert store directly. A cert with no private key (a pure trust-anchor
// import) can't be presented as a client certificate, so that's rejected
// here rather than failing opaquely inside the TLS handshake.
func (h *APIClientHandler) resolveClientCert(spec *apiclient.RequestSpec) error {
	if spec.ClientCertID == "" {
		return nil
	}
	c, err := h.certStore.Get(spec.ClientCertID)
	if err != nil {
		return fmt.Errorf("client certificate: %w", err)
	}
	if !c.HasKey {
		return fmt.Errorf("client certificate %q has no private key and can't be presented for mTLS", c.Name)
	}
	spec.ClientCertPEM = c.CertPEM
	spec.ClientKeyPEM = c.KeyPEM
	return nil
}

func (h *APIClientHandler) Routes(r chi.Router) {
	r.Route("/workspaces", func(r chi.Router) {
		r.Get("/", h.listWorkspaces)
		r.Post("/", h.createWorkspace)
		r.Delete("/{id}", h.deleteWorkspace)
		r.Post("/{id}/lock", h.lockWorkspace)
		r.Post("/{id}/unlock", h.unlockWorkspace)
		r.Post("/{id}/remove-lock", h.removeWorkspaceLock)
	})

	r.Route("/collections", func(r chi.Router) {
		r.Get("/", h.listCollections)
		r.Post("/", h.createCollection)
		r.Get("/export-bulk", h.exportCollectionsBulk)
		r.Get("/{id}", h.getCollection)
		r.Put("/{id}", h.updateCollection)
		r.Delete("/{id}", h.deleteCollection)
		r.Get("/{id}/export", h.exportCollection)
		r.Post("/{id}/move", h.moveCollection)
	})
	r.Post("/collections/import", h.importCollection)
	r.Post("/collections/import-bulk", h.importCollectionsBulk)
	r.Post("/collections/import-soapui", h.importSoapUICollection)
	r.Post("/collections/import-wsdl", h.importWSDLCollection)

	r.Route("/environments", func(r chi.Router) {
		r.Get("/", h.listEnvironments)
		r.Post("/", h.createEnvironment)
		r.Put("/{id}", h.updateEnvironment)
		r.Delete("/{id}", h.deleteEnvironment)
	})
	r.Post("/environments/import", h.importEnvironment)

	r.Post("/execute", h.execute)
	r.Post("/loadtest", h.loadTest)
	r.Post("/loadtest-export", h.exportLoadTestResult)
	r.Route("/loadtest-runs", func(r chi.Router) {
		r.Get("/", h.listLoadTestRuns)
		r.Get("/{id}", h.getLoadTestRun)
		r.Get("/{id}/export", h.exportLoadTestRun)
		r.Delete("/{id}", h.deleteLoadTestRun)
	})
	r.Post("/ws-loadtest", h.wsLoadTest)
	r.Post("/curl-import", h.curlImport)
	r.Post("/curl-export", h.curlExport)
	r.Post("/snippet", h.codeSnippet)
	r.Post("/ws-exchange", h.wsExchange)
}

// --- workspaces ---

// workspaceListEntry adds Unlocked (whether THIS browser has already
// unlocked this workspace this session, per h.tracker) on top of
// apiclient.Workspace's own JSON fields — client-side state the store
// layer has no way to know about (it isn't cookie-aware), needed so the
// Collections page can tell "already unlocked, safe to open" from "locked
// and needs its password" without a separate round trip per workspace.
type workspaceListEntry struct {
	*apiclient.Workspace
	Unlocked bool `json:"unlocked"`
}

func (h *APIClientHandler) listWorkspaces(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.ListWorkspaces()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	clientID := clientIDIfPresent(r)
	out := make([]workspaceListEntry, len(list))
	for i, ws := range list {
		out[i] = workspaceListEntry{Workspace: ws, Unlocked: !ws.Locked || h.tracker.IsUnlocked(clientID, ws.ID)}
	}
	writeJSON(w, http.StatusOK, out)
}

func (h *APIClientHandler) createWorkspace(w http.ResponseWriter, r *http.Request) {
	var ws apiclient.Workspace
	if err := json.NewDecoder(r.Body).Decode(&ws); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if ws.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	created, err := h.store.CreateWorkspace(&ws)
	if errors.Is(err, apiclient.ErrDuplicateWorkspaceName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *APIClientHandler) deleteWorkspace(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if !h.requireCurrentWorkspacePasswordForDelete(w, r, id) {
		return
	}
	err := h.store.DeleteWorkspace(id)
	if errors.Is(err, apiclient.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, apiclient.ErrLastWorkspace) || errors.Is(err, apiclient.ErrDefaultWorkspace) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- collections ---

func (h *APIClientHandler) listCollections(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspaceId")
	if !checkWorkspaceUnlocked(w, r, h.store, h.tracker, workspaceID) {
		return
	}
	list, err := h.store.ListCollections(workspaceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *APIClientHandler) createCollection(w http.ResponseWriter, r *http.Request) {
	var c apiclient.Collection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !checkWorkspaceUnlocked(w, r, h.store, h.tracker, c.WorkspaceID) {
		return
	}
	created, err := h.store.CreateCollection(&c)
	if errors.Is(err, apiclient.ErrDuplicateCollectionName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *APIClientHandler) getCollection(w http.ResponseWriter, r *http.Request) {
	c, err := h.store.GetCollection(chi.URLParam(r, "id"))
	if errors.Is(err, apiclient.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, c)
}

func (h *APIClientHandler) updateCollection(w http.ResponseWriter, r *http.Request) {
	var c apiclient.Collection
	if err := json.NewDecoder(r.Body).Decode(&c); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	c.ID = chi.URLParam(r, "id")
	// workspace_id isn't mutable via update (see Store.UpdateCollection) —
	// only the collection's CURRENT, actual workspace is ever at risk here,
	// not whatever WorkspaceID this request's body happens to carry.
	if existing, err := h.store.GetCollection(c.ID); err == nil {
		if !checkWorkspaceUnlocked(w, r, h.store, h.tracker, existing.WorkspaceID) {
			return
		}
	}
	updated, err := h.store.UpdateCollection(&c)
	if errors.Is(err, apiclient.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, apiclient.ErrDuplicateCollectionName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *APIClientHandler) deleteCollection(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if existing, err := h.store.GetCollection(id); err == nil {
		if !checkWorkspaceUnlocked(w, r, h.store, h.tracker, existing.WorkspaceID) {
			return
		}
	}
	if err := h.store.DeleteCollection(id); err != nil {
		if errors.Is(err, apiclient.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *APIClientHandler) moveCollection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID string `json:"workspaceId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	id := chi.URLParam(r, "id")
	// Both ends of a move are at risk: leaving a locked workspace is still
	// a modification of what that lock protects, and landing in a locked
	// one requires its password just as much as creating directly into it
	// would.
	if existing, err := h.store.GetCollection(id); err == nil {
		if !checkWorkspaceUnlocked(w, r, h.store, h.tracker, existing.WorkspaceID, body.WorkspaceID) {
			return
		}
	}
	moved, err := h.store.MoveCollection(id, body.WorkspaceID)
	if errors.Is(err, apiclient.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, apiclient.ErrDuplicateCollectionName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, moved)
}

func (h *APIClientHandler) exportCollection(w http.ResponseWriter, r *http.Request) {
	c, err := h.store.GetCollection(chi.URLParam(r, "id"))
	if errors.Is(err, apiclient.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	data, err := postman.Export(*c)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Disposition", `attachment; filename="`+c.Name+`.postman_collection.json"`)
	w.Write(data)
}

func (h *APIClientHandler) importCollection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PostmanJSON string `json:"postmanJson"`
		WorkspaceID string `json:"workspaceId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	c, err := postman.Import([]byte(body.PostmanJSON))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	c.WorkspaceID = body.WorkspaceID
	created, err := h.createCollectionDeduped(c)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// importSoapUICollection parses a SoapUI project export (*.xml) into a
// collection — one folder per TestSuite, one nested folder per TestCase,
// one request item per request-shaped TestStep (see soapui.ImportCollection)
// — the XML counterpart of importCollection's Postman JSON import.
func (h *APIClientHandler) importSoapUICollection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		ProjectXML  string `json:"projectXml"`
		WorkspaceID string `json:"workspaceId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	c, err := soapui.ImportCollection([]byte(body.ProjectXML))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	c.WorkspaceID = body.WorkspaceID
	created, err := h.createCollectionDeduped(c)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// importWSDLCollection parses a plain WSDL document (no SoapUI project
// wrapper) into a flat collection of request items, one per operation — the
// WSDL counterpart of importSoapUICollection, for a service that only ever
// published its contract, never a SoapUI project. Unlike a SoapUI import,
// a WSDL alone carries neither a target endpoint (unless its own <service>
// declares one, used as a fallback) nor any sample request content, so
// every item gets a generic placeholder envelope for the user to fill in —
// the same limitation importWSDL already accepts for the mock-scaffolding
// side of a WSDL import.
func (h *APIClientHandler) importWSDLCollection(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WSDLContent string `json:"wsdlContent"`
		URL         string `json:"url"`
		// Name, when set, overrides the collection name the WSDL itself
		// would otherwise pick (see wsdl.ParseDocument's own name/service
		// fallback) — for the rarer WSDL with no name anywhere, or when the
		// user just wants a different name than whatever the document
		// declares.
		Name string `json:"name"`
		// ExtraSchemas holds the raw content of standalone *.xsd documents
		// this WSDL references via <xsd:include>/<xsd:import> rather than
		// declaring inline — this reader doesn't follow those references on
		// disk itself, so a request body scaffolded from a part whose real
		// structure lives in one of those files falls back to a flat
		// placeholder unless the caller supplies the file's own content
		// here (see wsdl.BuildSchemaSet).
		ExtraSchemas []string `json:"extraSchemas,omitempty"`
		WorkspaceID  string   `json:"workspaceId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	doc, err := wsdl.ParseDocument([]byte(body.WSDLContent))
	if err != nil {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("parse WSDL: %w", err))
		return
	}
	if len(doc.Operations) == 0 {
		writeErr(w, http.StatusBadRequest, fmt.Errorf("no operations found in the WSDL's portType"))
		return
	}

	endpoint := body.URL
	if endpoint == "" {
		endpoint = doc.EndpointURL
	}
	name := body.Name
	if name == "" {
		name = doc.Name
	}
	if name == "" {
		name = "WSDL Import"
	}

	extraSchemas := make([][]byte, len(body.ExtraSchemas))
	for i, s := range body.ExtraSchemas {
		extraSchemas[i] = []byte(s)
	}
	// The same bytes already parsed successfully as a WSDL document above,
	// so BuildSchemaSet failing here isn't realistically reachable — still
	// degrading to an empty set (flat placeholders throughout, exactly
	// today's behavior) rather than failing the whole import if it ever did.
	schemas, err := wsdl.BuildSchemaSet([]byte(body.WSDLContent), extraSchemas)
	if err != nil {
		schemas = &wsdl.SchemaSet{}
	}

	c := &apiclient.Collection{Name: name, Items: wsdlOperationsToItems(doc.Operations, endpoint, schemas)}
	c.WorkspaceID = body.WorkspaceID
	created, err := h.createCollectionDeduped(c)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func wsdlOperationsToItems(ops []wsdl.Operation, endpoint string, schemas *wsdl.SchemaSet) []apiclient.Item {
	items := make([]apiclient.Item, 0, len(ops))
	for _, op := range ops {
		name := op.Name
		if op.BindingName != "" {
			name = op.Name + " (" + op.BindingName + ")"
		}
		// Same reasoning as soapui.callsToItems: Content-Type and SOAPAction
		// are both required for the request to actually dispatch against a
		// real SOAP endpoint or an AirMock SOAP mock, so both are set here
		// rather than left for the user to discover and add by hand.
		headers := []apiclient.KV{{Key: contentTypeHeader, Value: "text/xml; charset=utf-8"}}
		if op.SOAPAction != "" {
			headers = append(headers, apiclient.KV{Key: "SOAPAction", Value: op.SOAPAction})
		}
		items = append(items, apiclient.Item{
			Type: apiclient.ItemRequest,
			Name: name,
			Request: &apiclient.RequestSpec{
				Method:         http.MethodPost,
				URL:            endpoint,
				Headers:        headers,
				Body:           stubSOAPRequestEnvelope(op.Name, op.InputParts, schemas),
				RawContentType: "xml",
			},
		})
	}
	return items
}

// stubSOAPRequestEnvelope builds a request body for an operation a WSDL
// alone provides no real sample input for. Each part (see wsdl.Part) is
// rendered by renderPart: a full recursive expansion — one placeholder
// element per expected field, each commented with an "Optional:" or "N to
// M repetitions:" hint exactly matching SoapUI's own generated blank-
// sample convention — when its Element reference resolves against schemas
// (built from the WSDL's own <wsdl:types> plus any externally-supplied
// schema files, see wsdl.BuildSchemaSet), or a single flat <name>?</name>
// placeholder when it doesn't (no schema at all, an rpc/encoded-style
// Type-only part, or an unresolvable Element reference). A header part
// (wsdl.Part.Header) is placed in <soapenv:Header> instead of alongside
// the actual body parts — a real enterprise WSDL commonly splits an auth/
// routing part out as a header this way, and dumping it into the body
// instead would send it somewhere the endpoint isn't looking for it. Body
// parts are rendered directly as <soapenv:Body>'s own children (matching
// how a real document/literal SOAP body's children ARE its message parts,
// no extra wrapper) UNLESS there are none at all, in which case a single
// generic placeholder wrapped in the operation's own name stands in for
// "nothing is known about this operation's input whatsoever".
func stubSOAPRequestEnvelope(operationName string, parts []wsdl.Part, schemas *wsdl.SchemaSet) string {
	var headerParts, bodyParts []wsdl.Part
	for _, p := range parts {
		if p.Header {
			headerParts = append(headerParts, p)
		} else {
			bodyParts = append(bodyParts, p)
		}
	}

	ns := newNamespaceRegistry()

	header := "<soapenv:Header/>"
	if len(headerParts) > 0 {
		header = fmt.Sprintf("<soapenv:Header>\n%s  </soapenv:Header>", renderParts(headerParts, schemas, "    ", ns))
	}

	innerBody := fmt.Sprintf("    <%s xmlns=\"urn:airmock:request\">\n      <!--Optional: fill in request parameters here-->\n    </%s>\n", operationName, operationName)
	if len(bodyParts) > 0 {
		innerBody = renderParts(bodyParts, schemas, "    ", ns)
	}

	return fmt.Sprintf(`<?xml version="1.0" encoding="UTF-8"?>
<soapenv:Envelope xmlns:soapenv="http://schemas.xmlsoap.org/soap/envelope/"%s>
  %s
  <soapenv:Body>
%s  </soapenv:Body>
</soapenv:Envelope>`, ns.declarations(), header, innerBody)
}

// renderParts renders each part in order (see renderPart), concatenated.
func renderParts(parts []wsdl.Part, schemas *wsdl.SchemaSet, indent string, ns *namespaceRegistry) string {
	var b strings.Builder
	for _, p := range parts {
		b.WriteString(renderPart(p, schemas, indent, ns))
	}
	return b.String()
}

// renderPart renders one message part: a full recursive expansion (real
// tag, namespace-prefixed via ns) when its Element resolves in schemas, or
// a single flat <!--name: hint--><name>?</name> placeholder otherwise.
func renderPart(p wsdl.Part, schemas *wsdl.SchemaSet, indent string, ns *namespaceRegistry) string {
	if p.Element != "" && schemas != nil {
		if tag, namespace, inner, ok := schemas.RenderElement(p.Element, indent+"  "); ok {
			qname := ns.qualify(namespace, tag)
			return fmt.Sprintf("%s<%s>\n%s%s</%s>\n", indent, qname, inner, indent, qname)
		}
	}
	hint := p.Type
	if hint == "" {
		hint = p.Element
	}
	if hint == "" {
		hint = "any"
	}
	return fmt.Sprintf("%s<!--%s: %s-->\n%s<%s>?</%s>\n", indent, p.Name, hint, indent, p.Name, p.Name)
}

// namespaceRegistry mints a stable "nsN" prefix per distinct namespace URI
// encountered while rendering one request body, in first-seen order, and
// renders the xmlns:nsN="..." declarations for all of them together —
// collected once per stub so a namespace shared by both a header and a
// body element (common: one schema backs both) is declared only once.
type namespaceRegistry struct {
	prefixes map[string]string
	order    []string
}

func newNamespaceRegistry() *namespaceRegistry {
	return &namespaceRegistry{prefixes: map[string]string{}}
}

// qualify returns tag as-is when namespace is empty, else "nsN:tag" with a
// prefix newly assigned (and remembered) for that namespace.
func (r *namespaceRegistry) qualify(namespace, tag string) string {
	if namespace == "" {
		return tag
	}
	prefix, seen := r.prefixes[namespace]
	if !seen {
		prefix = fmt.Sprintf("ns%d", len(r.order))
		r.prefixes[namespace] = prefix
		r.order = append(r.order, namespace)
	}
	return prefix + ":" + tag
}

func (r *namespaceRegistry) declarations() string {
	var b strings.Builder
	for _, uri := range r.order {
		fmt.Fprintf(&b, " xmlns:%s=%q", r.prefixes[uri], uri)
	}
	return b.String()
}

// createCollectionDeduped retries an import under "<name> (2)", "<name>
// (3)", etc. on a name collision — an import is a one-shot JSON paste with
// no chance for the user to pick a different name up front, so failing the
// whole import over a name clash would be needlessly disruptive.
func (h *APIClientHandler) createCollectionDeduped(c *apiclient.Collection) (*apiclient.Collection, error) {
	baseName := c.Name
	created, err := h.store.CreateCollection(c)
	for attempt := 2; errors.Is(err, apiclient.ErrDuplicateCollectionName) && attempt <= 100; attempt++ {
		c.Name = fmt.Sprintf("%s (%d)", baseName, attempt)
		created, err = h.store.CreateCollection(c)
	}
	return created, err
}

// exportCollectionsBulk bundles several collections' Postman exports into
// one .zip — exportCollection already returns one raw JSON file per
// collection, so a multi-select export just needs to gather several of
// those into a single archive instead of triggering N separate browser
// downloads (most browsers throttle/block automatic downloads past the
// first one or two anyway).
func (h *APIClientHandler) exportCollectionsBulk(w http.ResponseWriter, r *http.Request) {
	idsParam := r.URL.Query().Get("ids")
	if idsParam == "" {
		writeErr(w, http.StatusBadRequest, errors.New("ids query parameter is required"))
		return
	}

	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", `attachment; filename="collections-export.zip"`)
	zw := zip.NewWriter(w)
	defer zw.Close()

	usedNames := map[string]int{}
	for _, id := range strings.Split(idsParam, ",") {
		id = strings.TrimSpace(id)
		if id == "" {
			continue
		}
		c, err := h.store.GetCollection(id)
		if err != nil {
			continue // best-effort: a since-deleted id is skipped, not fatal to the rest of the batch
		}
		data, err := postman.Export(*c)
		if err != nil {
			continue
		}
		zf, err := zw.Create(zipEntryName(c.Name, usedNames))
		if err != nil {
			continue
		}
		zf.Write(data)
	}
}

// zipEntryName sanitizes a collection name into a safe zip entry filename
// and de-duplicates it against every other name already used in this same
// archive — two collections can share a name across different workspaces
// even though one workspace's own collection names are already unique, and
// a zip with two identically-named entries is legal but confusing to unpack.
func zipEntryName(name string, used map[string]int) string {
	safe := zipNameSanitizer.ReplaceAllString(name, "_")
	if safe == "" {
		safe = "collection"
	}
	used[safe]++
	if n := used[safe]; n > 1 {
		safe = fmt.Sprintf("%s (%d)", safe, n)
	}
	return safe + ".postman_collection.json"
}

var zipNameSanitizer = regexp.MustCompile(`[\\/:*?"<>|]`)

type importBulkFile struct {
	FileName string `json:"fileName"`
	Content  string `json:"content"`
}

type importBulkResult struct {
	Imported []importBulkImported `json:"imported"`
	Failed   []importBulkFailed   `json:"failed"`
}

type importBulkImported struct {
	FileName       string `json:"fileName"`
	CollectionID   string `json:"collectionId"`
	CollectionName string `json:"collectionName"`
}

type importBulkFailed struct {
	FileName string `json:"fileName"`
	Reason   string `json:"reason"`
}

// importCollectionsBulk imports every file in one request, one collection
// per file — the frontend reads an entire picked directory's files
// client-side (the same way ImportSource reads a single file today) and
// sends their raw text content here rather than uploading them, so this
// stays a plain JSON endpoint like importCollection. A single malformed or
// non-Postman file is skipped with a reason rather than aborting the whole
// batch — the same "keep going" philosophy MocksHandler.importAll already
// follows for bulk mock import.
func (h *APIClientHandler) importCollectionsBulk(w http.ResponseWriter, r *http.Request) {
	var body struct {
		WorkspaceID string           `json:"workspaceId"`
		Files       []importBulkFile `json:"files"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	result := importBulkResult{Imported: []importBulkImported{}, Failed: []importBulkFailed{}}
	for _, f := range body.Files {
		c, err := postman.Import([]byte(f.Content))
		if err != nil {
			result.Failed = append(result.Failed, importBulkFailed{FileName: f.FileName, Reason: err.Error()})
			continue
		}
		c.WorkspaceID = body.WorkspaceID
		created, err := h.createCollectionDeduped(c)
		if err != nil {
			result.Failed = append(result.Failed, importBulkFailed{FileName: f.FileName, Reason: err.Error()})
			continue
		}
		result.Imported = append(result.Imported, importBulkImported{FileName: f.FileName, CollectionID: created.ID, CollectionName: created.Name})
	}
	writeJSON(w, http.StatusOK, result)
}

// importEnvironment parses a Postman *.postman_environment.json export —
// previously there was no way to bring one in at all, only a collection
// (whose own collection-level `variable[]` is handled separately, by
// postman.Import/Export). A real Postman workflow commonly ships an
// environment as a SEPARATE file from its collection, so this is a
// distinct endpoint/action, not folded into importCollection.
func (h *APIClientHandler) importEnvironment(w http.ResponseWriter, r *http.Request) {
	var body struct {
		PostmanJSON string `json:"postmanJson"`
		WorkspaceID string `json:"workspaceId"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	e, err := postman.ImportEnvironment([]byte(body.PostmanJSON))
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	e.WorkspaceID = body.WorkspaceID
	created, err := h.createEnvironmentDeduped(e)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

// createEnvironmentDeduped mirrors createCollectionDeduped's own "<name>
// (2)", "<name> (3)" retry-on-collision behavior, for the same reason: an
// import is a one-shot paste with no chance to pick a different name first.
func (h *APIClientHandler) createEnvironmentDeduped(e *apiclient.Environment) (*apiclient.Environment, error) {
	baseName := e.Name
	created, err := h.store.CreateEnvironment(e)
	for attempt := 2; errors.Is(err, apiclient.ErrDuplicateEnvironmentName) && attempt <= 100; attempt++ {
		e.Name = fmt.Sprintf("%s (%d)", baseName, attempt)
		created, err = h.store.CreateEnvironment(e)
	}
	return created, err
}

// --- environments ---

func (h *APIClientHandler) listEnvironments(w http.ResponseWriter, r *http.Request) {
	workspaceID := r.URL.Query().Get("workspaceId")
	if !checkWorkspaceUnlocked(w, r, h.store, h.tracker, workspaceID) {
		return
	}
	list, err := h.store.ListEnvironments(workspaceID)
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *APIClientHandler) createEnvironment(w http.ResponseWriter, r *http.Request) {
	var e apiclient.Environment
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if !checkWorkspaceUnlocked(w, r, h.store, h.tracker, e.WorkspaceID) {
		return
	}
	created, err := h.store.CreateEnvironment(&e)
	if errors.Is(err, apiclient.ErrDuplicateEnvironmentName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *APIClientHandler) updateEnvironment(w http.ResponseWriter, r *http.Request) {
	var e apiclient.Environment
	if err := json.NewDecoder(r.Body).Decode(&e); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	e.ID = chi.URLParam(r, "id")
	// workspace_id isn't mutable via update (see Store.UpdateEnvironment) —
	// only the environment's CURRENT, actual workspace is ever at risk.
	if existing, err := h.store.GetEnvironment(e.ID); err == nil {
		if !checkWorkspaceUnlocked(w, r, h.store, h.tracker, existing.WorkspaceID) {
			return
		}
	}
	updated, err := h.store.UpdateEnvironment(&e)
	if errors.Is(err, apiclient.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, apiclient.ErrDuplicateEnvironmentName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *APIClientHandler) deleteEnvironment(w http.ResponseWriter, r *http.Request) {
	id := chi.URLParam(r, "id")
	if existing, err := h.store.GetEnvironment(id); err == nil {
		if !checkWorkspaceUnlocked(w, r, h.store, h.tracker, existing.WorkspaceID) {
			return
		}
	}
	if err := h.store.DeleteEnvironment(id); err != nil {
		if errors.Is(err, apiclient.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// --- request execution / curl / ws ---

type executeRequest struct {
	Spec      apiclient.RequestSpec `json:"spec"`
	Variables map[string]string     `json:"variables,omitempty"`
	// CollectionID/CollectionName/RequestName are purely for hit-log
	// attribution — never used to build the outbound request itself. Left
	// empty for a request sent from an unsaved/draft tab (not part of any
	// collection) or the load tester (logged separately, at a much higher
	// volume than a real hit log should carry — see loadTest).
	CollectionID   string `json:"collectionId,omitempty"`
	CollectionName string `json:"collectionName,omitempty"`
	RequestName    string `json:"requestName,omitempty"`
}

// execute passes r.Context() through to ExecuteContext (rather than the
// plain Execute wrapper) so the API client's "Force stop" button — which
// aborts the browser's fetch to this endpoint — actually cancels the real
// outbound network call too: aborting a fetch closes the underlying
// connection, net/http's server cancels r.Context() when that happens, and
// ExecuteContext ties the outbound request's context to it.
func (h *APIClientHandler) execute(w http.ResponseWriter, r *http.Request) {
	var req executeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.resolveClientCert(&req.Spec); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	start := time.Now()
	result := apiclient.ExecuteContext(r.Context(), req.Spec, req.Variables)
	h.recordOutboundHit(req, result, time.Since(start))
	writeJSON(w, http.StatusOK, result)
}

// recordOutboundHit logs one DirectionOutboundCall entry per "Send"/
// Collection Runner call — best-effort and never blocks/fails the actual
// response, matching every engine's own logInboundHit. Deliberately NOT
// called from loadTest: a load test can fire hundreds of requests a
// second, and flooding the hit log with synthetic traffic would swamp any
// real signal Log History/Dashboard are meant to surface.
func (h *APIClientHandler) recordOutboundHit(req executeRequest, result *apiclient.ExecutionResult, elapsed time.Duration) {
	if h.hitLogger == nil {
		return
	}
	targetURL := apiclient.EnsureScheme(apiclient.SubstituteVars(req.Spec.URL, req.Variables), "http://")
	latencyMs := result.Timing.TotalMs
	if latencyMs == 0 {
		latencyMs = elapsed.Milliseconds()
	}
	err := h.hitLogger.Record(&hitlog.Entry{
		ProtocolType:    "http",
		Direction:       hitlog.DirectionOutboundCall,
		Method:          strings.ToUpper(req.Spec.Method),
		TargetURL:       targetURL,
		RequestHeaders:  kvHeadersToMap(req.Spec.Headers),
		RequestBody:     req.Spec.Body,
		ResponseStatus:  result.StatusCode,
		ResponseHeaders: hitlog.HeadersFromMulti(result.Headers),
		ResponseBody:    result.Body,
		LatencyMs:       latencyMs,
		CollectionID:    req.CollectionID,
		CollectionName:  req.CollectionName,
		RequestName:     req.RequestName,
	})
	if err != nil {
		log.Printf("airmock: failed to record outbound API-client hit: %v", err)
	}
}

// kvHeadersToMap collapses a RequestSpec's ordered, disable-able header
// list into the plain map hitlog.Entry expects — a disabled row was never
// actually sent, so it's excluded here the same way applyHeaders/ToCurl
// already skip it when building the real request.
func kvHeadersToMap(headers []apiclient.KV) map[string]string {
	out := make(map[string]string, len(headers))
	for _, h := range headers {
		if h.Disabled {
			continue
		}
		out[h.Key] = h.Value
	}
	return out
}

type loadTestRequest struct {
	Spec          apiclient.RequestSpec `json:"spec"`
	Variables     map[string]string     `json:"variables,omitempty"`
	Concurrency   int                   `json:"concurrency"`
	TotalRequests int                   `json:"totalRequests,omitempty"`
	DurationSecs  int                   `json:"durationSecs,omitempty"`
	Detailed      bool                  `json:"detailed,omitempty"`
	// ItemID/CollectionID identify which saved request this run is for —
	// '' for an unsaved/draft tab, still persisted (see saveLoadTestRun)
	// but invisible to any item's history list.
	ItemID       string `json:"itemId,omitempty"`
	CollectionID string `json:"collectionId,omitempty"`
}

// loadTest blocks for the whole run and returns the final summary — no
// streaming/progress protocol, which keeps this a plain request/response
// call like every other apiclient endpoint. RunLoadTest itself clamps
// concurrency/requests/duration to sane caps, so a large or malformed input
// here degrades to "runs the capped version" rather than erroring. Passing
// r.Context() lets the admin UI's "Stop" button abort the run early — the
// browser cancelling its fetch cancels this request's context, which
// RunLoadTest's worker pool checks between iterations.
func (h *APIClientHandler) loadTest(w http.ResponseWriter, r *http.Request) {
	var req loadTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if err := h.resolveClientCert(&req.Spec); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg := apiclient.LoadTestConfig{
		Concurrency:   req.Concurrency,
		TotalRequests: req.TotalRequests,
		DurationSecs:  req.DurationSecs,
		Detailed:      req.Detailed,
	}
	result := apiclient.RunLoadTest(r.Context(), req.Spec, req.Variables, cfg)
	h.saveLoadTestRun(req.ItemID, req.CollectionID, req.Spec.Method, req.Spec.URL, cfg, result)
	writeJSON(w, http.StatusOK, result)
}

type wsLoadTestRequest struct {
	Spec          apiclient.WSRequestSpec `json:"spec"`
	Variables     map[string]string       `json:"variables,omitempty"`
	Concurrency   int                     `json:"concurrency"`
	TotalRequests int                     `json:"totalRequests,omitempty"`
	DurationSecs  int                     `json:"durationSecs,omitempty"`
	Detailed      bool                    `json:"detailed,omitempty"`
	ItemID        string                  `json:"itemId,omitempty"`
	CollectionID  string                  `json:"collectionId,omitempty"`
}

// wsLoadTest is loadTest's WebSocket counterpart, sharing the same
// blocking request/response shape (see loadTest's comment) and the same
// caller-supplied caps, clamped identically by RunWSLoadTest.
func (h *APIClientHandler) wsLoadTest(w http.ResponseWriter, r *http.Request) {
	var req wsLoadTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	cfg := apiclient.LoadTestConfig{
		Concurrency:   req.Concurrency,
		TotalRequests: req.TotalRequests,
		DurationSecs:  req.DurationSecs,
		Detailed:      req.Detailed,
	}
	result := apiclient.RunWSLoadTest(r.Context(), req.Spec, req.Variables, cfg)
	h.saveLoadTestRun(req.ItemID, req.CollectionID, "WS", req.Spec.URL, cfg, result)
	writeJSON(w, http.StatusOK, result)
}

// saveLoadTestRun persists a just-completed run's history — best-effort,
// matching this codebase's existing "cleanup/logging side effect must
// never fail the real operation" convention (see e.g. mock.Store.Delete's
// own best-effort dynamic-value cleanup): a save failure is logged, never
// returned to the caller, whose run already succeeded and whose result is
// already about to be written back regardless.
func (h *APIClientHandler) saveLoadTestRun(itemID, collectionID, method, url string, cfg apiclient.LoadTestConfig, result *apiclient.LoadTestResult) {
	run := &apiclient.LoadTestRun{
		ItemID: itemID, CollectionID: collectionID, Method: method, URL: url,
		Config: apiclient.ClampLoadTestConfig(cfg),
		Result: result,
	}
	if err := h.store.SaveLoadTestRun(run); err != nil {
		log.Printf("airmock: failed to save load test run history: %v", err)
	}
}

func (h *APIClientHandler) listLoadTestRuns(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.ListLoadTestRuns(r.URL.Query().Get("itemId"))
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *APIClientHandler) getLoadTestRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.store.GetLoadTestRun(chi.URLParam(r, "id"))
	if errors.Is(err, apiclient.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// exportLoadTestRun mirrors HitLogHandler.export's own shape (CSV by
// default, or ?format=json) — the full run (config, aggregate, every
// sample, not just what's on screen), as a downloadable file.
func (h *APIClientHandler) exportLoadTestRun(w http.ResponseWriter, r *http.Request) {
	run, err := h.store.GetLoadTestRun(chi.URLParam(r, "id"))
	if errors.Is(err, apiclient.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="airmock-loadtest-%s.json"`, run.ID))
		json.NewEncoder(w).Encode(run)
		return
	}
	writeLoadTestRunCSV(w, run)
}

var loadTestRunCSVSummaryHeader = []string{
	"runId", "method", "url", "createdAt", "totalRequests", "requestsPerSec",
	"minMs", "avgMs", "p50Ms", "p90Ms", "p95Ms", "p99Ms", "maxMs",
	"count2xx", "count3xx", "count4xx", "count5xx", "countError",
}

var loadTestRunCSVSampleHeader = []string{"index", "elapsedMs", "latencyMs", "statusCode", "error"}

// writeLoadTestRunCSV writes a short one-row summary block, a blank
// separator row, then a per-sample table — two sections in one file rather
// than two separate downloads, since both describe the same run and a
// spreadsheet's "skip to row N" is trivial once you know the sample table's
// header repeats the column names.
func writeLoadTestRunCSV(w http.ResponseWriter, run *apiclient.LoadTestRun) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="airmock-loadtest-%s.csv"`, run.ID))

	cw := csv.NewWriter(w)
	cw.Write(loadTestRunCSVSummaryHeader)
	res := run.Result
	cw.Write([]string{
		run.ID, csvFormulaGuard(run.Method), csvFormulaGuard(run.URL), run.CreatedAt.Format(time.RFC3339),
		strconv.Itoa(res.TotalRequests), strconv.FormatFloat(res.RequestsPerSec, 'f', -1, 64),
		strconv.FormatInt(res.MinMs, 10), strconv.FormatInt(res.AvgMs, 10),
		strconv.FormatInt(res.P50Ms, 10), strconv.FormatInt(res.P90Ms, 10), strconv.FormatInt(res.P95Ms, 10), strconv.FormatInt(res.P99Ms, 10),
		strconv.FormatInt(res.MaxMs, 10),
		strconv.Itoa(res.Statuses.Count2xx), strconv.Itoa(res.Statuses.Count3xx), strconv.Itoa(res.Statuses.Count4xx),
		strconv.Itoa(res.Statuses.Count5xx), strconv.Itoa(res.Statuses.CountError),
	})
	cw.Write(make([]string, len(loadTestRunCSVSummaryHeader))) // blank separator row
	cw.Write(loadTestRunCSVSampleHeader)
	for _, s := range res.Samples {
		cw.Write([]string{
			strconv.Itoa(s.Index), strconv.FormatInt(s.ElapsedMs, 10), strconv.FormatInt(s.LatencyMs, 10),
			strconv.Itoa(s.StatusCode), csvFormulaGuard(s.Error),
		})
	}
	cw.Flush()
}

// exportLoadTestResult handles a Detailed-run's download: full response
// bodies/headers are captured in memory (see loadtest.go's record()) and
// returned in a live run's own HTTP response, but are deliberately never
// persisted (apiclient.Store.SaveLoadTestRun only writes the 4 lightweight
// sample columns) — so exportLoadTestRun above, which looks a run up by id,
// can never serve them for ANY run, live or historical. This endpoint
// instead takes the full result the browser already has (POSTed back,
// since a plain <a href> can't carry a body) and renders exactly that —
// correct for a just-ran Detailed result, and callable at all only because
// the frontend gates its use on loadTestResult.detailed, which GetLoadTestRun
// always forces false on anything reloaded from history (see
// exportLoadTestRun's getLoadTestRun), so this is never reachable for a
// history row anyway.
type loadTestResultExportRequest struct {
	Result *apiclient.LoadTestResult `json:"result"`
}

func (h *APIClientHandler) exportLoadTestResult(w http.ResponseWriter, r *http.Request) {
	var req loadTestResultExportRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Result == nil {
		writeErr(w, http.StatusBadRequest, errors.New("missing result"))
		return
	}

	filename := "airmock-loadtest"
	if req.Result.RunID != "" {
		filename += "-" + req.Result.RunID
	}
	if r.URL.Query().Get("format") == "json" {
		w.Header().Set("Content-Type", "application/json")
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.json"`, filename))
		json.NewEncoder(w).Encode(req.Result)
		return
	}
	writeLoadTestResultDetailedCSV(w, req.Result, filename)
}

var loadTestResultCSVSummaryHeader = []string{
	"totalRequests", "requestsPerSec", // requestsPerSec = TPS (transactions/requests per second)
	"minMs", "avgMs", "p50Ms", "p90Ms", "p95Ms", "p99Ms", "maxMs",
	"count2xx", "count3xx", "count4xx", "count5xx", "countError",
}

var loadTestResultCSVSampleHeader = []string{
	"index", "elapsedMs", "latencyMs", "statusCode", "error", "responseHeaders", "responseBody",
}

// writeLoadTestResultDetailedCSV mirrors writeLoadTestRunCSV's two-section
// shape, but always includes the responseHeaders/responseBody columns
// (populated when the run was Detailed, blank otherwise) — the two extra
// columns exportLoadTestRun's persisted-run CSV never has, since the DB
// never stores them.
func writeLoadTestResultDetailedCSV(w http.ResponseWriter, res *apiclient.LoadTestResult, filename string) {
	w.Header().Set("Content-Type", "text/csv")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s.csv"`, filename))

	cw := csv.NewWriter(w)
	cw.Write(loadTestResultCSVSummaryHeader)
	cw.Write([]string{
		strconv.Itoa(res.TotalRequests), strconv.FormatFloat(res.RequestsPerSec, 'f', -1, 64),
		strconv.FormatInt(res.MinMs, 10), strconv.FormatInt(res.AvgMs, 10),
		strconv.FormatInt(res.P50Ms, 10), strconv.FormatInt(res.P90Ms, 10), strconv.FormatInt(res.P95Ms, 10), strconv.FormatInt(res.P99Ms, 10),
		strconv.FormatInt(res.MaxMs, 10),
		strconv.Itoa(res.Statuses.Count2xx), strconv.Itoa(res.Statuses.Count3xx), strconv.Itoa(res.Statuses.Count4xx),
		strconv.Itoa(res.Statuses.Count5xx), strconv.Itoa(res.Statuses.CountError),
	})
	cw.Write(make([]string, len(loadTestResultCSVSummaryHeader))) // blank separator row
	cw.Write(loadTestResultCSVSampleHeader)
	for _, s := range res.Samples {
		headersJSON, _ := json.Marshal(s.ResponseHeaders)
		cw.Write([]string{
			strconv.Itoa(s.Index), strconv.FormatInt(s.ElapsedMs, 10), strconv.FormatInt(s.LatencyMs, 10),
			strconv.Itoa(s.StatusCode), csvFormulaGuard(s.Error), string(headersJSON), csvFormulaGuard(s.ResponseBody),
		})
	}
	cw.Flush()
}

func (h *APIClientHandler) deleteLoadTestRun(w http.ResponseWriter, r *http.Request) {
	if err := h.store.DeleteLoadTestRun(chi.URLParam(r, "id")); err != nil {
		if errors.Is(err, apiclient.ErrNotFound) {
			writeErr(w, http.StatusNotFound, err)
			return
		}
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *APIClientHandler) curlImport(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Curl string `json:"curl"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	spec, err := apiclient.ParseCurl(body.Curl)
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, spec)
}

func (h *APIClientHandler) curlExport(w http.ResponseWriter, r *http.Request) {
	var spec apiclient.RequestSpec
	if err := json.NewDecoder(r.Body).Decode(&spec); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"curl": apiclient.ToCurl(spec)})
}

type snippetRequest struct {
	Spec     apiclient.RequestSpec `json:"spec"`
	Language string                `json:"language"` // "curl" | "js" | "python" | "go"
}

// codeSnippet is the generalized sibling of curlExport, covering every
// "copy as code" language the request panel offers — curl included, so the
// frontend can use one endpoint/modal for all of them instead of curl
// alone having a separate code path.
func (h *APIClientHandler) codeSnippet(w http.ResponseWriter, r *http.Request) {
	var req snippetRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	var code string
	switch req.Language {
	case "curl", "":
		code = apiclient.ToCurl(req.Spec)
	case "js":
		code = apiclient.ToJSFetch(req.Spec)
	case "python":
		code = apiclient.ToPythonRequests(req.Spec)
	case "go":
		code = apiclient.ToGo(req.Spec)
	default:
		writeErr(w, http.StatusBadRequest, fmt.Errorf("unknown snippet language %q", req.Language))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"code": code})
}

type wsExchangeRequest struct {
	Spec       apiclient.WSRequestSpec `json:"spec"`
	Variables  map[string]string       `json:"variables,omitempty"`
	ListenSecs int                     `json:"listenSecs,omitempty"`
}

func (h *APIClientHandler) wsExchange(w http.ResponseWriter, r *http.Request) {
	var req wsExchangeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	listenFor := time.Duration(req.ListenSecs) * time.Second
	result := apiclient.ExchangeWS(req.Spec, req.Variables, listenFor)
	writeJSON(w, http.StatusOK, result)
}
