package tcpengine

import (
	"bufio"
	"io"
	"strings"
	"testing"

	"github.com/addictedabhi/airmock/internal/mock"
)

func TestTCPInteractionMatches(t *testing.T) {
	tests := []struct {
		name string
		it   mock.TCPInteraction
		line string
		want bool
	}{
		{"contains default matches substring", mock.TCPInteraction{Match: "LOGIN"}, "LOGIN admin", true},
		{"contains default rejects miss", mock.TCPInteraction{Match: "LOGIN"}, "PING", false},
		{"exact requires full match", mock.TCPInteraction{Match: "QUIT", MatchType: "exact"}, "QUIT", true},
		{"exact rejects partial", mock.TCPInteraction{Match: "QUIT", MatchType: "exact"}, "QUIT NOW", false},
		{"regex matches pattern", mock.TCPInteraction{Match: `^GET \d+$`, MatchType: "regex"}, "GET 42", true},
		{"regex rejects non-match", mock.TCPInteraction{Match: `^GET \d+$`, MatchType: "regex"}, "GET abc", false},
		{"invalid regex is a non-match, not a panic", mock.TCPInteraction{Match: `(`, MatchType: "regex"}, "(", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tcpInteractionMatches(tt.it, tt.line)
			if got != tt.want {
				t.Errorf("tcpInteractionMatches(%+v, %q) = %v, want %v", tt.it, tt.line, got, tt.want)
			}
		})
	}
}

func TestMatchTCPInteractionFirstMatchWins(t *testing.T) {
	cfg := &mock.TCPConfig{
		Interactions: []mock.TCPInteraction{
			{Match: "HELLO", Response: "first\n"},
			{Match: "HELLO", Response: "second\n"},
		},
		DefaultResponse: "default\n",
	}

	resp, closeAfter, _ := matchTCPInteraction(cfg, "HELLO world", "test-mock", nil)
	if resp != "first\n" {
		t.Fatalf("expected the first matching interaction to win, got %q", resp)
	}
	if closeAfter {
		t.Fatal("expected closeAfter=false")
	}
}

func TestMatchTCPInteractionFallsBackToDefault(t *testing.T) {
	cfg := &mock.TCPConfig{
		Interactions:    []mock.TCPInteraction{{Match: "HELLO", Response: "hi\n"}},
		DefaultResponse: "unknown command\n",
	}

	resp, _, _ := matchTCPInteraction(cfg, "nonsense", "test-mock", nil)
	if resp != "unknown command\n" {
		t.Fatalf("expected the default response, got %q", resp)
	}
}

func TestMatchTCPInteractionNoDefaultMeansNoResponse(t *testing.T) {
	cfg := &mock.TCPConfig{Interactions: []mock.TCPInteraction{{Match: "HELLO", Response: "hi\n"}}}

	resp, _, _ := matchTCPInteraction(cfg, "nonsense", "test-mock", nil)
	if resp != "" {
		t.Fatalf("expected no response when nothing matches and there's no default, got %q", resp)
	}
}

func TestMatchTCPInteractionRendersTemplateWithReceivedLine(t *testing.T) {
	cfg := &mock.TCPConfig{
		Interactions: []mock.TCPInteraction{
			{Match: "ECHO", Response: "you said: {{.Request.Body}}\n"},
		},
	}

	resp, _, _ := matchTCPInteraction(cfg, "ECHO hi there", "test-mock", nil)
	if resp != "you said: ECHO hi there\n" {
		t.Fatalf("expected the template to render with the received line, got %q", resp)
	}
}

func TestMatchTCPInteractionCloseAfter(t *testing.T) {
	cfg := &mock.TCPConfig{
		Interactions: []mock.TCPInteraction{{Match: "QUIT", MatchType: "exact", Response: "bye\n", CloseAfter: true}},
	}

	resp, closeAfter, _ := matchTCPInteraction(cfg, "QUIT", "test-mock", nil)
	if resp != "bye\n" || !closeAfter {
		t.Fatalf(`expected ("bye\n", true), got (%q, %v)`, resp, closeAfter)
	}
}

func TestEnsureLineEndingAppendsWhenAbsent(t *testing.T) {
	if got := ensureLineEnding("hello"); got != "hello\r\n" {
		t.Fatalf("expected CRLF appended, got %q", got)
	}
}

// TestEnsureLineEndingNormalizesRatherThanDoubling guards the exact bug a
// naive "append if not already present" check would reintroduce: text
// authored with a bare "\n" already baked in (a very common authoring
// habit) must end up with exactly ONE correct line ending, not
// "hello\n\r\n" — a plain suffix check against "\r\n" wouldn't have
// recognized the existing bare "\n" as already terminated.
func TestEnsureLineEndingNormalizesRatherThanDoubling(t *testing.T) {
	if got := ensureLineEnding("hello\n"); got != "hello\r\n" {
		t.Fatalf("expected exactly one line ending, got %q", got)
	}
	if got := ensureLineEnding("hello\r\n"); got != "hello\r\n" {
		t.Fatalf("expected exactly one line ending, got %q", got)
	}
}

// TestEnsureLineEndingIgnoresLineDelimiter is the regression test for the
// "###" scenario: a mock's own LineDelimiter is its wire-framing choice for
// INCOMING lines only (see readUntilDelimiter/trimDelimiter) — output
// always gets a real CRLF regardless, so a custom delimiter never
// accidentally suppresses the line break a human terminal needs.
func TestEnsureLineEndingIgnoresLineDelimiter(t *testing.T) {
	if got := ensureLineEnding("hello"); got != "hello\r\n" {
		t.Fatalf("expected CRLF regardless of any configured LineDelimiter, got %q", got)
	}
}

func TestEnsureLineEndingLeavesEmptyStringAlone(t *testing.T) {
	if got := ensureLineEnding(""); got != "" {
		t.Fatalf("expected an empty message to stay empty (no response at all), got %q", got)
	}
}

// TestParseLoginLineMatchesPlainTextFormat covers the whole point of
// LineFormat: an author writes the login command as it actually looks on
// the wire, using {username}/{password} placeholders, with no regex
// knowledge required — punctuation like the colons here is matched
// literally rather than as a regex metacharacter.
func TestParseLoginLineMatchesPlainTextFormat(t *testing.T) {
	username, password, matched := parseLoginLine("LOGIN:{username}:{password}", "LOGIN:admin:secret")
	if !matched {
		t.Fatalf("expected the format to match the line")
	}
	if username != "admin" || password != "secret" {
		t.Fatalf("expected admin/secret, got %q/%q", username, password)
	}
}

// TestParseLoginLineHonorsPlaceholderOrder covers {password} appearing
// before {username} in the format — the named capture groups must still
// land the right value in the right field regardless of which placeholder
// comes first in the template.
func TestParseLoginLineHonorsPlaceholderOrder(t *testing.T) {
	username, password, matched := parseLoginLine("AUTH pass={password} user={username}", "AUTH pass=secret user=admin")
	if !matched {
		t.Fatalf("expected the format to match the line")
	}
	if username != "admin" || password != "secret" {
		t.Fatalf("expected admin/secret, got %q/%q", username, password)
	}
}

// TestParseLoginLineDoesNotMatchWrongShape covers a line that simply
// doesn't fit the configured format — must report matched=false rather
// than panicking, so lineLoginAttempt can treat it like wrong credentials.
func TestParseLoginLineDoesNotMatchWrongShape(t *testing.T) {
	_, _, matched := parseLoginLine("LOGIN:{username}:{password}", "not a login line at all")
	if matched {
		t.Fatalf("expected no match for a line that doesn't fit the format")
	}
}

// TestDiscardBufferedLineNoiseDrainsTrailingCRLF covers the fix for a real
// report: a custom mid-line delimiter (";") stops readUntilDelimiter right
// at the ";", leaving a real telnet client's trailing "\r\n" (sent because
// the user pressed Enter, independent of whatever delimiter they also
// typed) sitting in the buffer — this must be drained so it doesn't get
// glued onto the front of the next line read. The whole input is fed
// through strings.NewReader in one shot so bufio.Reader's first ReadByte
// (inside readUntilDelimiter) fills its internal buffer with everything at
// once, exactly like a real client's single Enter-terminated write — a
// fresh bufio.Reader reports Buffered()==0 until something has actually
// forced a fill, so skipping that setup read would test a buffer state
// that never occurs in the real call path.
func TestDiscardBufferedLineNoiseDrainsTrailingCRLF(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("PING;\r\nHello;"))
	line, err := readUntilDelimiter(r, ";")
	if err != nil || line != "PING;" {
		t.Fatalf("setup: readUntilDelimiter returned (%q, %v)", line, err)
	}
	discardBufferedLineNoise(r, ";")
	rest, _ := io.ReadAll(r)
	if string(rest) != "Hello;" {
		t.Fatalf("expected the leading CRLF drained and \"Hello;\" left untouched, got %q", rest)
	}
}

// TestDiscardBufferedLineNoiseNeverBlocksOnUnbufferedData covers the other
// half of the fix: an automated client that sends nothing after its own
// delimiter (no stray CRLF at all) must never have its next real message
// mistaken for noise — discardBufferedLineNoise only inspects bytes
// bufio.Reader already reports as Buffered(), never bytes that would
// require blocking on a new network read to obtain. Here the buffer holds
// exactly "PING;" and nothing more, mirroring a scripted client's single
// clean write with no trailing noise at all.
func TestDiscardBufferedLineNoiseNeverBlocksOnUnbufferedData(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("PING;"))
	line, err := readUntilDelimiter(r, ";")
	if err != nil || line != "PING;" {
		t.Fatalf("setup: readUntilDelimiter returned (%q, %v)", line, err)
	}
	if buffered := r.Buffered(); buffered != 0 {
		t.Fatalf("test setup invalid: expected nothing buffered ahead, got %d bytes", buffered)
	}
	discardBufferedLineNoise(r, ";") // must be a no-op — nothing to peek without blocking
}

// TestDiscardBufferedLineNoiseSkipsNewlineDelimiters covers that a
// newline-based delimiter ("\n", "\r\n", "\r") never needs this drain —
// the client's own Enter keystroke IS the delimiter being matched in
// those cases, so there's no separate trailing noise to strip, and this
// must be a true no-op rather than eating the start of the next line.
func TestDiscardBufferedLineNoiseSkipsNewlineDelimiters(t *testing.T) {
	for _, delim := range []string{"\n", "\r\n", "\r"} {
		r := bufio.NewReader(strings.NewReader("PING" + delim + "\r\nHello" + delim))
		line, err := readUntilDelimiter(r, delim)
		if err != nil || line != "PING"+delim {
			t.Fatalf("delim %q: setup readUntilDelimiter returned (%q, %v)", delim, line, err)
		}
		discardBufferedLineNoise(r, delim)
		rest, _ := io.ReadAll(r)
		if string(rest) != "\r\nHello"+delim {
			t.Fatalf("delim %q: expected a no-op leaving \"\\r\\nHello%s\" untouched, got %q", delim, delim, rest)
		}
	}
}

// TestParseLoginLineRejectsMissingPlaceholders covers a misconfigured
// format — one written without {username}/{password} at all, or with a
// placeholder repeated — reporting matched=false rather than panicking,
// consistent with any other bad-format case.
func TestParseLoginLineRejectsMissingPlaceholders(t *testing.T) {
	cases := []string{
		"LOGIN:admin:secret",                        // no placeholders at all
		"LOGIN:{username}:{username}",                // {password} missing, {username} repeated
		"LOGIN:{username}",                           // {password} missing
	}
	for _, format := range cases {
		if _, _, matched := parseLoginLine(format, "LOGIN:admin:secret"); matched {
			t.Fatalf("expected format %q to be rejected as misconfigured", format)
		}
	}
}
