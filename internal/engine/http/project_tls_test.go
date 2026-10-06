package httpengine

import (
	"crypto/tls"
	"net/http"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/mock"
)

// fakeProjectResolver is a minimal, in-memory stand-in for mock.Store's
// GatewayPortForProject/api.ProjectTLSResolver pair — just enough to drive
// syncExtraListeners without a real database.
type fakeProjectResolver struct {
	port int
	tls  *TLSSettings
}

func (f *fakeProjectResolver) GatewayPortForProject(projectID string) (int, error) {
	if projectID == "" {
		return 0, nil
	}
	return f.port, nil
}

func (f *fakeProjectResolver) TLSSettingsForProject(projectID string) (*TLSSettings, error) {
	if projectID == "" {
		return nil, nil
	}
	return f.tls, nil
}

// TestProjectDedicatedPortServesTLS guards the actual feature this closes:
// a Mock Project with its own dedicated port previously always served plain
// HTTP regardless of any TLS config — now the project's own resolved
// TLSSettings wrap that dedicated listener, the same way TCPConfig.TLS
// wraps a TCP mock's own port.
func TestProjectDedicatedPortServesTLS(t *testing.T) {
	ca := mustGenCA(t, "proj")
	serverCert := mustGenLeaf(t, certs.KindServer, "127.0.0.1", []string{"127.0.0.1"}, ca)

	resolver := &fakeProjectResolver{
		port: 18740,
		tls:  &TLSSettings{CertPEM: []byte(serverCert.CertPEM), KeyPEM: []byte(serverCert.KeyPEM)},
	}

	e := New()
	e.SetProjectPortResolver(resolver)
	e.SetProjectTLSResolver(resolver)
	defer e.Stop(t.Context())

	if err := e.RegisterMock(&mock.Definition{
		ID: "ok", ProjectID: "p1", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `ok`},
	}); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trustPool(ca)}},
		Timeout:   3 * time.Second,
	}
	resp, err := client.Get("https://127.0.0.1:18740/")
	if err != nil {
		t.Fatalf("expected the project's dedicated port to serve TLS, got: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// A plain (non-TLS) client talking to a TLS-only port must not be
	// served the mock's actual 200 response in the clear — Go's TLS
	// listener recognizes the plaintext request and answers with its own
	// "Client sent an HTTP request to an HTTPS server" 400 rather than
	// erroring the connection outright, so the assertion is on status code,
	// not a transport error.
	plainClient := &http.Client{Timeout: 1 * time.Second}
	plainResp, err := plainClient.Get("http://127.0.0.1:18740/")
	if err != nil {
		t.Fatalf("expected a (rejecting) plaintext response rather than a transport error, got: %v", err)
	}
	defer plainResp.Body.Close()
	if plainResp.StatusCode == 200 {
		t.Fatal("expected a plain HTTP request against a TLS-enabled dedicated port to be rejected, not served the mock's response")
	}
}

// TestProjectDedicatedPortRestartsOnTLSToggle guards syncExtraListeners'
// trickiest case: a project's TLS setting flips after its dedicated
// listener is already running plain — since a listener can't add TLS in
// place, the port must be torn down and restarted, not left serving
// plaintext forever.
func TestProjectDedicatedPortRestartsOnTLSToggle(t *testing.T) {
	ca := mustGenCA(t, "toggle")
	serverCert := mustGenLeaf(t, certs.KindServer, "127.0.0.1", []string{"127.0.0.1"}, ca)

	resolver := &fakeProjectResolver{port: 18741} // TLS nil to start: plain HTTP

	e := New()
	e.SetProjectPortResolver(resolver)
	e.SetProjectTLSResolver(resolver)
	defer e.Stop(t.Context())

	def := &mock.Definition{
		ID: "ok", ProjectID: "p1", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `ok`},
	}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock: %v", err)
	}

	plainClient := &http.Client{Timeout: 3 * time.Second}
	resp, err := plainClient.Get("http://127.0.0.1:18741/")
	if err != nil {
		t.Fatalf("expected plain HTTP before TLS is enabled, got: %v", err)
	}
	resp.Body.Close()

	// Flip the project's TLS on and force a rebuild (re-registering the
	// same mock is enough to trigger syncExtraListeners again).
	resolver.tls = &TLSSettings{CertPEM: []byte(serverCert.CertPEM), KeyPEM: []byte(serverCert.KeyPEM)}
	if err := e.RegisterMock(def); err != nil {
		t.Fatalf("RegisterMock (after enabling TLS): %v", err)
	}

	tlsClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trustPool(ca)}},
		Timeout:   3 * time.Second,
	}
	tlsResp, err := tlsClient.Get("https://127.0.0.1:18741/")
	if err != nil {
		t.Fatalf("expected the port to now serve TLS after the toggle, got: %v", err)
	}
	tlsResp.Body.Close()
}
