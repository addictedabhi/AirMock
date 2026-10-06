package certs

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net"
	"strings"
	"time"
)

// ChainEntry describes one certificate in a dialed TLS chain.
type ChainEntry struct {
	Subject            string    `json:"subject"`
	Issuer             string    `json:"issuer"`
	NotBefore          time.Time `json:"notBefore"`
	NotAfter           time.Time `json:"notAfter"`
	DNSNames           []string  `json:"dnsNames,omitempty"`
	SignatureAlgorithm string    `json:"signatureAlgorithm"`
	IsSelfSigned       bool      `json:"isSelfSigned"`
}

// Inspection is the result of dialing a TLS endpoint and inspecting its
// presented certificate chain — a lightweight built-in equivalent of
// `openssl s_client -connect`.
type Inspection struct {
	Chain             []ChainEntry `json:"chain"`
	Expired           bool         `json:"expired"`
	ExpiresSoon       bool         `json:"expiresSoon"` // within 14 days
	HostnameMismatch  bool         `json:"hostnameMismatch"`
	VerificationError string       `json:"verificationError,omitempty"`
}

// Inspect dials hostPort over TLS and reports the presented certificate
// chain. It never fails just because the chain doesn't verify — that's
// exactly the kind of misconfiguration this tool exists to surface — so
// InsecureSkipVerify is intentional, with verification re-run manually
// afterward to populate VerificationError/HostnameMismatch for display.
func Inspect(hostPort string, timeout time.Duration) (*Inspection, error) {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	dialer := &net.Dialer{Timeout: timeout}

	conn, err := tls.DialWithDialer(dialer, "tcp", hostPort, &tls.Config{InsecureSkipVerify: true})
	if err != nil {
		return nil, fmt.Errorf("dial %s: %w", hostPort, err)
	}
	defer conn.Close()

	state := conn.ConnectionState()
	if len(state.PeerCertificates) == 0 {
		return nil, fmt.Errorf("no certificate presented by %s", hostPort)
	}

	host, _, _ := net.SplitHostPort(hostPort)
	return analyzeChain(state.PeerCertificates, host), nil
}

// analyzeChain is Inspect's pure analysis step, split out so it can be unit
// tested against manufactured certificates (expired, self-signed, hostname
// mismatch) without needing a live TLS listener.
func analyzeChain(chain []*x509.Certificate, host string) *Inspection {
	result := &Inspection{}
	now := time.Now()

	for _, cert := range chain {
		result.Chain = append(result.Chain, ChainEntry{
			Subject:            cert.Subject.String(),
			Issuer:             cert.Issuer.String(),
			NotBefore:          cert.NotBefore,
			NotAfter:           cert.NotAfter,
			DNSNames:           cert.DNSNames,
			SignatureAlgorithm: cert.SignatureAlgorithm.String(),
			IsSelfSigned:       cert.Subject.String() == cert.Issuer.String(),
		})
	}

	leaf := chain[0]
	if now.After(leaf.NotAfter) {
		result.Expired = true
	} else if leaf.NotAfter.Sub(now) < 14*24*time.Hour {
		result.ExpiresSoon = true
	}

	if host != "" {
		if err := leaf.VerifyHostname(host); err != nil {
			result.HostnameMismatch = true
		}
	}

	opts := x509.VerifyOptions{
		DNSName:       host,
		Intermediates: x509.NewCertPool(),
	}
	for _, cert := range chain[1:] {
		opts.Intermediates.AddCert(cert)
	}
	if _, err := leaf.Verify(opts); err != nil {
		result.VerificationError = summarizeVerifyError(err)
	}

	return result
}

func summarizeVerifyError(err error) string {
	msg := err.Error()
	// x509 verify errors are verbose; keep the first line for a compact UI display.
	if idx := strings.Index(msg, "\n"); idx != -1 {
		msg = msg[:idx]
	}
	return msg
}
