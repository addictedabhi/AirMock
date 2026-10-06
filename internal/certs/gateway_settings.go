package certs

import (
	"database/sql"
	"errors"
	"fmt"
	"time"
)

// ClientCertMode is the tri-state that replaced a plain require-client-cert
// bool everywhere TLS is configured (gateway, project, TCP mock, SMTP
// mock): a listener can now require no client cert at all, request one but
// still accept a connection without it (tls.VerifyClientCertIfGiven), or
// reject any connection that doesn't present one (tls.RequireAndVerifyClientCert).
type ClientCertMode string

const (
	ClientCertNone     ClientCertMode = "none"
	ClientCertOptional ClientCertMode = "optional"
	ClientCertRequired ClientCertMode = "required"
)

// GatewaySettings describes the gateway's single optional HTTPS listener:
// which stored certificate it presents (directly via CertID, or via
// BundleID's own server cert), and how it handles client certificates.
// There is exactly one row (id='default') — TLS is configured at the
// gateway/instance level, not per mock, since all phase-1 HTTP-family
// mocks share one net/http.Server per port.
type GatewaySettings struct {
	Enabled bool   `json:"enabled"`
	CertID  string `json:"certId"`
	// BundleID/ClientCertMode fixed a real bug in the process — before JSON
	// tags existed here, PUT /api/gateway/tls's response (writeJSON(w, gs)
	// directly, unlike GET's own hand-built lowercase map) marshaled with
	// bare Go field names (e.g. "Enabled", "ClientCertMode"), silently
	// leaving the admin UI's post-save `gateway = await jreq(...)` reactive
	// state entirely undefined until the next page load re-fetched via GET.
	BundleID       string         `json:"bundleId,omitempty"`
	ClientCertMode ClientCertMode `json:"clientCertMode,omitempty"`
	ClientCAID     string         `json:"clientCaId,omitempty"`
	UpdatedAt      time.Time      `json:"updatedAt"`
}

func (s *Store) GetGatewaySettings() (*GatewaySettings, error) {
	row := s.db.QueryRow(
		`SELECT enabled, cert_id, require_client_cert, client_ca_id, client_cert_mode, bundle_id, updated_at
		 FROM gateway_tls_settings WHERE id='default'`)

	var enabledInt, requireInt int
	var certID, clientCAID, clientCertMode, bundleID sql.NullString
	var updatedAt string

	err := row.Scan(&enabledInt, &certID, &requireInt, &clientCAID, &clientCertMode, &bundleID, &updatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return &GatewaySettings{}, nil // no row yet => TLS disabled by default
	}
	if err != nil {
		return nil, fmt.Errorf("scan gateway tls settings: %w", err)
	}

	gs := &GatewaySettings{
		Enabled:        enabledInt != 0,
		CertID:         certID.String,
		BundleID:       bundleID.String,
		ClientCAID:     clientCAID.String,
		ClientCertMode: ClientCertMode(clientCertMode.String),
	}
	// Legacy rows saved before client_cert_mode existed: fall back to the
	// old bool so a pre-existing gateway mTLS config keeps working exactly
	// as configured, rather than silently reverting to "none" on upgrade.
	if gs.ClientCertMode == "" && requireInt != 0 {
		gs.ClientCertMode = ClientCertRequired
	}
	if gs.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return nil, fmt.Errorf("parse updated_at: %w", err)
	}
	return gs, nil
}

func (s *Store) SaveGatewaySettings(gs *GatewaySettings) error {
	gs.UpdatedAt = time.Now().UTC()
	_, err := s.db.Exec(
		`INSERT INTO gateway_tls_settings (id, enabled, cert_id, require_client_cert, client_ca_id, client_cert_mode, bundle_id, updated_at)
		 VALUES ('default', ?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(id) DO UPDATE SET
		   enabled=excluded.enabled, cert_id=excluded.cert_id,
		   require_client_cert=excluded.require_client_cert,
		   client_ca_id=excluded.client_ca_id,
		   client_cert_mode=excluded.client_cert_mode,
		   bundle_id=excluded.bundle_id, updated_at=excluded.updated_at`,
		boolToInt(gs.Enabled), nullableString(gs.CertID), boolToInt(gs.ClientCertMode == ClientCertRequired),
		nullableString(gs.ClientCAID), string(gs.ClientCertMode), nullableString(gs.BundleID), formatTime(gs.UpdatedAt),
	)
	if err != nil {
		return fmt.Errorf("save gateway tls settings: %w", err)
	}
	return nil
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
