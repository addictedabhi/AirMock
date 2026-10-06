package mock

import (
	"path/filepath"
	"testing"

	"github.com/addictedabhi/airmock/internal/storage"
)

func TestCounterIncrementsByDefaultStepOfOne(t *testing.T) {
	s := newTestStore(t)
	for i, want := range []int64{1, 2, 3} {
		got, err := s.Counter("mock-1", "orderId", 1)
		if err != nil {
			t.Fatalf("Counter call %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("Counter call %d = %d, want %d", i, got, want)
		}
	}
}

func TestCounterDecrementsWithNegativeStep(t *testing.T) {
	s := newTestStore(t)
	for i, want := range []int64{-1, -2, -3} {
		got, err := s.Counter("mock-1", "stock", -1)
		if err != nil {
			t.Fatalf("Counter call %d: %v", i, err)
		}
		if got != want {
			t.Fatalf("Counter call %d = %d, want %d", i, got, want)
		}
	}
}

// TestCounterScopedByOwnerAndName guards two mocks (or a mock and a
// differently-named counter) from ever seeing each other's values.
func TestCounterScopedByOwnerAndName(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.Counter("mock-1", "x", 1); err != nil {
		t.Fatalf("Counter: %v", err)
	}
	got, err := s.Counter("mock-2", "x", 1)
	if err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected mock-2's own counter to start fresh at 1, got %d", got)
	}
	got, err = s.Counter("mock-1", "y", 1)
	if err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if got != 1 {
		t.Fatalf("expected a differently-named counter on the same mock to start fresh at 1, got %d", got)
	}
}

// TestCounterPersistsAcrossRestart simulates a server restart: closing and
// reopening a *Store against the same on-disk database file must NOT reset
// the counter, since the user explicitly asked for counters to survive a
// restart rather than being in-memory only.
func TestCounterPersistsAcrossRestart(t *testing.T) {
	dbPath := filepath.Join(t.TempDir(), "test.db")

	db1, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	s1 := NewStore(db1)
	for i := 0; i < 3; i++ {
		if _, err := s1.Counter("mock-1", "orderId", 1); err != nil {
			t.Fatalf("Counter: %v", err)
		}
	}
	if err := db1.Close(); err != nil {
		t.Fatalf("close db1: %v", err)
	}

	db2, err := storage.Open(dbPath)
	if err != nil {
		t.Fatalf("storage.Open (reopen): %v", err)
	}
	t.Cleanup(func() { db2.Close() })
	s2 := NewStore(db2)
	got, err := s2.Counter("mock-1", "orderId", 1)
	if err != nil {
		t.Fatalf("Counter (after reopen): %v", err)
	}
	if got != 4 {
		t.Fatalf("expected the counter to continue from 4 after a simulated restart, got %d", got)
	}
}

const testCSV = "name,email\nAlice,alice@example.com\nBob,bob@example.com\nCarol,carol@example.com\n"

func TestSetCSVSourceRejectsEmptyCSV(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetCSVSource("mock-1", "round_robin", "name,email\n"); err != ErrEmptyCSV {
		t.Fatalf("expected ErrEmptyCSV for a header-only CSV, got %v", err)
	}
}

func TestSetCSVSourceRejectsInvalidMode(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetCSVSource("mock-1", "bogus", testCSV); err != ErrInvalidCSVMode {
		t.Fatalf("expected ErrInvalidCSVMode, got %v", err)
	}
}

func TestNextCSVRowRoundRobinCyclesAndWraps(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetCSVSource("mock-1", "round_robin", testCSV); err != nil {
		t.Fatalf("SetCSVSource: %v", err)
	}
	want := []string{"Alice", "Bob", "Carol", "Alice", "Bob"}
	for i, wantName := range want {
		row, ok, err := s.NextCSVRow("mock-1")
		if err != nil {
			t.Fatalf("NextCSVRow call %d: %v", i, err)
		}
		if !ok {
			t.Fatalf("NextCSVRow call %d: expected ok=true", i)
		}
		if row["name"] != wantName {
			t.Fatalf("NextCSVRow call %d: got name %q, want %q", i, row["name"], wantName)
		}
	}
}

func TestNextCSVRowRandomStaysInBounds(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetCSVSource("mock-1", "random", testCSV); err != nil {
		t.Fatalf("SetCSVSource: %v", err)
	}
	valid := map[string]bool{"Alice": true, "Bob": true, "Carol": true}
	for i := 0; i < 20; i++ {
		row, ok, err := s.NextCSVRow("mock-1")
		if err != nil {
			t.Fatalf("NextCSVRow call %d: %v", i, err)
		}
		if !ok || !valid[row["name"]] {
			t.Fatalf("NextCSVRow call %d: got unexpected row %+v", i, row)
		}
	}
}

func TestNextCSVRowNoAttachmentReturnsNotOK(t *testing.T) {
	s := newTestStore(t)
	_, ok, err := s.NextCSVRow("mock-with-no-csv")
	if err != nil {
		t.Fatalf("NextCSVRow: %v", err)
	}
	if ok {
		t.Fatal("expected ok=false when no CSV is attached")
	}
}

func TestSetCSVSourceReplaceResetsCursor(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetCSVSource("mock-1", "round_robin", testCSV); err != nil {
		t.Fatalf("SetCSVSource: %v", err)
	}
	// Advance the cursor partway through.
	if _, _, err := s.NextCSVRow("mock-1"); err != nil {
		t.Fatalf("NextCSVRow: %v", err)
	}
	if _, _, err := s.NextCSVRow("mock-1"); err != nil {
		t.Fatalf("NextCSVRow: %v", err)
	}
	// Re-attaching (even the same content) should restart from row 0.
	if _, err := s.SetCSVSource("mock-1", "round_robin", testCSV); err != nil {
		t.Fatalf("SetCSVSource (replace): %v", err)
	}
	row, ok, err := s.NextCSVRow("mock-1")
	if err != nil {
		t.Fatalf("NextCSVRow: %v", err)
	}
	if !ok || row["name"] != "Alice" {
		t.Fatalf("expected the cursor to restart at row 0 (Alice) after replacing the CSV, got %+v", row)
	}
}

func TestGetCSVSourceMetaReportsRowCountAndColumns(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetCSVSource("mock-1", "round_robin", testCSV); err != nil {
		t.Fatalf("SetCSVSource: %v", err)
	}
	meta, err := s.GetCSVSourceMeta("mock-1")
	if err != nil {
		t.Fatalf("GetCSVSourceMeta: %v", err)
	}
	if meta.Mode != "round_robin" || meta.RowCount != 3 || len(meta.Columns) != 2 {
		t.Fatalf("unexpected meta: %+v", meta)
	}
}

func TestGetCSVSourceMetaNotFound(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.GetCSVSourceMeta("mock-1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
}

func TestDeleteCSVSource(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetCSVSource("mock-1", "round_robin", testCSV); err != nil {
		t.Fatalf("SetCSVSource: %v", err)
	}
	if err := s.DeleteCSVSource("mock-1"); err != nil {
		t.Fatalf("DeleteCSVSource: %v", err)
	}
	if _, err := s.GetCSVSourceMeta("mock-1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound after delete, got %v", err)
	}
}

func TestDeleteCSVSourceNotFound(t *testing.T) {
	s := newTestStore(t)
	if err := s.DeleteCSVSource("mock-1"); err != ErrNotFound {
		t.Fatalf("expected ErrNotFound deleting a non-existent attachment, got %v", err)
	}
}

// TestCSVFunctionReusesSameRowWithinOneRender guards the "one row per
// render, not per csv() call" requirement — {{csv "name"}} and
// {{csv "email"}} in the same template must refer to the SAME record.
func TestCSVFunctionReusesSameRowWithinOneRender(t *testing.T) {
	s := newTestStore(t)
	if _, err := s.SetCSVSource("mock-1", "round_robin", testCSV); err != nil {
		t.Fatalf("SetCSVSource: %v", err)
	}
	got, err := RenderBody(`{{csv "name"}} <{{csv "email"}}>`, RequestContext{}, RenderOptions{OwnerID: "mock-1", DynamicValues: s})
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	if got != "Alice <alice@example.com>" {
		t.Fatalf("expected both csv() calls to resolve to the same row, got %q", got)
	}
}

func TestCounterFunctionInTemplate(t *testing.T) {
	s := newTestStore(t)
	got, err := RenderBody(`{{counter "orderId"}}-{{counter "orderId"}}`, RequestContext{}, RenderOptions{OwnerID: "mock-1", DynamicValues: s})
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	if got != "1-2" {
		t.Fatalf("expected sequential counter values across two calls in one template, got %q", got)
	}
}

func TestCounterFunctionWithExplicitStep(t *testing.T) {
	s := newTestStore(t)
	got, err := RenderBody(`{{counter "stock" -1}}`, RequestContext{}, RenderOptions{OwnerID: "mock-1", DynamicValues: s})
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	if got != "-1" {
		t.Fatalf("expected a negative step to decrement, got %q", got)
	}
}

func TestCounterFunctionErrorsWithoutDynamicValueSource(t *testing.T) {
	_, err := RenderBody(`{{counter "orderId"}}`, RequestContext{}, RenderOptions{})
	if err == nil {
		t.Fatal("expected an error when no DynamicValueSource is configured")
	}
}

func TestFakeFunctionUnknownKindReturnsEmptyString(t *testing.T) {
	got, err := RenderBody(`[{{fake "not-a-real-kind"}}]`, RequestContext{}, RenderOptions{})
	if err != nil {
		t.Fatalf("RenderBody: %v", err)
	}
	if got != "[]" {
		t.Fatalf("expected an unknown fake kind to render as empty, got %q", got)
	}
}

func TestFakeFunctionNumberRespectsRange(t *testing.T) {
	for i := 0; i < 20; i++ {
		got, err := RenderBody(`{{fake "number" "5" "5"}}`, RequestContext{}, RenderOptions{})
		if err != nil {
			t.Fatalf("RenderBody: %v", err)
		}
		if got != "5" {
			t.Fatalf("expected fake \"number\" 5 5 to always render 5, got %q", got)
		}
	}
}

// TestDeleteMockCleansUpItsCounterAndCSVRows guards a real gap: owner_id
// has no FK/CASCADE tying dynamic_counters/dynamic_csv_sources rows to the
// mocks table, so without Store.Delete's own explicit cleanup, a deleted
// mock's counters and (potentially large) CSV content would sit orphaned
// in the database forever, keyed by a UUID nothing can ever reach again.
func TestDeleteMockCleansUpItsCounterAndCSVRows(t *testing.T) {
	s := newTestStore(t)
	m, err := s.Create(&Definition{Name: "cleanup-target", Method: "GET", PathPattern: "/cleanup-target", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Counter(m.ID, "hits", 1); err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if _, err := s.SetCSVSource(m.ID, "round_robin", testCSV); err != nil {
		t.Fatalf("SetCSVSource: %v", err)
	}

	if err := s.Delete(m.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	var counterRows, csvRows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dynamic_counters WHERE owner_id = ?`, m.ID).Scan(&counterRows); err != nil {
		t.Fatalf("count dynamic_counters: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dynamic_csv_sources WHERE owner_id = ?`, m.ID).Scan(&csvRows); err != nil {
		t.Fatalf("count dynamic_csv_sources: %v", err)
	}
	if counterRows != 0 || csvRows != 0 {
		t.Fatalf("expected the deleted mock's counter/csv rows to be cleaned up, got %d counter rows and %d csv rows", counterRows, csvRows)
	}
}

// TestDeleteProjectCleansUpItsMocksCounterAndCSVRows is the same guard for
// the project-cascade-delete path, which removes its mocks via a different
// SQL statement than the single-mock Store.Delete above.
func TestDeleteProjectCleansUpItsMocksCounterAndCSVRows(t *testing.T) {
	s := newTestStore(t)
	p, err := s.CreateProject(&Project{Name: "cleanup-project"})
	if err != nil {
		t.Fatalf("CreateProject: %v", err)
	}
	m, err := s.Create(&Definition{Name: "cleanup-project-mock", Method: "GET", PathPattern: "/cleanup-project-mock", Enabled: true, ProjectID: p.ID})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	if _, err := s.Counter(m.ID, "hits", 1); err != nil {
		t.Fatalf("Counter: %v", err)
	}
	if _, err := s.SetCSVSource(m.ID, "round_robin", testCSV); err != nil {
		t.Fatalf("SetCSVSource: %v", err)
	}

	if err := s.DeleteProject(p.ID); err != nil {
		t.Fatalf("DeleteProject: %v", err)
	}

	var counterRows, csvRows int
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dynamic_counters WHERE owner_id = ?`, m.ID).Scan(&counterRows); err != nil {
		t.Fatalf("count dynamic_counters: %v", err)
	}
	if err := s.db.QueryRow(`SELECT COUNT(*) FROM dynamic_csv_sources WHERE owner_id = ?`, m.ID).Scan(&csvRows); err != nil {
		t.Fatalf("count dynamic_csv_sources: %v", err)
	}
	if counterRows != 0 || csvRows != 0 {
		t.Fatalf("expected the deleted project's mock's counter/csv rows to be cleaned up, got %d counter rows and %d csv rows", counterRows, csvRows)
	}
}
