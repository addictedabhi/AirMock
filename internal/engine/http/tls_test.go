package httpengine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"net/http"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/mock"
)

func mustGenCA(t *testing.T, name string) *certs.Certificate {
	t.Helper()
	ca, err := certs.Generate(certs.GenerateRequest{
		Name: name, Kind: certs.KindCA, CommonName: name + " Root CA",
		KeyAlgorithm: certs.KeyECDSA, ValidDays: 30,
	})
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	return ca
}

func mustGenLeaf(t *testing.T, kind certs.Kind, cn string, sans []string, issuer *certs.Certificate) *certs.Certificate {
	t.Helper()
	c, err := certs.Generate(certs.GenerateRequest{
		Name: cn, Kind: kind, CommonName: cn, SANs: sans,
		KeyAlgorithm: certs.KeyECDSA, ValidDays: 30, Issuer: issuer,
	})
	if err != nil {
		t.Fatalf("generate %s cert: %v", kind, err)
	}
	return c
}

func trustPool(cas ...*certs.Certificate) *x509.CertPool {
	pool := x509.NewCertPool()
	for _, ca := range cas {
		pool.AppendCertsFromPEM([]byte(ca.CertPEM))
	}
	return pool
}

func TestGatewayTLSRebindToMismatchedHostnameCertFailsVerification(t *testing.T) {
	ca := mustGenCA(t, "gw")
	certA := mustGenLeaf(t, certs.KindServer, "127.0.0.1", []string{"127.0.0.1"}, ca)
	certB := mustGenLeaf(t, certs.KindServer, "mismatched.invalid", []string{"mismatched.invalid"}, ca)

	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "ok", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `ok`},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := "127.0.0.1:18730"
	if err := e.EnsureTLS(ctx, addr, TLSSettings{CertPEM: []byte(certA.CertPEM), KeyPEM: []byte(certA.KeyPEM)}); err != nil {
		t.Fatalf("EnsureTLS: %v", err)
	}
	defer e.DisableTLS(context.Background())

	client := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trustPool(ca)}},
		Timeout:   3 * time.Second,
	}

	resp, err := client.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("expected successful request against certA, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}

	// Rebind to certB (same trusted CA, different hostname) — the client's
	// trust store still recognizes the issuer, but "127.0.0.1" is no longer
	// among the presented cert's SANs, so hostname verification must fail.
	e.UpdateTLSSettings(TLSSettings{CertPEM: []byte(certB.CertPEM), KeyPEM: []byte(certB.KeyPEM)})

	_, err = client.Get("https://" + addr + "/")
	if err == nil {
		t.Fatal("expected hostname verification failure after rebinding to a mismatched-hostname cert")
	}
}

func TestGatewayMTLSRejectsWithoutClientCertAcceptsWithOne(t *testing.T) {
	ca := mustGenCA(t, "mtls")
	serverCert := mustGenLeaf(t, certs.KindServer, "127.0.0.1", []string{"127.0.0.1"}, ca)
	clientCert := mustGenLeaf(t, certs.KindClient, "tester", nil, ca)

	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "ok", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `ok`},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := "127.0.0.1:18731"
	err := e.EnsureTLS(ctx, addr, TLSSettings{
		CertPEM: []byte(serverCert.CertPEM), KeyPEM: []byte(serverCert.KeyPEM),
		ClientCertMode: "required", ClientCAPEM: []byte(ca.CertPEM),
	})
	if err != nil {
		t.Fatalf("EnsureTLS: %v", err)
	}
	defer e.DisableTLS(context.Background())

	noCertClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trustPool(ca)}},
		Timeout:   3 * time.Second,
	}
	if _, err := noCertClient.Get("https://" + addr + "/"); err == nil {
		t.Fatal("expected request without a client certificate to be rejected")
	}

	clientTLSCert, err := tls.X509KeyPair([]byte(clientCert.CertPEM), []byte(clientCert.KeyPEM))
	if err != nil {
		t.Fatalf("load client cert: %v", err)
	}
	withCertClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:      trustPool(ca),
			Certificates: []tls.Certificate{clientTLSCert},
		}},
		Timeout: 3 * time.Second,
	}
	resp, err := withCertClient.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("expected request with a valid client certificate to succeed, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp.StatusCode)
	}
}

// TestGatewayOptionalClientCertAcceptsBothWithAndWithoutOne guards the
// tri-state's middle option: unlike "required" (tested above), "optional"
// must still let an unauthenticated connection through (tls.VerifyClientCertIfGiven)
// while still verifying a client cert if one IS presented.
func TestGatewayOptionalClientCertAcceptsBothWithAndWithoutOne(t *testing.T) {
	ca := mustGenCA(t, "mtls-optional")
	serverCert := mustGenLeaf(t, certs.KindServer, "127.0.0.1", []string{"127.0.0.1"}, ca)
	clientCert := mustGenLeaf(t, certs.KindClient, "tester", nil, ca)

	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "ok", ProtocolType: "rest", Method: http.MethodGet, PathPattern: "/", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `ok`},
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	addr := "127.0.0.1:18732"
	err := e.EnsureTLS(ctx, addr, TLSSettings{
		CertPEM: []byte(serverCert.CertPEM), KeyPEM: []byte(serverCert.KeyPEM),
		ClientCertMode: "optional", ClientCAPEM: []byte(ca.CertPEM),
	})
	if err != nil {
		t.Fatalf("EnsureTLS: %v", err)
	}
	defer e.DisableTLS(context.Background())

	noCertClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{RootCAs: trustPool(ca)}},
		Timeout:   3 * time.Second,
	}
	resp, err := noCertClient.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("expected a connection without a client cert to be ACCEPTED under optional mode, got: %v", err)
	}
	resp.Body.Close()
	if resp.StatusCode != 200 {
		t.Fatalf("expected 200 with no client cert under optional mode, got %d", resp.StatusCode)
	}

	clientTLSCert, err := tls.X509KeyPair([]byte(clientCert.CertPEM), []byte(clientCert.KeyPEM))
	if err != nil {
		t.Fatalf("load client cert: %v", err)
	}
	withCertClient := &http.Client{
		Transport: &http.Transport{TLSClientConfig: &tls.Config{
			RootCAs:      trustPool(ca),
			Certificates: []tls.Certificate{clientTLSCert},
		}},
		Timeout: 3 * time.Second,
	}
	resp2, err := withCertClient.Get("https://" + addr + "/")
	if err != nil {
		t.Fatalf("expected request with a valid client certificate to also succeed under optional mode, got: %v", err)
	}
	resp2.Body.Close()
	if resp2.StatusCode != 200 {
		t.Fatalf("expected 200, got %d", resp2.StatusCode)
	}
}
