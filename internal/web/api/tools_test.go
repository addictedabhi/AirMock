package api

import (
	"bytes"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
)

// allowFetchTo lets a test fetch from its own httptest server (always on
// 127.0.0.1), which fetch-url refuses outside tests. Only that one address is
// exempted, so a redirect from it to anywhere else is still checked.
func allowFetchTo(t *testing.T, srv *httptest.Server) {
	t.Helper()
	fetchAllowedAddrs = []string{srv.Listener.Addr().String()}
	t.Cleanup(func() { fetchAllowedAddrs = nil })
}

func TestFetchURLReturnsRemoteContent(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<wsdl>fake spec content</wsdl>"))
	}))
	defer remote.Close()
	allowFetchTo(t, remote)

	r := chi.NewRouter()
	r.Route("/tools", NewToolsHandler().Routes)

	body, _ := json.Marshal(map[string]string{"url": remote.URL})
	req := httptest.NewRequest(http.MethodPost, "/tools/fetch-url", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var out map[string]string
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if out["content"] != "<wsdl>fake spec content</wsdl>" {
		t.Fatalf("expected the remote body, got %q", out["content"])
	}
}

func TestFetchURLRejectsNonHTTPScheme(t *testing.T) {
	r := chi.NewRouter()
	r.Route("/tools", NewToolsHandler().Routes)

	body, _ := json.Marshal(map[string]string{"url": "file:///etc/passwd"})
	req := httptest.NewRequest(http.MethodPost, "/tools/fetch-url", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a non-http(s) URL, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFetchURLPropagatesUpstreamFailure(t *testing.T) {
	remote := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer remote.Close()
	allowFetchTo(t, remote)

	r := chi.NewRouter()
	r.Route("/tools", NewToolsHandler().Routes)

	body, _ := json.Marshal(map[string]string{"url": remote.URL})
	req := httptest.NewRequest(http.MethodPost, "/tools/fetch-url", bytes.NewReader(body))
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadGateway {
		t.Fatalf("expected 502 when the upstream returns an error status, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFetchURLRefusesLocalAndMetadataAddresses(t *testing.T) {
	// A real local server that must never be reached.
	var reached bool
	local := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		w.Write([]byte("secret local service"))
	}))
	defer local.Close()

	r := chi.NewRouter()
	r.Route("/tools", NewToolsHandler().Routes)
	for _, target := range []string{
		local.URL,                                  // loopback
		"http://127.0.0.1:1/api/config",            // loopback literal
		"http://[::1]:1/",                          // IPv6 loopback
		"http://localhost:1/",                      // name resolving to loopback
		"http://169.254.169.254/latest/meta-data/", // cloud metadata (link-local)
		"http://0.0.0.0:1/",                        // unspecified
		"http://[fd00:ec2::254]/",                  // AWS IPv6 metadata
		"http://100.100.100.200/",                  // Alibaba metadata
	} {
		body, _ := json.Marshal(map[string]string{"url": target})
		rec := httptest.NewRecorder()
		r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tools/fetch-url", bytes.NewReader(body)))
		if rec.Code != http.StatusBadGateway && rec.Code != http.StatusForbidden {
			t.Errorf("%s: expected the fetch to be refused, got %d: %s", target, rec.Code, rec.Body.String())
		}
		if !bytes.Contains(rec.Body.Bytes(), []byte("loopback")) {
			t.Errorf("%s: expected an explanation mentioning loopback/link-local, got %s", target, rec.Body.String())
		}
	}
	if reached {
		t.Fatal("the local server was contacted")
	}
}

func TestFetchURLRefusesARedirectToALocalAddress(t *testing.T) {
	redirector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, "http://169.254.169.254/latest/meta-data/", http.StatusFound)
	}))
	defer redirector.Close()
	allowFetchTo(t, redirector) // the first hop is permitted; the redirect target is not

	r := chi.NewRouter()
	r.Route("/tools", NewToolsHandler().Routes)
	body, _ := json.Marshal(map[string]string{"url": redirector.URL})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodPost, "/tools/fetch-url", bytes.NewReader(body)))
	if rec.Code != http.StatusBadGateway || !bytes.Contains(rec.Body.Bytes(), []byte("loopback")) {
		t.Fatalf("a redirect to the metadata address must be refused, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestFetchBlocklistKeepsPrivateAndPublicAddressesAllowed(t *testing.T) {
	for _, ok := range []string{"10.1.2.3", "192.168.1.5", "172.16.0.9", "93.184.216.34", "2606:2800:220:1:248:1893:25c8:1946"} {
		if isBlockedFetchIP(net.ParseIP(ok)) {
			t.Errorf("%s must stay allowed (internal spec servers are legitimate)", ok)
		}
	}
}
