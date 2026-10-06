package api

import (
	"errors"
	"io"
	"net/http"
)

// maxCSVUploadSize caps a single CSV attachment — generous for a lookup
// table meant to back template placeholders, small enough that one request
// can't be used to exhaust memory.
const maxCSVUploadSize = 5 << 20 // 5 MiB

// parseCSVUpload reads a multipart CSV upload's "file" part and "mode"
// field — shared by the mock-scoped and scheduled-event-scoped csv-source
// routes below, since the parsing is identical; only which store ends up
// receiving (mode, content) differs per owner kind.
func parseCSVUpload(r *http.Request) (mode, content string, err error) {
	if err := r.ParseMultipartForm(maxCSVUploadSize); err != nil {
		return "", "", err
	}
	mode = r.FormValue("mode")
	f, _, err := r.FormFile("file")
	if err != nil {
		return "", "", errors.New("a CSV file is required")
	}
	defer f.Close()
	data, err := io.ReadAll(f)
	if err != nil {
		return "", "", err
	}
	return mode, string(data), nil
}
