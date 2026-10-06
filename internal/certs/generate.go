package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"slices"
	"time"

	"github.com/google/uuid"
)

// Generate creates and signs a new certificate per req, returning the store
// entry (PEM-encoded cert + private key, not yet persisted).
func Generate(req GenerateRequest) (*Certificate, error) {
	if err := assertASCII(req.CommonName); err != nil {
		return nil, fmt.Errorf("common name: %w", err)
	}
	for _, san := range req.SANs {
		if err := assertASCII(san); err != nil {
			return nil, fmt.Errorf("SAN %q: %w", san, err)
		}
	}
	if req.Kind != KindCA && req.Issuer == nil {
		return nil, fmt.Errorf("%s certificates must be signed by an issuer CA", req.Kind)
	}
	if req.Issuer != nil && req.Issuer.Kind != KindCA {
		return nil, fmt.Errorf("issuer %q is not a CA", req.Issuer.Name)
	}
	if req.ValidDays <= 0 {
		req.ValidDays = 365
	}

	priv, pub, err := generateKey(req.KeyAlgorithm)
	if err != nil {
		return nil, err
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, fmt.Errorf("generate serial: %w", err)
	}

	notBefore := time.Now().UTC()
	notAfter := notBefore.AddDate(0, 0, req.ValidDays)

	template := &x509.Certificate{
		SerialNumber: serial,
		Subject:      pkix.Name{CommonName: req.CommonName},
		NotBefore:    notBefore,
		NotAfter:     notAfter,
	}
	applySANs(template, effectiveSANs(req))

	var (
		parent    *x509.Certificate
		signerKey crypto.Signer
	)
	switch req.Kind {
	case KindCA:
		template.IsCA = true
		template.BasicConstraintsValid = true
		template.KeyUsage = x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature | x509.KeyUsageCRLSign
		parent = template // self-signed
		signerKey = priv
	case KindServer:
		template.KeyUsage = x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
		parent, signerKey, err = parseIssuer(req.Issuer)
		if err != nil {
			return nil, err
		}
	case KindClient:
		template.KeyUsage = x509.KeyUsageDigitalSignature
		template.ExtKeyUsage = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
		parent, signerKey, err = parseIssuer(req.Issuer)
		if err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("unknown certificate kind %q", req.Kind)
	}

	derBytes, err := x509.CreateCertificate(rand.Reader, template, parent, pub, signerKey)
	if err != nil {
		return nil, fmt.Errorf("create certificate: %w", err)
	}

	keyDER, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		return nil, fmt.Errorf("marshal private key: %w", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: derBytes})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	issuerID := ""
	if req.Issuer != nil {
		issuerID = req.Issuer.ID
	}

	return &Certificate{
		ID:           uuid.NewString(),
		Name:         req.Name,
		Kind:         req.Kind,
		IssuerID:     issuerID,
		CommonName:   req.CommonName,
		SANs:         effectiveSANs(req),
		KeyAlgorithm: req.KeyAlgorithm,
		CertPEM:      string(certPEM),
		KeyPEM:       string(keyPEM),
		HasKey:       true,
		NotBefore:    notBefore,
		NotAfter:     notAfter,
		CreatedAt:    time.Now().UTC(),
	}, nil
}

func generateKey(algo KeyAlgorithm) (crypto.Signer, crypto.PublicKey, error) {
	switch algo {
	case KeyECDSA:
		priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			return nil, nil, fmt.Errorf("generate ecdsa key: %w", err)
		}
		return priv, &priv.PublicKey, nil
	case KeyRSA, "":
		priv, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, nil, fmt.Errorf("generate rsa key: %w", err)
		}
		return priv, &priv.PublicKey, nil
	default:
		return nil, nil, fmt.Errorf("unknown key algorithm %q", algo)
	}
}

// effectiveSANs adds req.CommonName to req.SANs when it isn't already
// there — modern TLS clients (Go's crypto/tls included) verify a hostname
// solely against a certificate's Subject Alternative Names; CommonName has
// been ignored for that purpose since RFC 6125 / Go 1.15, so a server or
// client cert generated with only a CommonName ("localhost", say) and an
// empty SANs field produces a certificate with NO names to match at all —
// "certificate is not valid for any names, but wanted to match ..." — even
// though the form's CommonName field visually looks like it named the
// right host. Skipped for CA certs: a CA's CommonName is a human label
// ("My Root CA"), never a hostname, and CAs aren't hostname-verified in
// the first place.
func effectiveSANs(req GenerateRequest) []string {
	if req.Kind == KindCA || req.CommonName == "" || slices.Contains(req.SANs, req.CommonName) {
		return req.SANs
	}
	return append(append([]string{}, req.SANs...), req.CommonName)
}

func applySANs(template *x509.Certificate, sans []string) {
	for _, san := range sans {
		if ip := net.ParseIP(san); ip != nil {
			template.IPAddresses = append(template.IPAddresses, ip)
		} else {
			template.DNSNames = append(template.DNSNames, san)
		}
	}
}

func parseIssuer(issuer *Certificate) (*x509.Certificate, crypto.Signer, error) {
	certBlock, _ := pem.Decode([]byte(issuer.CertPEM))
	if certBlock == nil {
		return nil, nil, fmt.Errorf("issuer %q: invalid cert PEM", issuer.Name)
	}
	cert, err := x509.ParseCertificate(certBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("issuer %q: parse cert: %w", issuer.Name, err)
	}

	keyBlock, _ := pem.Decode([]byte(issuer.KeyPEM))
	if keyBlock == nil {
		return nil, nil, fmt.Errorf("issuer %q: invalid key PEM", issuer.Name)
	}
	key, err := x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
	if err != nil {
		return nil, nil, fmt.Errorf("issuer %q: parse key: %w", issuer.Name, err)
	}
	signer, ok := key.(crypto.Signer)
	if !ok {
		return nil, nil, fmt.Errorf("issuer %q: key is not a signer", issuer.Name)
	}
	return cert, signer, nil
}

func assertASCII(s string) error {
	for _, r := range s {
		if r > 127 {
			return fmt.Errorf("must be ASCII-only (found %q)", r)
		}
	}
	return nil
}
