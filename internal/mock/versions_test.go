package mock

import (
	"testing"

	"github.com/addictedabhi/airmock/internal/settings"
)

// fakeVersionSettingsProvider stands in for internal/settings.Store —
// returns whatever MaxVersionsPerMock the test configured, without needing
// a real settings.Store/DB round trip.
type fakeVersionSettingsProvider struct {
	maxVersionsPerMock int
}

func (f *fakeVersionSettingsProvider) Get() (*settings.Settings, error) {
	return &settings.Settings{MaxVersionsPerMock: f.maxVersionsPerMock}, nil
}

func TestVersionHistory_snapshotsOnUpdate(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(&Definition{
		Name: "m1", ProtocolType: "rest", Method: "GET", PathPattern: "/a", Enabled: true,
		Response: ResponseTemplate{StatusCode: 200, BodyTemplate: "v1"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	// No versions yet — Create doesn't snapshot (nothing existed before it).
	versions, err := s.ListVersions(created.ID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 0 {
		t.Fatalf("expected 0 versions right after create, got %d", len(versions))
	}

	created.Response.BodyTemplate = "v2"
	if _, err := s.Update(created); err != nil {
		t.Fatalf("Update: %v", err)
	}
	created.Response.BodyTemplate = "v3"
	if _, err := s.Update(created); err != nil {
		t.Fatalf("Update: %v", err)
	}

	versions, err = s.ListVersions(created.ID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions after 2 updates, got %d", len(versions))
	}
	// Most recent first: the last snapshot taken was just before the v2->v3
	// update, i.e. it captured "v2".
	if versions[0].Definition.Response.BodyTemplate != "v2" {
		t.Fatalf("expected the most recent version to be 'v2', got %q", versions[0].Definition.Response.BodyTemplate)
	}
	if versions[1].Definition.Response.BodyTemplate != "v1" {
		t.Fatalf("expected the oldest version to be 'v1', got %q", versions[1].Definition.Response.BodyTemplate)
	}
}

func TestVersionHistory_restore(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(&Definition{
		Name: "m2", ProtocolType: "rest", Method: "GET", PathPattern: "/b", Enabled: true,
		Response: ResponseTemplate{StatusCode: 200, BodyTemplate: "original"},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	created.Response.BodyTemplate = "changed"
	if _, err := s.Update(created); err != nil {
		t.Fatalf("Update: %v", err)
	}

	versions, err := s.ListVersions(created.ID)
	if err != nil || len(versions) != 1 {
		t.Fatalf("ListVersions: %v (len=%d)", err, len(versions))
	}
	originalVersionID := versions[0].ID

	restored, err := s.RestoreVersion(created.ID, originalVersionID)
	if err != nil {
		t.Fatalf("RestoreVersion: %v", err)
	}
	if restored.Response.BodyTemplate != "original" {
		t.Fatalf("expected restored body to be 'original', got %q", restored.Response.BodyTemplate)
	}
	if restored.ID != created.ID {
		t.Fatalf("expected restore to keep the same mock ID, got %q vs %q", restored.ID, created.ID)
	}

	// The restore itself was an Update, so it should have snapshotted the
	// "changed" state that was current right before the restore — undoing
	// a restore is possible too.
	versions, err = s.ListVersions(created.ID)
	if err != nil {
		t.Fatalf("ListVersions after restore: %v", err)
	}
	if len(versions) != 2 {
		t.Fatalf("expected 2 versions after the restore (original snapshot + pre-restore snapshot), got %d", len(versions))
	}
	if versions[0].Definition.Response.BodyTemplate != "changed" {
		t.Fatalf("expected the newest version to be the pre-restore 'changed' state, got %q", versions[0].Definition.Response.BodyTemplate)
	}

	// Fetching the live mock confirms the restore actually took effect.
	live, err := s.Get(created.ID)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if live.Response.BodyTemplate != "original" {
		t.Fatalf("expected live mock to be restored to 'original', got %q", live.Response.BodyTemplate)
	}
}

func TestVersionHistory_pruning(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(&Definition{
		Name: "m3", ProtocolType: "rest", Method: "GET", PathPattern: "/c", Enabled: true,
		Response: ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for i := 0; i < defaultMaxVersionsPerMock+5; i++ {
		created.Response.StatusCode = 200 + i
		if _, err := s.Update(created); err != nil {
			t.Fatalf("Update %d: %v", i, err)
		}
	}

	versions, err := s.ListVersions(created.ID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != defaultMaxVersionsPerMock {
		t.Fatalf("expected pruning to cap at %d versions, got %d", defaultMaxVersionsPerMock, len(versions))
	}
}

// TestVersionHistory_pruningRespectsConfiguredLimit is the regression test
// for making version-history depth globally configurable: with a
// SettingsProvider wired up, pruning must honor ITS MaxVersionsPerMock,
// not the hardcoded default.
func TestVersionHistory_pruningRespectsConfiguredLimit(t *testing.T) {
	s := newTestStore(t)
	s.SetSettingsProvider(&fakeVersionSettingsProvider{maxVersionsPerMock: 3})

	created, err := s.Create(&Definition{
		Name: "m4", ProtocolType: "rest", Method: "GET", PathPattern: "/d", Enabled: true,
		Response: ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for i := 0; i < 10; i++ {
		created.Response.StatusCode = 200 + i
		if _, err := s.Update(created); err != nil {
			t.Fatalf("Update %d: %v", i, err)
		}
	}

	versions, err := s.ListVersions(created.ID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != 3 {
		t.Fatalf("expected pruning to cap at the configured 3 versions, got %d", len(versions))
	}
}

// TestVersionHistory_settingsProviderZeroFallsBackToDefault mirrors
// settings.Store.Get's own "0 means not configured yet" handling — if a
// SettingsProvider returns 0 (e.g. a genuinely pre-migration settings row),
// pruning must still fall back to the hardcoded default rather than
// keeping zero versions.
func TestVersionHistory_settingsProviderZeroFallsBackToDefault(t *testing.T) {
	s := newTestStore(t)
	s.SetSettingsProvider(&fakeVersionSettingsProvider{maxVersionsPerMock: 0})

	created, err := s.Create(&Definition{
		Name: "m5", ProtocolType: "rest", Method: "GET", PathPattern: "/e", Enabled: true,
		Response: ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	for i := 0; i < defaultMaxVersionsPerMock+5; i++ {
		created.Response.StatusCode = 200 + i
		if _, err := s.Update(created); err != nil {
			t.Fatalf("Update %d: %v", i, err)
		}
	}

	versions, err := s.ListVersions(created.ID)
	if err != nil {
		t.Fatalf("ListVersions: %v", err)
	}
	if len(versions) != defaultMaxVersionsPerMock {
		t.Fatalf("expected a 0 limit to fall back to the default %d, got %d", defaultMaxVersionsPerMock, len(versions))
	}
}

func TestVersionHistory_restoreUnknownVersionNotFound(t *testing.T) {
	s := newTestStore(t)
	created, err := s.Create(&Definition{
		Name: "m4", ProtocolType: "rest", Method: "GET", PathPattern: "/d", Enabled: true,
		Response: ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.RestoreVersion(created.ID, "does-not-exist"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

// TestVersionHistory_enforceMaxVersionsForAllMocksTrimsExistingHistory
// covers "lowering the setting trims history that's already there," not
// just future edits: two mocks are each edited up to the default limit,
// then EnforceMaxVersionsForAllMocks(2) must immediately trim BOTH down to
// their 2 most recent versions.
func TestVersionHistory_enforceMaxVersionsForAllMocksTrimsExistingHistory(t *testing.T) {
	s := newTestStore(t)

	m6, err := s.Create(&Definition{
		Name: "m6", ProtocolType: "rest", Method: "GET", PathPattern: "/f", Enabled: true,
		Response: ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create m6: %v", err)
	}
	m7, err := s.Create(&Definition{
		Name: "m7", ProtocolType: "rest", Method: "GET", PathPattern: "/g", Enabled: true,
		Response: ResponseTemplate{StatusCode: 200},
	})
	if err != nil {
		t.Fatalf("Create m7: %v", err)
	}

	for _, m := range []*Definition{m6, m7} {
		for i := 0; i < 6; i++ {
			m.Response.StatusCode = 200 + i
			if _, err := s.Update(m); err != nil {
				t.Fatalf("Update %s %d: %v", m.Name, i, err)
			}
		}
	}

	if err := s.EnforceMaxVersionsForAllMocks(2); err != nil {
		t.Fatalf("EnforceMaxVersionsForAllMocks: %v", err)
	}

	for _, m := range []*Definition{m6, m7} {
		versions, err := s.ListVersions(m.ID)
		if err != nil {
			t.Fatalf("ListVersions(%s): %v", m.Name, err)
		}
		if len(versions) != 2 {
			t.Fatalf("expected %s trimmed to 2 versions, got %d", m.Name, len(versions))
		}
	}
}
