package tcpengine

import (
	"bufio"
	"strings"
	"testing"
)

func TestReadUntilDelimiterSupportsMultiByteDelimiter(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("HELLO\r\nWORLD\r\n"))

	line1, err := readUntilDelimiter(r, "\r\n")
	if err != nil || line1 != "HELLO\r\n" {
		t.Fatalf("expected %q, got %q (err=%v)", "HELLO\r\n", line1, err)
	}

	line2, err := readUntilDelimiter(r, "\r\n")
	if err != nil || line2 != "WORLD\r\n" {
		t.Fatalf("expected %q, got %q (err=%v)", "WORLD\r\n", line2, err)
	}
}

func TestTrimDelimiterCRLF(t *testing.T) {
	got := trimDelimiter("HELLO\r\n", "\r\n")
	if got != "HELLO" {
		t.Fatalf("expected %q, got %q", "HELLO", got)
	}
}

func TestTrimDelimiterLFOnlyAlsoStripsTrailingCR(t *testing.T) {
	// A client sending CRLF against an LF-configured mock should still
	// match cleanly, not leave a stray \r in the matched line.
	got := trimDelimiter("HELLO\r\n", "\n")
	if got != "HELLO" {
		t.Fatalf("expected %q, got %q", "HELLO", got)
	}
}

func TestTrimDelimiterCustomMultiCharDelimiter(t *testing.T) {
	got := trimDelimiter("PING###", "###")
	if got != "PING" {
		t.Fatalf("expected %q, got %q", "PING", got)
	}
}
