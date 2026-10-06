package auth

import (
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

func TestGetCredentialReturnsEmptyWhenNeverSet(t *testing.T) {
	s := newTestStore(t)
	credType, hash, err := s.GetCredential()
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if hash != "" {
		t.Fatalf("expected an empty hash on a fresh store, got %q", hash)
	}
	if credType != "" {
		t.Fatalf("expected an empty credential type on a fresh store, got %q", credType)
	}
}

func TestSetAndGetCredentialRoundTrip(t *testing.T) {
	s := newTestStore(t)
	if err := s.SetCredential("pin", "some-bcrypt-hash"); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	credType, hash, err := s.GetCredential()
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if hash != "some-bcrypt-hash" {
		t.Fatalf("expected the saved hash back, got %q", hash)
	}
	if credType != "pin" {
		t.Fatalf("expected the saved credential type back, got %q", credType)
	}
}

func TestSetCredentialOverwritesExisting(t *testing.T) {
	s := newTestStore(t)
	s.SetCredential("pin", "first-hash")
	s.SetCredential("password", "second-hash")
	credType, hash, err := s.GetCredential()
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if hash != "second-hash" || credType != "password" {
		t.Fatalf("expected the second (overwriting) credential, got type=%q hash=%q", credType, hash)
	}
}

func TestSetCredentialEmptyHashClearsIt(t *testing.T) {
	s := newTestStore(t)
	s.SetCredential("password", "a-hash")
	if err := s.SetCredential("", ""); err != nil {
		t.Fatalf("SetCredential: %v", err)
	}
	credType, hash, err := s.GetCredential()
	if err != nil {
		t.Fatalf("GetCredential: %v", err)
	}
	if hash != "" || credType != "" {
		t.Fatalf("expected the credential to be cleared, got type=%q hash=%q", credType, hash)
	}
}
