package certs

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func mustGenerateCA(t *testing.T, name string) *Certificate {
	t.Helper()
	ca, err := Generate(GenerateRequest{
		Name: name, Kind: KindCA, CommonName: name + " Root CA",
		KeyAlgorithm: KeyECDSA, ValidDays: 365,
	})
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	return ca
}

func TestGenerateServerCertSignedByCAVerifies(t *testing.T) {
	ca := mustGenerateCA(t, "test")

	server, err := Generate(GenerateRequest{
		Name: "server", Kind: KindServer, CommonName: "mock.local",
		SANs: []string{"mock.local", "127.0.0.1"}, KeyAlgorithm: KeyRSA, ValidDays: 30, Issuer: ca,
	})
	if err != nil {
		t.Fatalf("generate server cert: %v", err)
	}

	pool := x509.NewCertPool()
	caBlock, _ := pem.Decode([]byte(ca.CertPEM))
	caCert, err := x509.ParseCertificate(caBlock.Bytes)
	if err != nil {
		t.Fatalf("parse ca: %v", err)
	}
	pool.AddCert(caCert)

	leafBlock, _ := pem.Decode([]byte(server.CertPEM))
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}

	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "mock.local", Roots: pool}); err != nil {
		t.Fatalf("expected leaf to verify against its issuing CA, got: %v", err)
	}
}

// TestGenerateServerCertWithNoExplicitSANsStillVerifies is the regression
// test for a real report: a user fills in "localhost" as the Common Name
// and leaves the SANs field blank (an easy, common mistake — nothing in
// the form suggests they're two different things) — the CN alone can't
// satisfy hostname verification since Go 1.15+ (and every other modern TLS
// client) checks ONLY the SAN list, ignoring CommonName entirely. Without
// effectiveSANs, this produced a certificate with zero DNSNames/IPAddresses
// — "certificate is not valid for any names" — even though it visually
// looked like it named the right host.
func TestGenerateServerCertWithNoExplicitSANsStillVerifies(t *testing.T) {
	ca := mustGenerateCA(t, "test")

	server, err := Generate(GenerateRequest{
		Name: "server", Kind: KindServer, CommonName: "localhost",
		KeyAlgorithm: KeyECDSA, ValidDays: 30, Issuer: ca, // deliberately no SANs
	})
	if err != nil {
		t.Fatalf("generate server cert: %v", err)
	}
	if len(server.SANs) != 1 || server.SANs[0] != "localhost" {
		t.Fatalf("expected the stored SANs to include the auto-added CommonName, got %v", server.SANs)
	}

	pool := x509.NewCertPool()
	caBlock, _ := pem.Decode([]byte(ca.CertPEM))
	caCert, _ := x509.ParseCertificate(caBlock.Bytes)
	pool.AddCert(caCert)

	leafBlock, _ := pem.Decode([]byte(server.CertPEM))
	leaf, err := x509.ParseCertificate(leafBlock.Bytes)
	if err != nil {
		t.Fatalf("parse leaf: %v", err)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{DNSName: "localhost", Roots: pool}); err != nil {
		t.Fatalf("expected the leaf to verify against its own CommonName despite no explicit SANs, got: %v", err)
	}
}

// TestGenerateServerCertDoesNotDuplicateCommonNameInSANs covers the other
// half: a user who DOES explicitly list the CommonName among the SANs
// (e.g. "localhost, 127.0.0.1") must not end up with "localhost" appearing
// twice in the certificate's SAN list.
func TestGenerateServerCertDoesNotDuplicateCommonNameInSANs(t *testing.T) {
	ca := mustGenerateCA(t, "test")
	server, err := Generate(GenerateRequest{
		Name: "server", Kind: KindServer, CommonName: "localhost",
		SANs: []string{"localhost", "127.0.0.1"}, KeyAlgorithm: KeyECDSA, ValidDays: 30, Issuer: ca,
	})
	if err != nil {
		t.Fatalf("generate server cert: %v", err)
	}
	if len(server.SANs) != 2 {
		t.Fatalf("expected no duplicate SAN entry, got %v", server.SANs)
	}
}

// TestGenerateCADoesNotGetCommonNameAddedAsSAN covers that a CA's
// CommonName ("Test Root CA", say) is a human label, not a hostname — it
// must never be silently added as a SAN, unlike server/client certs.
func TestGenerateCADoesNotGetCommonNameAddedAsSAN(t *testing.T) {
	ca, err := Generate(GenerateRequest{Name: "test", Kind: KindCA, CommonName: "Test Root CA", KeyAlgorithm: KeyECDSA, ValidDays: 365})
	if err != nil {
		t.Fatalf("generate CA: %v", err)
	}
	if len(ca.SANs) != 0 {
		t.Fatalf("expected a CA to get no auto-added SAN, got %v", ca.SANs)
	}
}

func TestGenerateLeafWithoutIssuerFails(t *testing.T) {
	_, err := Generate(GenerateRequest{Name: "x", Kind: KindServer, CommonName: "x", KeyAlgorithm: KeyRSA})
	if err == nil {
		t.Fatal("expected error generating a server cert with no issuer")
	}
}

func TestGenerateRejectsNonASCIICommonName(t *testing.T) {
	ca := mustGenerateCA(t, "test")
	_, err := Generate(GenerateRequest{
		Name: "bad", Kind: KindServer, CommonName: "mock—local", // em-dash, non-ASCII
		KeyAlgorithm: KeyRSA, Issuer: ca,
	})
	if err == nil {
		t.Fatal("expected non-ASCII common name to be rejected")
	}
}

// selfSignedFixture builds a self-signed certificate directly (bypassing
// Generate, which never produces an already-expired cert) so analyzeChain's
// expired/self-signed/hostname-mismatch flags can be tested against exact
// fixture dates and names.
func selfSignedFixture(t *testing.T, commonName string, notBefore, notAfter time.Time) *x509.Certificate {
	t.Helper()
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		DNSNames:     []string{commonName},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return cert
}

func TestAnalyzeChainFlagsExpiredCertificate(t *testing.T) {
	expired := selfSignedFixture(t, "expired.local",
		time.Now().AddDate(-2, 0, 0), time.Now().AddDate(-1, 0, 0))

	result := analyzeChain([]*x509.Certificate{expired}, "expired.local")

	if !result.Expired {
		t.Error("expected Expired=true for a cert whose NotAfter is in the past")
	}
	if !result.Chain[0].IsSelfSigned {
		t.Error("expected IsSelfSigned=true when subject == issuer")
	}
	if result.VerificationError == "" {
		t.Error("expected a VerificationError for an untrusted self-signed cert")
	}
}

func TestAnalyzeChainFlagsHostnameMismatch(t *testing.T) {
	cert := selfSignedFixture(t, "wrong-host.local",
		time.Now().Add(-time.Hour), time.Now().AddDate(1, 0, 0))

	result := analyzeChain([]*x509.Certificate{cert}, "expected-host.local")

	if result.Expired {
		t.Error("did not expect Expired=true for a currently-valid cert")
	}
	if !result.HostnameMismatch {
		t.Error("expected HostnameMismatch=true when the dialed host doesn't match the cert's DNS names")
	}
}
