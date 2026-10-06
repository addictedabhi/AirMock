package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/certs"
)

// maxFetchedImportBytes caps how much of a remote import source (WSDL/
// GraphQL SDL/OpenAPI/Postman JSON) is read — these are hand-authored spec
// documents, not arbitrary downloads, so a generous cap here just guards
// against pointing the importer at something huge or endless by mistake.
const maxFetchedImportBytes = 10 << 20 // 10MB

// fetchAllowedAddrs lists exact "ip:port" addresses fetch-url may reach even
// though they are blocked ranges. It is only ever set by tests, which serve
// their fixtures from httptest servers on 127.0.0.1.
var fetchAllowedAddrs []string

// isBlockedFetchIP reports addresses fetch-url must never connect to:
// loopback, unspecified, link-local and multicast ranges, plus the
// well-known cloud-metadata addresses that sit outside them. Without this the
// endpoint is a way to make AirMock read other services on its own host
// (including its own admin API) and the cloud instance-metadata service.
// Private (RFC 1918) ranges are deliberately allowed: spec files are often
// served from internal hosts.
func isBlockedFetchIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if ip.IsLoopback() || ip.IsUnspecified() || ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() || ip.IsMulticast() {
		return true
	}
	return ip.Equal(net.ParseIP("100.100.100.200")) || ip.Equal(net.ParseIP("fd00:ec2::254"))
}

var errFetchBlocked = errors.New("blocked: AirMock will not fetch from loopback, link-local or cloud-metadata addresses (that would let anyone who can reach the admin API read other services on this host). Host the file on a non-local address, or paste or upload it instead")

// fetchClient dials through a guard that checks the address actually being
// connected to, so it also covers DNS names that resolve to a blocked
// address and every hop of a redirect chain.
func fetchClient() *http.Client {
	dialer := &net.Dialer{
		Timeout: 10 * time.Second,
		Control: func(network, address string, _ syscall.RawConn) error {
			host, _, err := net.SplitHostPort(address)
			if err != nil {
				return err
			}
			if isBlockedFetchIP(net.ParseIP(host)) && !fetchAddrAllowed(address) {
				return errFetchBlocked
			}
			return nil
		},
	}
	return &http.Client{
		Timeout:   10 * time.Second,
		Transport: &http.Transport{DialContext: dialer.DialContext, Proxy: http.ProxyFromEnvironment},
	}
}

func fetchAddrAllowed(address string) bool {
	for _, a := range fetchAllowedAddrs {
		if a == address {
			return true
		}
	}
	return false
}

type ToolsHandler struct{}

func NewToolsHandler() *ToolsHandler {
	return &ToolsHandler{}
}

func (h *ToolsHandler) Routes(r chi.Router) {
	r.Post("/tls-test", h.tlsTest)
	r.Post("/fetch-url", h.fetchURL)
}

type fetchURLRequest struct {
	URL string `json:"url"`
}

// fetchURL retrieves an import source (a WSDL/GraphQL SDL/OpenAPI/Postman
// JSON document) from a URL server-side, so the browser's importer doesn't
// need the target to allow cross-origin requests — the admin UI just needs
// this one same-origin call.
func (h *ToolsHandler) fetchURL(w http.ResponseWriter, r *http.Request) {
	var req fetchURLRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	parsed, err := url.Parse(req.URL)
	if err != nil || (parsed.Scheme != "http" && parsed.Scheme != "https") {
		writeErr(w, http.StatusBadRequest, errors.New("url must be a valid http:// or https:// URL"))
		return
	}

	resp, err := fetchClient().Get(parsed.String())
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("fetch %s: %w", parsed, err))
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("fetch %s: unexpected status %s", parsed, resp.Status))
		return
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchedImportBytes))
	if err != nil {
		writeErr(w, http.StatusBadGateway, fmt.Errorf("read response from %s: %w", parsed, err))
		return
	}
	writeJSON(w, http.StatusOK, map[string]string{"content": string(body)})
}

type tlsTestRequest struct {
	Target string `json:"target"` // "host:port"
}

func (h *ToolsHandler) tlsTest(w http.ResponseWriter, r *http.Request) {
	var req tlsTestRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if req.Target == "" {
		writeErr(w, http.StatusBadRequest, errors.New("target (host:port) is required"))
		return
	}

	result, err := certs.Inspect(req.Target, 5*time.Second)
	if err != nil {
		writeErr(w, http.StatusBadGateway, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}
