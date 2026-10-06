// Package certs is a certificate store: generate any number of named CAs
// and leaf certificates independently, bind whichever one you want to the
// gateway's TLS listener, and rebind to a different one at any time without
// regenerating anything. Built entirely on the Go standard library
// (crypto/x509, crypto/tls, crypto/ecdsa, crypto/rsa) — no third-party cert
// library, both to stay dependency-light and to sidestep the exact
// node-forge multi-byte-subject corruption bug hit in a prior project (see
// the project memory): keep Subject/SAN fields ASCII-only.
package certs

import "time"

type Kind string

const (
	KindCA     Kind = "ca"
	KindServer Kind = "server"
	KindClient Kind = "client"
)

type KeyAlgorithm string

const (
	KeyRSA   KeyAlgorithm = "rsa"
	KeyECDSA KeyAlgorithm = "ecdsa"
)

// Certificate is one entry in the store — a CA, or a server/client leaf
// cert signed by a CA entry (IssuerID) or self-signed (IssuerID == "").
type Certificate struct {
	ID           string       `json:"id"`
	Name         string       `json:"name"`
	Kind         Kind         `json:"kind"`
	IssuerID     string       `json:"issuerId,omitempty"`
	CommonName   string       `json:"commonName"`
	SANs         []string     `json:"sans,omitempty"`
	KeyAlgorithm KeyAlgorithm `json:"keyAlgorithm"`
	CertPEM      string       `json:"certPem"`
	KeyPEM       string       `json:"-"` // never serialized to API responses; use the dedicated download endpoint
	// HasKey reports whether KeyPEM is non-empty, without exposing the key
	// material itself — every generated cert has one, but an imported cert
	// can be a pure trust anchor (a CA's public cert with no private key).
	// The API client's "present as a client certificate" picker uses this to
	// only offer certs that can actually be presented.
	HasKey    bool      `json:"hasKey"`
	NotBefore time.Time `json:"notBefore"`
	NotAfter  time.Time `json:"notAfter"`
	CreatedAt time.Time `json:"createdAt"`
}

// GenerateRequest is the input to Generate.
type GenerateRequest struct {
	Name         string
	Kind         Kind
	CommonName   string
	SANs         []string
	KeyAlgorithm KeyAlgorithm
	ValidDays    int
	Issuer       *Certificate // nil => self-signed (only valid for Kind == KindCA)
}
