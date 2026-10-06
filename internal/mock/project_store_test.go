package mock

import "testing"

func TestProjectTLSRoundTrip(t *testing.T) {
	s := newTestStore(t)

	created, err := s.CreateProject(&Project{
		Name:        "tls-project",
		GatewayPort: 9443,
		TLS:         &TCPTLSConfig{CertificateID: "cert-1"},
	})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	if created.TLS == nil || created.TLS.CertificateID != "cert-1" {
		t.Fatalf("expected TLS to round-trip through create, got %+v", created.TLS)
	}

	fetched, err := s.GetProject(created.ID)
	if err != nil {
		t.Fatalf("GetProject: %v", err)
	}
	if fetched.TLS == nil || fetched.TLS.CertificateID != "cert-1" {
		t.Fatalf("expected TLS to round-trip through get, got %+v", fetched.TLS)
	}

	list, err := s.ListProjects()
	if err != nil {
		t.Fatalf("ListProjects: %v", err)
	}
	if len(list) != 1 || list[0].TLS == nil || list[0].TLS.CertificateID != "cert-1" {
		t.Fatalf("expected TLS to round-trip through list, got %+v", list)
	}

	// Updating to clear TLS must persist nil, not silently keep the old value.
	fetched.TLS = nil
	updated, err := s.UpdateProject(fetched)
	if err != nil {
		t.Fatalf("UpdateProject: %v", err)
	}
	if updated.TLS != nil {
		t.Fatalf("expected TLS cleared after update, got %+v", updated.TLS)
	}
	reFetched, err := s.GetProject(created.ID)
	if err != nil {
		t.Fatalf("GetProject after clear: %v", err)
	}
	if reFetched.TLS != nil {
		t.Fatalf("expected TLS to stay cleared, got %+v", reFetched.TLS)
	}
}

func TestTLSConfigForProject(t *testing.T) {
	s := newTestStore(t)

	if cfg, err := s.TLSConfigForProject(""); err != nil || cfg != nil {
		t.Fatalf("expected nil, nil for an empty projectID, got %+v, %v", cfg, err)
	}
	if cfg, err := s.TLSConfigForProject("does-not-exist"); err != nil || cfg != nil {
		t.Fatalf("expected nil, nil (fail open) for an unresolvable projectID, got %+v, %v", cfg, err)
	}

	p, err := s.CreateProject(&Project{Name: "gx", GatewayPort: 9444, TLS: &TCPTLSConfig{CertificateID: "cert-2", ClientCertMode: "required", ClientCAID: "ca-1"}})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	cfg, err := s.TLSConfigForProject(p.ID)
	if err != nil {
		t.Fatalf("TLSConfigForProject: %v", err)
	}
	if cfg == nil || cfg.CertificateID != "cert-2" || cfg.ClientCertMode != "required" || cfg.ClientCAID != "ca-1" {
		t.Fatalf("expected the project's TLS config, got %+v", cfg)
	}
}
