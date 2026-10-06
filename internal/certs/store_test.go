package certs

import (
	"errors"
	"path/filepath"
	"testing"

	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestStore(t *testing.T) *Store {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return NewStore(db)
}

func TestSaveRejectsDuplicateNameCaseInsensitively(t *testing.T) {
	s := newTestStore(t)

	ca := mustGenerateCA(t, "root")
	if err := s.Save(ca); err != nil {
		t.Fatalf("first Save: %v", err)
	}

	dupe := mustGenerateCA(t, "root")
	dupe.Name = " " + ca.Name + " "
	err := s.Save(dupe)
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("expected ErrDuplicateName, got %v", err)
	}
}

// TestUpdateReplacesMaterialButKeepsID locks in the "renew in place"
// contract: the id must survive so every existing certId/clientCaId
// reference stays valid, while the cert/key material itself changes.
func TestUpdateReplacesMaterialButKeepsID(t *testing.T) {
	s := newTestStore(t)

	ca := mustGenerateCA(t, "root")
	if err := s.Save(ca); err != nil {
		t.Fatalf("Save: %v", err)
	}

	renewed, err := Generate(GenerateRequest{Name: ca.Name, Kind: ca.Kind, CommonName: ca.CommonName, KeyAlgorithm: ca.KeyAlgorithm, ValidDays: 730})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	renewed.ID = ca.ID
	if err := s.Update(renewed); err != nil {
		t.Fatalf("Update: %v", err)
	}

	got, err := s.Get(ca.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got.ID != ca.ID {
		t.Fatalf("expected id to survive renewal, got %q want %q", got.ID, ca.ID)
	}
	if got.CertPEM == ca.CertPEM {
		t.Fatal("expected the cert material to actually change after renewal")
	}
	if !got.NotAfter.After(ca.NotAfter) {
		t.Fatalf("expected renewed NotAfter (%v) to extend past the original (%v)", got.NotAfter, ca.NotAfter)
	}
}

func TestUpdateMissingIDReturnsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	ghost := mustGenerateCA(t, "ghost")
	if err := s.Update(ghost); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound updating a nonexistent id, got %v", err)
	}
}
