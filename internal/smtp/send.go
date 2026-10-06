package smtp

import (
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"mime/multipart"
	"net/smtp"
	"net/textproto"
	"strings"
)

// Send delivers one HTML email through the configured relay. Built on
// net/smtp directly (rather than the package-level smtp.SendMail helper)
// so UseTLS is an explicit choice — useful against a local dev SMTP
// catcher (e.g. MailHog/Mailpit) that doesn't offer STARTTLS at all,
// where SendMail's own opportunistic-STARTTLS-if-advertised behavior
// isn't controllable.
func Send(cfg Settings, to, subject, htmlBody string) error {
	if to == "" {
		return errors.New("no recipient email address resolved for this callback")
	}
	return deliver(cfg, to, buildMessage(cfg.FromName, cfg.FromAddress, to, subject, htmlBody))
}

// InlineImage is one image embedded in an HTML email body via a
// cid:ContentID reference — see SendWithInlineImage.
type InlineImage struct {
	ContentID   string // referenced in htmlBody as <img src="cid:ContentID">
	Bytes       []byte
	ContentType string // e.g. "image/png"
}

// SendWithInlineImage is Send's counterpart for an HTML body that
// references a logo/icon via <img src="cid:ContentID">, the standard way
// an image actually renders reliably across real mail clients — a data:
// URI or a remote-hosted <img src> both fail in large swaths of real
// clients (Outlook desktop especially), where a CID-embedded inline image
// in a multipart/related message just works. Used only by the SMTP
// settings page's "Send test email" (see internal/web/api/smtp.go), which
// has an actual logo to embed; every other caller (Send above, used by the
// async mock email callback) has no logo and stays on the simpler,
// single-part message.
func SendWithInlineImage(cfg Settings, to, subject, htmlBody string, img InlineImage) error {
	if to == "" {
		return errors.New("no recipient email address resolved")
	}
	msg, err := buildMultipartRelatedMessage(cfg.FromName, cfg.FromAddress, to, subject, htmlBody, img)
	if err != nil {
		return fmt.Errorf("build multipart message: %w", err)
	}
	return deliver(cfg, to, msg)
}

// deliver runs the actual SMTP protocol exchange shared by Send and
// SendWithInlineImage — everything except how the raw message body itself
// was assembled.
func deliver(cfg Settings, to string, rawMessage []byte) error {
	if !cfg.Configured() {
		return errors.New("SMTP is not configured — set a relay host in Settings")
	}

	addr := fmt.Sprintf("%s:%d", cfg.Host, cfg.Port)
	c, err := smtp.Dial(addr)
	if err != nil {
		return fmt.Errorf("dial smtp server: %w", err)
	}
	defer c.Close()

	if err := c.Hello("localhost"); err != nil {
		return fmt.Errorf("smtp HELO: %w", err)
	}

	if cfg.UseTLS {
		if ok, _ := c.Extension("STARTTLS"); ok {
			if err := c.StartTLS(&tls.Config{ServerName: cfg.Host}); err != nil {
				return fmt.Errorf("smtp STARTTLS: %w", err)
			}
		}
	}

	if cfg.Username != "" {
		if ok, _ := c.Extension("AUTH"); ok {
			auth := smtp.PlainAuth("", cfg.Username, cfg.Password, cfg.Host)
			if err := c.Auth(auth); err != nil {
				return fmt.Errorf("smtp AUTH: %w", err)
			}
		}
	}

	if err := c.Mail(cfg.FromAddress); err != nil {
		return fmt.Errorf("smtp MAIL FROM: %w", err)
	}
	if err := c.Rcpt(to); err != nil {
		return fmt.Errorf("smtp RCPT TO: %w", err)
	}
	w, err := c.Data()
	if err != nil {
		return fmt.Errorf("smtp DATA: %w", err)
	}
	if _, err := w.Write(rawMessage); err != nil {
		return fmt.Errorf("write smtp message: %w", err)
	}
	if err := w.Close(); err != nil {
		return fmt.Errorf("close smtp message: %w", err)
	}
	return c.Quit()
}

// buildMessage assembles a minimal HTML email. fromName/fromAddress/to/
// subject all pass through stripCRLF first: to/subject can originate from
// a rendered template fed by the very request that triggered the callback
// (an async mock's email recipient is commonly extracted from the request
// itself, the same way an HTTP callback's target URL can be), so an
// attacker-controlled "\r\nBcc: someone@else.com" would otherwise inject
// arbitrary extra headers into the raw message — classic SMTP header
// injection. fromName/fromAddress come from operator-entered settings
// rather than a request, but are stripped too for defense in depth.
func buildMessage(fromName, fromAddress, to, subject, htmlBody string) []byte {
	to, subject = stripCRLF(to), stripCRLF(subject)
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", formatFrom(fromName, fromAddress))
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")
	b.WriteString("Content-Type: text/html; charset=\"UTF-8\"\r\n")
	b.WriteString("\r\n")
	b.WriteString(htmlBody)
	return []byte(b.String())
}

// buildMultipartRelatedMessage is buildMessage's counterpart for an HTML
// body plus one inline image, as a proper multipart/related MIME message —
// built on Go's mime/multipart.Writer (its boundary generation and part
// framing are already RFC-correct; email's own multipart syntax is the
// same MIME machinery HTTP multipart/form-data uses, just with different
// header values) rather than hand-rolled boundary strings. The image is
// base64-encoded and line-wrapped at 76 characters per RFC 2045 — some
// mail relays/parsers are stricter than modern browsers about honoring
// that limit.
func buildMultipartRelatedMessage(fromName, fromAddress, to, subject, htmlBody string, img InlineImage) ([]byte, error) {
	to, subject = stripCRLF(to), stripCRLF(subject)
	var b strings.Builder
	fmt.Fprintf(&b, "From: %s\r\n", formatFrom(fromName, fromAddress))
	fmt.Fprintf(&b, "To: %s\r\n", to)
	fmt.Fprintf(&b, "Subject: %s\r\n", subject)
	b.WriteString("MIME-Version: 1.0\r\n")

	mw := multipart.NewWriter(&b)
	fmt.Fprintf(&b, "Content-Type: multipart/related; boundary=\"%s\"\r\n\r\n", mw.Boundary())

	htmlHeader := textproto.MIMEHeader{}
	htmlHeader.Set("Content-Type", `text/html; charset="UTF-8"`)
	htmlPart, err := mw.CreatePart(htmlHeader)
	if err != nil {
		return nil, fmt.Errorf("create html part: %w", err)
	}
	if _, err := htmlPart.Write([]byte(htmlBody)); err != nil {
		return nil, fmt.Errorf("write html part: %w", err)
	}

	imgHeader := textproto.MIMEHeader{}
	imgHeader.Set("Content-Type", img.ContentType)
	imgHeader.Set("Content-Transfer-Encoding", "base64")
	imgHeader.Set("Content-ID", "<"+img.ContentID+">")
	imgHeader.Set("Content-Disposition", `inline; filename="logo.png"`)
	imgPart, err := mw.CreatePart(imgHeader)
	if err != nil {
		return nil, fmt.Errorf("create image part: %w", err)
	}
	encoded := base64.StdEncoding.EncodeToString(img.Bytes)
	const lineLen = 76
	for i := 0; i < len(encoded); i += lineLen {
		end := i + lineLen
		if end > len(encoded) {
			end = len(encoded)
		}
		if _, err := fmt.Fprintf(imgPart, "%s\r\n", encoded[i:end]); err != nil {
			return nil, fmt.Errorf("write image part: %w", err)
		}
	}

	if err := mw.Close(); err != nil {
		return nil, fmt.Errorf("close multipart writer: %w", err)
	}
	return []byte(b.String()), nil
}

// formatFrom renders the RFC 2822 "personal name" form of the From header
// (`"Jane at AirMock" <mock@example.com>`) when a display name is set,
// falling back to the bare address when it isn't — matching how a normal
// mail client shows a friendly sender name instead of a raw address.
func formatFrom(name, address string) string {
	name, address = stripCRLF(name), stripCRLF(address)
	if name == "" {
		return address
	}
	// A literal `"` inside the display name must be backslash-escaped, or
	// it would terminate the quoted-string part of the header early.
	escaped := strings.ReplaceAll(name, `"`, `\"`)
	return fmt.Sprintf(`"%s" <%s>`, escaped, address)
}

func stripCRLF(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	return strings.ReplaceAll(s, "\n", " ")
}
