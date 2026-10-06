package smtpengine

import (
	"bufio"
	"context"
	"crypto/tls"
	"fmt"
	"testing"
	"time"

	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/mock"
)

type fakeCertProviderMulti struct {
	byID map[string]*certs.Certificate
}

func (f *fakeCertProviderMulti) Get(id string) (*certs.Certificate, error) {
	c, ok := f.byID[id]
	if !ok {
		return nil, fmt.Errorf("not found: %s", id)
	}
	return c, nil
}

func (f *fakeCertProviderMulti) GetBundle(id string) (*certs.Bundle, error) {
	return nil, fmt.Errorf("no bundle: %s", id)
}

// TestSMTPMockTLS guards the existing server-only TLS path (implicit TLS,
// like a real port-465 relay) still works — no test exercised this at all
// before, only the mTLS extension below is new.
func TestSMTPMockTLS(t *testing.T) {
	cert, err := certs.Generate(certs.GenerateRequest{Name: "server", Kind: certs.KindCA, CommonName: "airmock-test", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1})
	if err != nil {
		t.Fatalf("generate cert: %v", err)
	}
	cert.ID = "cert-1"

	e := New()
	e.SetCertProvider(&fakeCertProviderMulti{byID: map[string]*certs.Certificate{cert.ID: cert}})
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "tls-test", Name: "tls-test", Enabled: true, ProtocolType: "smtp",
		SMTP: &mock.SMTPConfig{TLS: &mock.TCPTLSConfig{CertificateID: cert.ID}, DefaultAccept: true},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		t.Fatalf("tls dial: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	reader := bufio.NewReader(conn)
	if _, err := reader.ReadString('\n'); err != nil {
		t.Fatalf("expected a banner over TLS: %v", err)
	}
}

// TestSMTPMockMTLSAcceptsClientCertSignedByConfiguredCA guards against a
// real gap: SMTP mocks could only do server-side TLS, never mTLS — unlike
// the HTTP gateway, which has always supported RequireClientCert/ClientCAID
// via certs.GatewaySettings.
func TestSMTPMockMTLSAcceptsClientCertSignedByConfiguredCA(t *testing.T) {
	ca, err := certs.Generate(certs.GenerateRequest{Name: "ca", Kind: certs.KindCA, CommonName: "airmock-test-ca", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1})
	if err != nil {
		t.Fatalf("generate ca: %v", err)
	}
	ca.ID = "ca-1"
	clientCert, err := certs.Generate(certs.GenerateRequest{Name: "client", Kind: certs.KindClient, CommonName: "test-client", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1, Issuer: ca})
	if err != nil {
		t.Fatalf("generate client cert: %v", err)
	}

	e := New()
	e.SetCertProvider(&fakeCertProviderMulti{byID: map[string]*certs.Certificate{ca.ID: ca}})
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "mtls-ok", Name: "mtls-ok", Enabled: true, ProtocolType: "smtp",
		SMTP: &mock.SMTPConfig{
			TLS:           &mock.TCPTLSConfig{CertificateID: ca.ID, ClientCertMode: "required", ClientCAID: ca.ID},
			DefaultAccept: true,
		},
	}
	addr := registerOnFreePort(t, e, m)

	clientTLSCert, err := tls.X509KeyPair([]byte(clientCert.CertPEM), []byte(clientCert.KeyPEM))
	if err != nil {
		t.Fatalf("build client tls cert: %v", err)
	}
	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true, Certificates: []tls.Certificate{clientTLSCert}})
	if err != nil {
		t.Fatalf("tls dial with valid client cert: %v", err)
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := bufio.NewReader(conn).ReadString('\n'); err != nil {
		t.Fatalf("expected a banner once the client cert is accepted: %v", err)
	}
}

// TestSMTPMockMTLSRejectsConnectionWithNoClientCert confirms the flip side:
// a client presenting no certificate at all must be rejected once
// RequireClientCert is on.
func TestSMTPMockMTLSRejectsConnectionWithNoClientCert(t *testing.T) {
	ca, err := certs.Generate(certs.GenerateRequest{Name: "ca", Kind: certs.KindCA, CommonName: "airmock-test-ca", KeyAlgorithm: certs.KeyECDSA, ValidDays: 1})
	if err != nil {
		t.Fatalf("generate ca: %v", err)
	}
	ca.ID = "ca-1"

	e := New()
	e.SetCertProvider(&fakeCertProviderMulti{byID: map[string]*certs.Certificate{ca.ID: ca}})
	defer e.Stop(context.Background())

	m := &mock.Definition{
		ID: "mtls-reject", Name: "mtls-reject", Enabled: true, ProtocolType: "smtp",
		SMTP: &mock.SMTPConfig{
			TLS:           &mock.TCPTLSConfig{CertificateID: ca.ID, ClientCertMode: "required", ClientCAID: ca.ID},
			DefaultAccept: true,
		},
	}
	addr := registerOnFreePort(t, e, m)

	conn, err := tls.Dial("tcp", addr, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return // dial itself failing is an acceptable way for this to manifest
	}
	defer conn.Close()
	conn.SetDeadline(time.Now().Add(2 * time.Second))
	if _, err := bufio.NewReader(conn).ReadString('\n'); err == nil {
		t.Fatal("expected reading the banner to fail without a client certificate")
	}
}
