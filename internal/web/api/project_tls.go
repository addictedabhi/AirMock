package api

import (
	"fmt"

	"github.com/addictedabhi/airmock/internal/certs"
	httpengine "github.com/addictedabhi/airmock/internal/engine/http"
	"github.com/addictedabhi/airmock/internal/mock"
)

// ProjectTLSResolver adapts mock.Store (which knows a project's TLS config
// as a bare certificate ID) and certs.Store (which resolves that ID to a
// real certificate) into httpengine.ProjectTLSResolver's ready-to-use
// TLSSettings — the same ID-to-PEM resolution ApplyGatewayTLS does for the
// shared gateway, just per-project instead of once globally.
type ProjectTLSResolver struct {
	mockStore *mock.Store
	certStore *certs.Store
}

func NewProjectTLSResolver(mockStore *mock.Store, certStore *certs.Store) *ProjectTLSResolver {
	return &ProjectTLSResolver{mockStore: mockStore, certStore: certStore}
}

func (r *ProjectTLSResolver) TLSSettingsForProject(projectID string) (*httpengine.TLSSettings, error) {
	cfg, err := r.mockStore.TLSConfigForProject(projectID)
	if err != nil || cfg == nil {
		return nil, err
	}

	certID, clientCAID, err := resolveBundleTLSRefs(r.certStore, cfg.BundleID, cfg.CertificateID, cfg.ClientCAID)
	if err != nil {
		return nil, err
	}
	if certID == "" {
		return nil, nil
	}

	cert, err := r.certStore.Get(certID)
	if err != nil {
		return nil, fmt.Errorf("resolve project TLS cert: %w", err)
	}
	settings := &httpengine.TLSSettings{CertPEM: []byte(cert.CertPEM), KeyPEM: []byte(cert.KeyPEM)}

	switch certs.ClientCertMode(cfg.ClientCertMode) {
	case certs.ClientCertOptional, certs.ClientCertRequired:
		if clientCAID == "" {
			return nil, fmt.Errorf("project TLS requires a client CA but none is configured")
		}
		ca, err := r.certStore.Get(clientCAID)
		if err != nil {
			return nil, fmt.Errorf("resolve project TLS client CA: %w", err)
		}
		settings.ClientCertMode = cfg.ClientCertMode
		settings.ClientCAPEM = []byte(ca.CertPEM)
	}
	return settings, nil
}
