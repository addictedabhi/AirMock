package httpengine

import (
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// maxRequestBodyBytes bounds how much of a request body the gateway reads
// for matching, validation and templates. A body over this is refused with
// 413 rather than silently cut short (the old 1 MiB cap handed templates a
// truncated body with no sign of it).
const maxRequestBodyBytes = 10 << 20 // 10 MiB

var errBodyTooLarge = fmt.Errorf("request body exceeds the %d MB limit AirMock reads for matching and templates", maxRequestBodyBytes>>20)

// readRequestBody returns the request body as the mock logic should see it
// (gzip-decoded when Content-Encoding says so) together with the raw bytes
// as received, which a proxy mock must forward untouched. On failure it has
// already written the error response and ok is false.
func readRequestBody(w http.ResponseWriter, r *http.Request) (decoded, raw []byte, ok bool) {
	defer r.Body.Close()
	raw, err := readCapped(r.Body)
	if err != nil {
		writeBodyError(w, err)
		return nil, nil, false
	}
	if !strings.EqualFold(strings.TrimSpace(r.Header.Get("Content-Encoding")), "gzip") || len(raw) == 0 {
		return raw, raw, true
	}
	zr, err := gzip.NewReader(strings.NewReader(string(raw)))
	if err != nil {
		http.Error(w, "request has Content-Encoding: gzip but the body is not valid gzip: "+err.Error(), http.StatusBadRequest)
		return nil, nil, false
	}
	defer zr.Close()
	decoded, err = readCapped(zr) // the same cap applies after decoding, so a small gzip cannot expand without bound
	if err != nil {
		writeBodyError(w, err)
		return nil, nil, false
	}
	return decoded, raw, true
}

func readCapped(src io.Reader) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(src, maxRequestBodyBytes+1))
	if err != nil {
		return nil, err
	}
	if len(b) > maxRequestBodyBytes {
		return nil, errBodyTooLarge
	}
	return b, nil
}

func writeBodyError(w http.ResponseWriter, err error) {
	if errors.Is(err, errBodyTooLarge) {
		http.Error(w, err.Error(), http.StatusRequestEntityTooLarge)
		return
	}
	http.Error(w, "reading request body: "+err.Error(), http.StatusBadRequest)
}
