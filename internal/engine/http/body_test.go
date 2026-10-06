package httpengine

import (
	"bytes"
	"compress/gzip"
	"context"
	"io"
	"net/http"
	"strconv"
	"strings"
	"testing"

	"github.com/addictedabhi/airmock/internal/engine"
	"github.com/addictedabhi/airmock/internal/mock"
)

func startLenEchoMock(t *testing.T) string {
	t.Helper()
	e := New()
	e.RegisterMock(&mock.Definition{
		ID: "len-echo", ProtocolType: "rest", Method: http.MethodPost, PathPattern: "/len", Enabled: true,
		Response: mock.ResponseTemplate{StatusCode: 200, BodyTemplate: `{{ len .Request.Body }}`},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	addr := freeAddr(t)
	if err := e.Start(ctx, engine.ListenerConfig{Addr: addr}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return "http://" + addr + "/len"
}

func post(t *testing.T, url string, body []byte, headers map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	req.Header.Set("Content-Type", "text/plain")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("POST: %v", err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// A body bigger than the old 1 MiB cap used to be cut to 1,048,576 bytes
// without any sign of it.
func TestLargeRequestBodyIsNotSilentlyTruncated(t *testing.T) {
	url := startLenEchoMock(t)
	status, got := post(t, url, bytes.Repeat([]byte("a"), 3_000_000), nil)
	if status != 200 || got != "3000000" {
		t.Fatalf("expected the template to see all 3000000 bytes, got status %d body %q", status, got)
	}
}

func TestOversizedRequestBodyIsRejectedNotTruncated(t *testing.T) {
	url := startLenEchoMock(t)
	status, got := post(t, url, bytes.Repeat([]byte("a"), maxRequestBodyBytes+1), nil)
	if status != http.StatusRequestEntityTooLarge || !strings.Contains(got, strconv.Itoa(maxRequestBodyBytes>>20)) {
		t.Fatalf("expected 413 naming the limit, got %d %q", status, got)
	}
}

func gz(t *testing.T, data []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := gzip.NewWriter(&buf)
	zw.Write(data)
	zw.Close()
	return buf.Bytes()
}

func TestGzipRequestBodyIsDecodedBeforeMatchingAndTemplates(t *testing.T) {
	url := startLenEchoMock(t)
	plain := bytes.Repeat([]byte("hello "), 1000)
	status, got := post(t, url, gz(t, plain), map[string]string{"Content-Encoding": "gzip"})
	if status != 200 || got != strconv.Itoa(len(plain)) {
		t.Fatalf("expected the template to see the %d decoded bytes, got %d %q", len(plain), status, got)
	}
}

func TestInvalidGzipBodyIsABadRequestAndABombIsRefused(t *testing.T) {
	url := startLenEchoMock(t)
	if status, _ := post(t, url, []byte("not gzip at all"), map[string]string{"Content-Encoding": "gzip"}); status != http.StatusBadRequest {
		t.Fatalf("expected 400 for a body that is not valid gzip, got %d", status)
	}
	bomb := gz(t, bytes.Repeat([]byte("a"), maxRequestBodyBytes+10))
	if status, _ := post(t, url, bomb, map[string]string{"Content-Encoding": "gzip"}); status != http.StatusRequestEntityTooLarge {
		t.Fatalf("expected 413 when the decoded body exceeds the limit, got %d", status)
	}
}
