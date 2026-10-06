package certs

import (
	"crypto"
	"crypto/ecdsa"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/youmark/pkcs8"
	pkcs12 "software.sslmate.com/src/go-pkcs12"
)

// ImportRequest is the input to Import — externally-issued certificate
// material the user is uploading, as opposed to GenerateRequest which mints
// brand new material. Either P12Data, or CertData (optionally with
// KeyData), must be set.
type ImportRequest struct {
	Name string
	Kind Kind

	// CertData is one or more concatenated PEM "CERTIFICATE" blocks (a
	// "fullchain" file — leaf first, intermediates after) or a single raw
	// DER certificate.
	CertData []byte
	// KeyData is a PEM private key (PKCS#1, PKCS#8, or SEC1/EC — optionally
	// passphrase-encrypted, legacy OpenSSL or modern PKCS#8 style) or a raw
	// DER-encoded key. Optional: a CA's public cert can be imported as a
	// pure trust anchor (e.g. to verify client certs against) without ever
	// holding its private key.
	KeyData []byte

	// P12Data is a PKCS#12/.pfx bundle containing a cert (+ chain) and,
	// usually, its private key. Mutually exclusive with CertData/KeyData.
	P12Data []byte

	// Passphrase decrypts an encrypted PEM private key or a PKCS#12 file.
	Passphrase string
}

// Import parses uploaded certificate/key material in whatever format it
// happens to be in and normalizes it into a store entry — same shape
// Generate produces, so every downstream consumer (gateway TLS, mock
// listeners, bundles) treats an imported cert identically to a generated
// one.
func Import(req ImportRequest) (*Certificate, error) {
	var chain []*x509.Certificate
	var key crypto.PrivateKey

	switch {
	case len(req.P12Data) > 0:
		pk, leaf, caCerts, err := pkcs12.DecodeChain(req.P12Data, req.Passphrase)
		if err != nil {
			return nil, fmt.Errorf("parse PKCS#12 bundle: %w", err)
		}
		chain = append([]*x509.Certificate{leaf}, caCerts...)
		key = pk
	case len(req.CertData) > 0:
		var err error
		chain, err = parseCertsAnyFormat(req.CertData)
		if err != nil {
			return nil, fmt.Errorf("parse certificate: %w", err)
		}
		if len(req.KeyData) > 0 {
			key, err = parseKeyAnyFormat(req.KeyData, req.Passphrase)
			if err != nil {
				return nil, fmt.Errorf("parse private key: %w", err)
			}
		}
	default:
		return nil, errors.New("certificate data is required")
	}

	leaf := chain[0]
	certPEM := encodeCertChainPEM(chain)

	var keyPEM []byte
	if key != nil {
		signer, ok := key.(crypto.Signer)
		if !ok {
			return nil, errors.New("uploaded private key is not usable for TLS")
		}
		keyDER, err := x509.MarshalPKCS8PrivateKey(signer)
		if err != nil {
			return nil, fmt.Errorf("re-encode private key: %w", err)
		}
		keyPEM = pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

		// The same check tls.X509KeyPair performs at runtime when a mock
		// actually binds this cert — catch a mismatched cert/key pair now,
		// at import time, rather than the first time something tries to
		// serve TLS with it.
		if _, err := tls.X509KeyPair(certPEM, keyPEM); err != nil {
			return nil, fmt.Errorf("certificate and private key do not match: %w", err)
		}
	}

	algo := KeyRSA
	if _, ok := leaf.PublicKey.(*ecdsa.PublicKey); ok {
		algo = KeyECDSA
	}

	return &Certificate{
		ID:           uuid.NewString(),
		Name:         req.Name,
		Kind:         req.Kind,
		CommonName:   leaf.Subject.CommonName,
		SANs:         sansFromCert(leaf),
		KeyAlgorithm: algo,
		CertPEM:      string(certPEM),
		KeyPEM:       string(keyPEM),
		HasKey:       len(keyPEM) > 0,
		NotBefore:    leaf.NotBefore,
		NotAfter:     leaf.NotAfter,
		CreatedAt:    time.Now().UTC(),
	}, nil
}

func sansFromCert(c *x509.Certificate) []string {
	var out []string
	out = append(out, c.DNSNames...)
	for _, ip := range c.IPAddresses {
		out = append(out, ip.String())
	}
	return out
}

// parseCertsAnyFormat accepts one or more concatenated PEM "CERTIFICATE"
// blocks, or a single raw DER certificate, and returns them leaf-first in
// the same order they appeared.
func parseCertsAnyFormat(data []byte) ([]*x509.Certificate, error) {
	var out []*x509.Certificate
	rest := data
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			break
		}
		if block.Type != "CERTIFICATE" {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, err
		}
		out = append(out, cert)
	}
	if len(out) > 0 {
		return out, nil
	}

	// No PEM blocks at all — try it as raw DER.
	cert, err := x509.ParseCertificate(data)
	if err != nil {
		return nil, fmt.Errorf("not a valid PEM or DER certificate: %w", err)
	}
	return []*x509.Certificate{cert}, nil
}

func encodeCertChainPEM(chain []*x509.Certificate) []byte {
	var buf []byte
	for _, c := range chain {
		buf = append(buf, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: c.Raw})...)
	}
	return buf
}

// parseKeyAnyFormat accepts a PEM private key — PKCS#1, PKCS#8, or SEC1/EC,
// optionally passphrase-encrypted in either the legacy OpenSSL
// "Proc-Type"/"DEK-Info" form or the modern "ENCRYPTED PRIVATE KEY" (PKCS#8)
// form — or a raw DER-encoded key.
func parseKeyAnyFormat(data []byte, passphrase string) (crypto.PrivateKey, error) {
	block, _ := pem.Decode(data)
	if block == nil {
		return parseDERKey(data)
	}

	der := block.Bytes
	if block.Type == "ENCRYPTED PRIVATE KEY" {
		if passphrase == "" {
			return nil, errors.New("this private key is encrypted; a passphrase is required")
		}
		key, err := pkcs8.ParsePKCS8PrivateKey(der, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("decrypt private key: %w", err)
		}
		return key, nil
	}
	//lint:ignore SA1019 legacy OpenSSL PEM encryption is still encountered on real imports and worth supporting
	if x509.IsEncryptedPEMBlock(block) { //nolint:staticcheck
		if passphrase == "" {
			return nil, errors.New("this private key is encrypted; a passphrase is required")
		}
		//nolint:staticcheck
		decrypted, err := x509.DecryptPEMBlock(block, []byte(passphrase))
		if err != nil {
			return nil, fmt.Errorf("decrypt private key: %w", err)
		}
		der = decrypted
	}

	return parseDERKey(der)
}

func parseDERKey(der []byte) (crypto.PrivateKey, error) {
	if key, err := x509.ParsePKCS8PrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParsePKCS1PrivateKey(der); err == nil {
		return key, nil
	}
	if key, err := x509.ParseECPrivateKey(der); err == nil {
		return key, nil
	}
	return nil, errors.New("unrecognized private key format (expected PKCS#1, PKCS#8, or EC)")
}
