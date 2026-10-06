package smtp

import (
	"bufio"
	"encoding/base64"
	"fmt"
	"net"
	"strconv"
	"strings"
	"testing"
)

func TestBuildMessageStripsCRLFFromHeaderInjection(t *testing.T) {
	// A recipient/subject can originate from a rendered template fed by
	// the very request that triggered the callback — an attacker-supplied
	// "victim@example.com\r\nBcc: attacker@evil.com" must not be able to
	// smuggle a second header into the raw message.
	msg := string(buildMessage("", "mock@example.com", "victim@example.com\r\nBcc: attacker@evil.com", "Hello\r\nX-Injected: true", "<p>body</p>"))

	// The injected "Bcc:"/"X-Injected:" text must survive only as inert
	// content folded into the existing To:/Subject: line — never as its
	// OWN header line, which is what would actually let it be interpreted
	// as a real extra header by a mail parser.
	for _, line := range strings.Split(msg, "\r\n") {
		if strings.HasPrefix(line, "Bcc:") {
			t.Fatalf("expected no standalone Bcc: header line, got message:\n%s", msg)
		}
		if strings.HasPrefix(line, "X-Injected:") {
			t.Fatalf("expected no standalone X-Injected: header line, got message:\n%s", msg)
		}
	}
	if !strings.Contains(msg, "To: victim@example.com Bcc: attacker@evil.com\r\n") {
		t.Fatalf("expected the injected text folded into the To: line as inert content, got message:\n%s", msg)
	}
	if !strings.Contains(msg, "Content-Type: text/html") {
		t.Fatalf("expected an HTML content type, got message:\n%s", msg)
	}
}

func TestFormatFromIncludesPersonalNameWhenSet(t *testing.T) {
	got := formatFrom("AirMock Notifications", "mock@example.com")
	want := `"AirMock Notifications" <mock@example.com>`
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestFormatFromFallsBackToBareAddressWhenNameEmpty(t *testing.T) {
	got := formatFrom("", "mock@example.com")
	if got != "mock@example.com" {
		t.Fatalf("got %q, want bare address", got)
	}
}

func TestFormatFromEscapesEmbeddedQuotesAndStripsCRLF(t *testing.T) {
	got := formatFrom(`Evil"\r\nBcc:attacker@evil.com`, "mock@example.com")
	if strings.Contains(got, "\r") || strings.Contains(got, "\n") {
		t.Fatalf("expected CRLF stripped from the display name, got %q", got)
	}
	// The literal double-quote must be backslash-escaped, not left to
	// terminate the quoted-string early.
	if !strings.Contains(got, `\"`) {
		t.Fatalf(`expected the embedded " to be escaped as \", got %q`, got)
	}
}

// fakeSMTPServer speaks just enough SMTP to exercise Send end-to-end
// (EHLO/MAIL/RCPT/DATA/QUIT) without advertising STARTTLS or AUTH, so a
// Settings with UseTLS=false and no credentials completes a real
// conversation over a real TCP connection — not a mocked-out client.
func fakeSMTPServer(t *testing.T) (addr string, capturedData chan string) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })
	captured := make(chan string, 1)

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		fmt.Fprintf(conn, "220 fake.smtp ESMTP\r\n")

		inData := false
		var data strings.Builder
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case inData:
				if strings.TrimRight(line, "\r\n") == "." {
					inData = false
					captured <- data.String()
					fmt.Fprintf(conn, "250 OK\r\n")
					continue
				}
				data.WriteString(line)
			case strings.HasPrefix(strings.ToUpper(line), "EHLO"):
				fmt.Fprintf(conn, "250 fake.smtp\r\n")
			case strings.HasPrefix(strings.ToUpper(line), "MAIL FROM"):
				fmt.Fprintf(conn, "250 OK\r\n")
			case strings.HasPrefix(strings.ToUpper(line), "RCPT TO"):
				fmt.Fprintf(conn, "250 OK\r\n")
			case strings.HasPrefix(strings.ToUpper(line), "DATA"):
				inData = true
				fmt.Fprintf(conn, "354 Go ahead\r\n")
			case strings.HasPrefix(strings.ToUpper(line), "QUIT"):
				fmt.Fprintf(conn, "221 Bye\r\n")
				return
			default:
				fmt.Fprintf(conn, "250 OK\r\n")
			}
		}
	}()

	return ln.Addr().String(), captured
}

func TestSendDeliversAgainstAFakeSMTPServer(t *testing.T) {
	addr, captured := fakeSMTPServer(t)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	cfg := Settings{Host: host, Port: port, FromAddress: "mock@example.com", UseTLS: false}
	if err := Send(cfg, "to@example.com", "Order shipped", "<p>Your order is on its way.</p>"); err != nil {
		t.Fatalf("Send: %v", err)
	}

	select {
	case data := <-captured:
		if !strings.Contains(data, "Subject: Order shipped") {
			t.Fatalf("expected the subject in the sent message, got:\n%s", data)
		}
		if !strings.Contains(data, "<p>Your order is on its way.</p>") {
			t.Fatalf("expected the HTML body in the sent message, got:\n%s", data)
		}
	default:
		t.Fatal("expected the fake server to have captured a DATA payload")
	}
}

func TestSendFailsWhenUnconfigured(t *testing.T) {
	if err := Send(Settings{}, "to@example.com", "subj", "body"); err == nil {
		t.Fatal("expected an error for an unconfigured relay")
	}
}

// TestSendWithInlineImageDeliversAgainstAFakeSMTPServer guards the
// multipart/related path end to end — the actual reason for it existing
// at all is a real mail client rendering the embedded image, which this
// can't observe directly, but it can and does verify the wire format a
// real client would need: a multipart/related Content-Type with a
// matching boundary, an HTML part referencing cid:<the same ContentID>,
// and an image part carrying that exact ContentID plus base64-encoded
// bytes that decode back to the original image.
func TestSendWithInlineImageDeliversAgainstAFakeSMTPServer(t *testing.T) {
	addr, captured := fakeSMTPServer(t)
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}

	cfg := Settings{Host: host, Port: port, FromAddress: "mock@example.com", UseTLS: false}
	imageBytes := []byte("not a real png, just test bytes")
	err = SendWithInlineImage(cfg, "to@example.com", "AirMock test email", `<img src="cid:airmock-logo">`, InlineImage{
		ContentID:   "airmock-logo",
		Bytes:       imageBytes,
		ContentType: "image/png",
	})
	if err != nil {
		t.Fatalf("SendWithInlineImage: %v", err)
	}

	select {
	case data := <-captured:
		if !strings.Contains(data, "Content-Type: multipart/related;") {
			t.Fatalf("expected a multipart/related content type, got:\n%s", data)
		}
		if !strings.Contains(data, `Content-Type: text/html; charset="UTF-8"`) {
			t.Fatalf("expected an HTML part, got:\n%s", data)
		}
		if !strings.Contains(data, `cid:airmock-logo`) {
			t.Fatalf("expected the HTML body's cid: reference to survive, got:\n%s", data)
		}
		// "Content-Id", not "Content-ID" — Go's textproto header
		// canonicalization title-cases each hyphen-separated segment
		// rather than preserving "ID" as an acronym; harmless for a real
		// mail parser (header names are case-insensitive per RFC 2822),
		// but the exact casing this test checks for has to match it.
		if !strings.Contains(data, "Content-Id: <airmock-logo>") {
			t.Fatalf("expected a matching Content-Id on the image part, got:\n%s", data)
		}
		if !strings.Contains(data, "Content-Type: image/png") {
			t.Fatalf("expected the image part's own content type, got:\n%s", data)
		}
		wantEncoded := base64.StdEncoding.EncodeToString(imageBytes)
		gotUnwrapped := strings.ReplaceAll(strings.ReplaceAll(data, "\r\n", ""), "\n", "")
		if !strings.Contains(gotUnwrapped, wantEncoded) {
			t.Fatalf("expected the base64-encoded image bytes present (line-wrapping aside), got:\n%s", data)
		}
	default:
		t.Fatal("expected the fake server to have captured a DATA payload")
	}
}

func TestSendWithInlineImageFailsWhenUnconfigured(t *testing.T) {
	err := SendWithInlineImage(Settings{}, "to@example.com", "subj", "body", InlineImage{ContentID: "x", Bytes: []byte("x"), ContentType: "image/png"})
	if err == nil {
		t.Fatal("expected an error for an unconfigured relay")
	}
}
