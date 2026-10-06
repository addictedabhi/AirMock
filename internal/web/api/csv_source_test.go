package api

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/mock"
)

const testCSVUploadContent = "name,email\nAlice,alice@example.com\nBob,bob@example.com\n"

// doCSVUpload builds and sends a multipart csv-source request — the "file"
// part carries csvContent, the "mode" field carries mode.
func doCSVUpload(t *testing.T, r chi.Router, method, path, mode, csvContent string) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if mode != "" {
		if err := mw.WriteField("mode", mode); err != nil {
			t.Fatalf("write mode field: %v", err)
		}
	}
	if csvContent != "" {
		fw, err := mw.CreateFormFile("file", "data.csv")
		if err != nil {
			t.Fatalf("create form file: %v", err)
		}
		if _, err := fw.Write([]byte(csvContent)); err != nil {
			t.Fatalf("write csv content: %v", err)
		}
	}
	if err := mw.Close(); err != nil {
		t.Fatalf("close multipart writer: %v", err)
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, req)
	return rec
}

func TestMockCSVSourceUploadGetDelete(t *testing.T) {
	r, store := newTestMocksRouter(t)
	m, err := store.Create(&mock.Definition{Name: "m1", Method: "GET", PathPattern: "/m1", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}

	rec := doCSVUpload(t, r, "PUT", "/api/mocks/"+m.ID+"/csv-source", "round_robin", testCSVUploadContent)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 uploading a CSV, got %d: %s", rec.Code, rec.Body.String())
	}
	var meta struct {
		Mode     string   `json:"mode"`
		RowCount int      `json:"rowCount"`
		Columns  []string `json:"columns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta.Mode != "round_robin" || meta.RowCount != 2 || len(meta.Columns) != 2 {
		t.Fatalf("unexpected meta: %+v", meta)
	}

	rec = doJSON(t, r, "GET", "/api/mocks/"+m.ID+"/csv-source", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200 getting csv-source metadata, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, r, "DELETE", "/api/mocks/"+m.ID+"/csv-source", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 deleting csv-source, got %d: %s", rec.Code, rec.Body.String())
	}

	rec = doJSON(t, r, "GET", "/api/mocks/"+m.ID+"/csv-source", nil)
	if rec.Code != http.StatusNoContent {
		t.Fatalf("expected 204 (nothing attached) after delete, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMockCSVSourceRejectsEmptyCSV(t *testing.T) {
	r, store := newTestMocksRouter(t)
	m, err := store.Create(&mock.Definition{Name: "m1", Method: "GET", PathPattern: "/m1", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec := doCSVUpload(t, r, "PUT", "/api/mocks/"+m.ID+"/csv-source", "round_robin", "name,email\n")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a header-only CSV, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMockCSVSourceRejectsMissingFile(t *testing.T) {
	r, store := newTestMocksRouter(t)
	m, err := store.Create(&mock.Definition{Name: "m1", Method: "GET", PathPattern: "/m1", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec := doCSVUpload(t, r, "PUT", "/api/mocks/"+m.ID+"/csv-source", "round_robin", "")
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 with no file part, got %d: %s", rec.Code, rec.Body.String())
	}
}

// "No CSV attached" is the normal state of most mocks, and the edit form asks
// every time, so it is a 204 rather than a 404 (which browsers log as a failed
// request); only a mock that does not exist is a 404.
func TestMockCSVSourceGetIsNoContentWhenNothingIsAttachedAndNotFoundForAMissingMock(t *testing.T) {
	r, store := newTestMocksRouter(t)
	m, err := store.Create(&mock.Definition{Name: "m1", Method: "GET", PathPattern: "/m1", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec := doJSON(t, r, "GET", "/api/mocks/"+m.ID+"/csv-source", nil)
	if rec.Code != http.StatusNoContent || rec.Body.Len() != 0 {
		t.Fatalf("expected an empty 204 when no CSV is attached, got %d: %s", rec.Code, rec.Body.String())
	}
	rec = doJSON(t, r, "GET", "/api/mocks/does-not-exist/csv-source", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404 for a mock that does not exist, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestMockCSVSourceReplaceUpdatesMeta(t *testing.T) {
	r, store := newTestMocksRouter(t)
	m, err := store.Create(&mock.Definition{Name: "m1", Method: "GET", PathPattern: "/m1", Enabled: true})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	rec := doCSVUpload(t, r, "PUT", "/api/mocks/"+m.ID+"/csv-source", "round_robin", testCSVUploadContent)
	if rec.Code != http.StatusOK {
		t.Fatalf("first upload: expected 200, got %d", rec.Code)
	}
	rec = doCSVUpload(t, r, "PUT", "/api/mocks/"+m.ID+"/csv-source", "random", "a,b,c\n1,2,3\n")
	if rec.Code != http.StatusOK {
		t.Fatalf("replace upload: expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var meta struct {
		Mode     string   `json:"mode"`
		RowCount int      `json:"rowCount"`
		Columns  []string `json:"columns"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &meta); err != nil {
		t.Fatalf("decode meta: %v", err)
	}
	if meta.Mode != "random" || meta.RowCount != 1 || len(meta.Columns) != 3 {
		t.Fatalf("expected the replacement's own metadata, got %+v", meta)
	}
}
