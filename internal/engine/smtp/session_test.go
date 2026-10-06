package smtpengine

import (
	"bufio"
	"strings"
	"testing"
)

// TestReadDataBlockRejectsOversizedInput guards against a real DoS: a
// client sending DATA and never terminating with "." (or just sending a
// huge message) previously grew readDataBlock's accumulator unbounded for
// the connection's whole lifetime. It must now stop with an error once
// maxDataBlockSize is exceeded, rather than continuing to accumulate.
func TestReadDataBlockRejectsOversizedInput(t *testing.T) {
	// One line just over the cap, never terminated by ".\r\n" — if the cap
	// weren't enforced this would read forever until EOF; expect a distinct
	// "exceeds" error long before that.
	oversized := strings.Repeat("a", maxDataBlockSize+1) + "\r\n"
	reader := bufio.NewReader(strings.NewReader(oversized))

	_, err := readDataBlock(reader)
	if err == nil {
		t.Fatal("expected an error for a DATA block over maxDataBlockSize, got nil")
	}
	if !strings.Contains(err.Error(), "exceeds") {
		t.Fatalf("expected an 'exceeds' error, got %v", err)
	}
}

func TestReadDataBlockAcceptsNormalInput(t *testing.T) {
	reader := bufio.NewReader(strings.NewReader("Subject: hi\r\n\r\nhello world\r\n.\r\n"))
	body, err := readDataBlock(reader)
	if err != nil {
		t.Fatalf("expected a small, well-formed DATA block to be accepted, got %v", err)
	}
	if !strings.Contains(body, "hello world") {
		t.Fatalf("expected the body to be captured, got %q", body)
	}
}
