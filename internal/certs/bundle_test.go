package certs

import "testing"

func TestBundleCRUDRoundTrip(t *testing.T) {
	s := newTestStore(t)
	ca := mustGenerateCA(t, "bundle-ca")
	if err := s.Save(ca); err != nil {
		t.Fatalf("save ca: %v", err)
	}
	server, err := Generate(GenerateRequest{Name: "bundle-server", Kind: KindServer, CommonName: "mock.local", KeyAlgorithm: KeyECDSA, ValidDays: 30, Issuer: ca})
	if err != nil {
		t.Fatalf("generate server: %v", err)
	}
	if err := s.Save(server); err != nil {
		t.Fatalf("save server: %v", err)
	}

	created, err := s.CreateBundle(&Bundle{Name: "my-bundle", CAID: ca.ID, ServerCertID: server.ID})
	if err != nil {
		t.Fatalf("CreateBundle: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected a generated ID")
	}

	fetched, err := s.GetBundle(created.ID)
	if err != nil {
		t.Fatalf("GetBundle: %v", err)
	}
	if fetched.Name != "my-bundle" || fetched.CAID != ca.ID || fetched.ServerCertID != server.ID || fetched.ClientCertID != "" {
		t.Fatalf("unexpected fetched bundle: %+v", fetched)
	}

	list, err := s.ListBundles()
	if err != nil {
		t.Fatalf("ListBundles: %v", err)
	}
	if len(list) != 1 || list[0].ID != created.ID {
		t.Fatalf("expected exactly the one bundle, got %+v", list)
	}

	fetched.Name = "renamed-bundle"
	updated, err := s.UpdateBundle(fetched)
	if err != nil {
		t.Fatalf("UpdateBundle: %v", err)
	}
	if updated.Name != "renamed-bundle" {
		t.Fatalf("expected renamed bundle, got %+v", updated)
	}

	if err := s.DeleteBundle(created.ID); err != nil {
		t.Fatalf("DeleteBundle: %v", err)
	}
	if _, err := s.GetBundle(created.ID); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
	// The underlying certs must survive the bundle's deletion — deleting a
	// bundle is only supposed to remove the grouping, never the certs.
	if _, err := s.Get(ca.ID); err != nil {
		t.Fatalf("expected the CA to still exist after deleting its bundle: %v", err)
	}
	if _, err := s.Get(server.ID); err != nil {
		t.Fatalf("expected the server cert to still exist after deleting its bundle: %v", err)
	}
}

func TestBundleRejectsDuplicateNameCaseInsensitively(t *testing.T) {
	s := newTestStore(t)
	ca := mustGenerateCA(t, "dup-ca")
	if err := s.Save(ca); err != nil {
		t.Fatalf("save ca: %v", err)
	}

	if _, err := s.CreateBundle(&Bundle{Name: "shared-name", CAID: ca.ID}); err != nil {
		t.Fatalf("first CreateBundle: %v", err)
	}
	if _, err := s.CreateBundle(&Bundle{Name: " Shared-Name ", CAID: ca.ID}); err != ErrDuplicateBundleName {
		t.Fatalf("expected ErrDuplicateBundleName, got %v", err)
	}
}

func TestUpdateBundleFailsForUnknownID(t *testing.T) {
	s := newTestStore(t)
	_, err := s.UpdateBundle(&Bundle{ID: "does-not-exist", Name: "x"})
	if err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
