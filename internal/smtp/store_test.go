package smtp

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

func TestGetSettingsDefaultsToUnconfigured(t *testing.T) {
	s := newTestStore(t)
	got, err := s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.Configured() {
		t.Fatalf("expected a fresh store to be unconfigured, got %+v", got)
	}
}

func TestSaveSettingsRoundTrips(t *testing.T) {
	s := newTestStore(t)
	want := Settings{Host: "smtp.example.com", Port: 587, Username: "user", Password: "pw", FromName: "AirMock Notifications", FromAddress: "mock@example.com", UseTLS: true}
	if _, err := s.SaveSettings(want); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	got, err := s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if got.Host != want.Host || got.Port != want.Port || got.Username != want.Username ||
		got.Password != want.Password || got.FromName != want.FromName || got.FromAddress != want.FromAddress || got.UseTLS != want.UseTLS {
		t.Fatalf("round trip mismatch: got %+v, want %+v", got, want)
	}
	if !got.Configured() {
		t.Fatal("expected settings with a host to report Configured()")
	}

	// Saving again must update the singleton row, not insert a second one.
	want.Host = "smtp2.example.com"
	if _, err := s.SaveSettings(want); err != nil {
		t.Fatalf("second SaveSettings: %v", err)
	}
	got, err = s.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings after update: %v", err)
	}
	if got.Host != "smtp2.example.com" {
		t.Fatalf("expected updated host, got %q", got.Host)
	}
}

func TestTemplateCRUD(t *testing.T) {
	s := newTestStore(t)

	created, err := s.CreateTemplate(&Template{Name: "Order confirmation", Subject: "Order {{.Request.Body}}", HTMLBody: "<p>Thanks!</p>"})
	if err != nil {
		t.Fatalf("CreateTemplate: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected CreateTemplate to assign an id")
	}

	list, err := s.ListTemplates()
	if err != nil {
		t.Fatalf("ListTemplates: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 template, got %d", len(list))
	}

	got, err := s.GetTemplate(created.ID)
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if got.Name != "Order confirmation" {
		t.Fatalf("unexpected name: %q", got.Name)
	}

	got.HTMLBody = "<p>Updated!</p>"
	if _, err := s.UpdateTemplate(got); err != nil {
		t.Fatalf("UpdateTemplate: %v", err)
	}
	reGot, err := s.GetTemplate(created.ID)
	if err != nil {
		t.Fatalf("GetTemplate after update: %v", err)
	}
	if reGot.HTMLBody != "<p>Updated!</p>" {
		t.Fatalf("expected updated body, got %q", reGot.HTMLBody)
	}

	if err := s.DeleteTemplate(created.ID); err != nil {
		t.Fatalf("DeleteTemplate: %v", err)
	}
	if _, err := s.GetTemplate(created.ID); !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestCreateTemplateRejectsDuplicateNameCaseInsensitively(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.CreateTemplate(&Template{Name: "Welcome"}); err != nil {
		t.Fatalf("first CreateTemplate: %v", err)
	}
	_, err := s.CreateTemplate(&Template{Name: " WELCOME "})
	if !errors.Is(err, ErrDuplicateName) {
		t.Fatalf("expected ErrDuplicateName, got %v", err)
	}
}

func TestUpdateTemplateMissingIDReturnsErrNotFound(t *testing.T) {
	s := newTestStore(t)
	_, err := s.UpdateTemplate(&Template{ID: "ghost", Name: "Ghost"})
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}
