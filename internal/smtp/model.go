// Package smtp is the outgoing-email side of async mock callbacks: a
// single configurable SMTP relay (Settings) and a library of reusable named
// HTML templates (Template) that an async mock's "email" callback channel
// can send through instead of an HTTP webhook.
package smtp

import "time"

// Settings describes the single outgoing SMTP relay used to deliver every
// email-channel callback — one shared relay for the whole instance, the
// same "one setting, many mocks reference it" shape as certs.GatewaySettings
// for gateway TLS, rather than requiring SMTP credentials on every mock.
type Settings struct {
	Host     string `json:"host"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	// FromName is the "personal" (display name) part of the From header —
	// RFC 2822 terminology for the "John Doe" in `From: John Doe
	// <john@example.com>` — so a delivered email shows a friendly sender
	// name instead of just the raw address. Optional: empty means the From
	// header is just FromAddress with no display name.
	FromName    string `json:"fromName,omitempty"`
	FromAddress string `json:"fromAddress"`
	// UseTLS selects STARTTLS on the configured port (587 is the common
	// STARTTLS submission port; 465 implicit-TLS and 25 plaintext relays
	// are both also reachable by turning this off and pointing Port at
	// them, since Send just skips the STARTTLS upgrade when false).
	UseTLS    bool      `json:"useTls"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// Configured reports whether Settings has enough to actually attempt a
// send — an empty Host is the "SMTP was never set up" state, distinguished
// from a real (if wrong) configuration so the caller can surface a clearer
// error than a connection failure to an empty address.
func (s Settings) Configured() bool {
	return s.Host != ""
}

// Template is one named, reusable HTML email body (with {{placeholder}}
// substitution via the same text/template+sprig engine every other mock
// response body uses) plus its subject line — selected by id from an async
// mock's AsyncConfig.EmailTemplateID rather than repeating the same HTML
// inline on every mock that wants to send the same kind of notification.
type Template struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Subject   string    `json:"subject"`
	HTMLBody  string    `json:"htmlBody"`
	CreatedAt time.Time `json:"createdAt"`
	UpdatedAt time.Time `json:"updatedAt"`
}
