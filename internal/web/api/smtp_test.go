package api

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/smtp"
	"github.com/addictedabhi/airmock/internal/storage"
)

func newTestSMTPRouter(t *testing.T) (chi.Router, *smtp.Store) {
	t.Helper()
	db, err := storage.Open(filepath.Join(t.TempDir(), "test.db"))
	if err != nil {
		t.Fatalf("storage.Open: %v", err)
	}
	t.Cleanup(func() { db.Close() })

	store := smtp.NewStore(db)
	r := chi.NewRouter()
	r.Route("/api/smtp", NewSMTPHandler(store).Routes)
	return r, store
}

// fakeSMTPServer speaks just enough SMTP (no STARTTLS/AUTH advertised) to
// accept one real EHLO/MAIL/RCPT/DATA/QUIT conversation over a real TCP
// connection — the same minimal-server idiom already used by
// internal/smtp's own tests and internal/engine/http's async-email test,
// reused here so testSend's handler wiring (store -> smtp.Send) is
// exercised against a real listener instead of skipped for lack of network
// access.
func fakeSMTPServer(t *testing.T) (host string, port int) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		r := bufio.NewReader(conn)
		fmt.Fprintf(conn, "220 fake.smtp ESMTP\r\n")
		inData := false
		for {
			line, err := r.ReadString('\n')
			if err != nil {
				return
			}
			switch {
			case inData:
				if strings.TrimRight(line, "\r\n") == "." {
					inData = false
					fmt.Fprintf(conn, "250 OK\r\n")
					continue
				}
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

	h, portStr, err := net.SplitHostPort(ln.Addr().String())
	if err != nil {
		t.Fatalf("split host port: %v", err)
	}
	p, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatalf("parse port: %v", err)
	}
	return h, p
}

func TestSMTPGetSettingsDefaultsToUnconfigured(t *testing.T) {
	r, _ := newTestSMTPRouter(t)

	rec := doJSON(t, r, http.MethodGet, "/api/smtp/settings", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Host != "" || got.PasswordSet {
		t.Fatalf("expected an unconfigured fresh store, got %+v", got)
	}
}

func TestSMTPSaveSettingsRoundTrip(t *testing.T) {
	r, store := newTestSMTPRouter(t)

	rec := doJSON(t, r, http.MethodPut, "/api/smtp/settings", saveSettingsRequest{
		Host: "smtp.example.com", Port: 587, Username: "user", Password: "secret",
		FromName: "AirMock", FromAddress: "mock@example.com", UseTLS: true,
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got settingsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.Host != "smtp.example.com" || !got.PasswordSet {
		t.Fatalf("unexpected response: %+v", got)
	}
	// The raw password must never round-trip back down to the browser.
	if strings.Contains(rec.Body.String(), "secret") {
		t.Fatalf("expected the raw password to never appear in the response body, got: %s", rec.Body.String())
	}

	persisted, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if persisted.Password != "secret" {
		t.Fatalf("expected the password to actually be persisted in the store, got %q", persisted.Password)
	}

	// A GET afterward must reflect the same persisted state.
	getRec := doJSON(t, r, http.MethodGet, "/api/smtp/settings", nil)
	var reGot settingsResponse
	if err := json.Unmarshal(getRec.Body.Bytes(), &reGot); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if reGot.Host != "smtp.example.com" || !reGot.PasswordSet {
		t.Fatalf("expected GET to reflect the saved settings, got %+v", reGot)
	}
}

// TestSMTPSaveSettingsKeepsExistingPasswordWhenBlank guards
// saveSettingsRequest.Password's documented "empty means keep the existing
// password" contract in saveSettings — otherwise every settings edit that
// doesn't explicitly resend a password (the UI never shows or resends the
// existing one, since it's write-only) would silently wipe it.
func TestSMTPSaveSettingsKeepsExistingPasswordWhenBlank(t *testing.T) {
	r, store := newTestSMTPRouter(t)

	doJSON(t, r, http.MethodPut, "/api/smtp/settings", saveSettingsRequest{
		Host: "smtp.example.com", Port: 587, Password: "original-secret", FromAddress: "mock@example.com",
	})

	rec := doJSON(t, r, http.MethodPut, "/api/smtp/settings", saveSettingsRequest{
		Host: "smtp2.example.com", Port: 25, Password: "", FromAddress: "mock2@example.com",
	})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}

	persisted, err := store.GetSettings()
	if err != nil {
		t.Fatalf("GetSettings: %v", err)
	}
	if persisted.Password != "original-secret" {
		t.Fatalf("expected the existing password to be preserved, got %q", persisted.Password)
	}
	if persisted.Host != "smtp2.example.com" {
		t.Fatalf("expected the other fields to still update, got %+v", persisted)
	}
}

func TestSMTPTestSendSuccess(t *testing.T) {
	r, store := newTestSMTPRouter(t)
	host, port := fakeSMTPServer(t)
	if _, err := store.SaveSettings(smtp.Settings{Host: host, Port: port, FromAddress: "mock@example.com", UseTLS: false}); err != nil {
		t.Fatalf("SaveSettings: %v", err)
	}

	rec := doJSON(t, r, http.MethodPost, "/api/smtp/test", testSendRequest{To: "to@example.com"})
	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var got map[string]bool
	if err := json.Unmarshal(rec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if !got["sent"] {
		t.Fatalf("expected sent=true, got %+v", got)
	}
}

func TestSMTPTestSendFailsWhenToMissing(t *testing.T) {
	r, _ := newTestSMTPRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/smtp/test", testSendRequest{})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 when 'to' is missing, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestSMTPTestSendFailsWhenUnconfigured exercises testSend's failure path
// without needing an actually-unreachable network address: an empty/never
// -configured relay (smtp.Settings.Configured() == false) is rejected by
// smtp.Send itself before any dial is attempted, so this deterministically
// covers the "send failed" -> 400 branch with no network flakiness.
func TestSMTPTestSendFailsWhenUnconfigured(t *testing.T) {
	r, _ := newTestSMTPRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/smtp/test", testSendRequest{To: "to@example.com"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for an unconfigured relay, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSMTPTemplatesCRUD(t *testing.T) {
	r, store := newTestSMTPRouter(t)

	createRec := doJSON(t, r, http.MethodPost, "/api/smtp/templates/", smtp.Template{
		Name: "Order confirmation", Subject: "Order shipped", HTMLBody: "<p>Thanks!</p>",
	})
	if createRec.Code != http.StatusCreated {
		t.Fatalf("expected 201, got %d: %s", createRec.Code, createRec.Body.String())
	}
	var created smtp.Template
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("unmarshal created: %v", err)
	}
	if created.ID == "" {
		t.Fatal("expected the created template to have an assigned id")
	}

	listRec := doJSON(t, r, http.MethodGet, "/api/smtp/templates/", nil)
	var list []*smtp.Template
	if err := json.Unmarshal(listRec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal list: %v", err)
	}
	if len(list) != 1 {
		t.Fatalf("expected 1 template, got %d", len(list))
	}

	getRec := doJSON(t, r, http.MethodGet, "/api/smtp/templates/"+created.ID, nil)
	if getRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", getRec.Code, getRec.Body.String())
	}
	var got smtp.Template
	if err := json.Unmarshal(getRec.Body.Bytes(), &got); err != nil {
		t.Fatalf("unmarshal get: %v", err)
	}
	if got.Name != "Order confirmation" {
		t.Fatalf("unexpected fetched template: %+v", got)
	}

	got.HTMLBody = "<p>Updated!</p>"
	updateRec := doJSON(t, r, http.MethodPut, "/api/smtp/templates/"+created.ID, got)
	if updateRec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", updateRec.Code, updateRec.Body.String())
	}
	var updated smtp.Template
	if err := json.Unmarshal(updateRec.Body.Bytes(), &updated); err != nil {
		t.Fatalf("unmarshal updated: %v", err)
	}
	if updated.HTMLBody != "<p>Updated!</p>" {
		t.Fatalf("expected updated body, got %+v", updated)
	}

	reGot, err := store.GetTemplate(created.ID)
	if err != nil {
		t.Fatalf("GetTemplate: %v", err)
	}
	if reGot.HTMLBody != "<p>Updated!</p>" {
		t.Fatalf("expected the update to be persisted, got %+v", reGot)
	}

	deleteRec := doJSON(t, r, http.MethodDelete, "/api/smtp/templates/"+created.ID, nil)
	if deleteRec.Code != http.StatusNoContent {
		t.Fatalf("expected 204, got %d: %s", deleteRec.Code, deleteRec.Body.String())
	}
	if _, err := store.GetTemplate(created.ID); err != smtp.ErrNotFound {
		t.Fatalf("expected the template to be gone after delete, got err=%v", err)
	}
}

func TestSMTPTemplatesCreateRejectsMissingName(t *testing.T) {
	r, _ := newTestSMTPRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/smtp/templates/", smtp.Template{Subject: "no name"})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for a missing name, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSMTPTemplatesCreateRejectsDuplicateName(t *testing.T) {
	r, _ := newTestSMTPRouter(t)

	rec := doJSON(t, r, http.MethodPost, "/api/smtp/templates/", smtp.Template{Name: "dup"})
	if rec.Code != http.StatusCreated {
		t.Fatalf("expected first create to succeed, got %d: %s", rec.Code, rec.Body.String())
	}
	rec2 := doJSON(t, r, http.MethodPost, "/api/smtp/templates/", smtp.Template{Name: " DUP "})
	if rec2.Code != http.StatusConflict {
		t.Fatalf("expected 409 for a case/whitespace-insensitive duplicate name, got %d: %s", rec2.Code, rec2.Body.String())
	}
}

func TestSMTPTemplatesGetNotFound(t *testing.T) {
	r, _ := newTestSMTPRouter(t)

	rec := doJSON(t, r, http.MethodGet, "/api/smtp/templates/does-not-exist", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSMTPTemplatesUpdateNotFound(t *testing.T) {
	r, _ := newTestSMTPRouter(t)

	rec := doJSON(t, r, http.MethodPut, "/api/smtp/templates/does-not-exist", smtp.Template{Name: "ghost"})
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSMTPTemplatesUpdateRejectsDuplicateName(t *testing.T) {
	r, _ := newTestSMTPRouter(t)
	doJSON(t, r, http.MethodPost, "/api/smtp/templates/", smtp.Template{Name: "taken"})
	otherRec := doJSON(t, r, http.MethodPost, "/api/smtp/templates/", smtp.Template{Name: "other"})
	var other smtp.Template
	if err := json.Unmarshal(otherRec.Body.Bytes(), &other); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	rec := doJSON(t, r, http.MethodPut, "/api/smtp/templates/"+other.ID, smtp.Template{Name: "taken"})
	if rec.Code != http.StatusConflict {
		t.Fatalf("expected 409 for renaming into an existing name, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestSMTPTemplatesDeleteNotFound(t *testing.T) {
	r, _ := newTestSMTPRouter(t)

	rec := doJSON(t, r, http.MethodDelete, "/api/smtp/templates/does-not-exist", nil)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

// TestAirmockLogoBytesFindsTheEmbeddedAsset guards the "Send test email"
// logo lookup directly: the embedded SPA build (see web.DistFS) must
// contain a real, non-empty airmock-icon-email.png regardless of the content
// hash Vite gives it on any given build.
func TestAirmockLogoBytesFindsTheEmbeddedAsset(t *testing.T) {
	b, err := airmockLogoBytes()
	if err != nil {
		t.Fatalf("airmockLogoBytes: %v", err)
	}
	if len(b) == 0 {
		t.Fatal("expected non-empty logo bytes")
	}
	// A PNG file always starts with this 8-byte signature — a cheap sanity
	// check that this is genuinely image data, not e.g. an empty/truncated
	// file the glob happened to match.
	pngSignature := []byte{0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n'}
	if len(b) < len(pngSignature) || string(b[:len(pngSignature)]) != string(pngSignature) {
		t.Fatalf("expected a valid PNG signature, got the first bytes: %v", b[:min(len(b), 16)])
	}
}

// TestTestEmailHTMLReferencesTheLogoAndSentTime guards the things that
// actually make this an "attractive" template rather than the old bare
// <p>: a cid: reference to the embedded logo (see SendWithInlineImage), a
// rendered send timestamp (not a static placeholder), and the logo being
// wrapped in a link out to the product site — recipients expect a product
// logo in an email to be clickable.
func TestTestEmailHTMLReferencesTheLogoAndSentTime(t *testing.T) {
	sentAt := time.Date(2026, 3, 4, 15, 30, 0, 0, time.UTC)
	html := testEmailHTML(sentAt)
	if !strings.Contains(html, `src="cid:airmock-logo"`) {
		t.Fatalf("expected a cid: reference to the logo, got:\n%s", html)
	}
	if !strings.Contains(html, "AirMock") {
		t.Fatalf("expected the AirMock name somewhere in the template, got:\n%s", html)
	}
	if !strings.Contains(html, sentAt.Format("Jan 2, 2006 3:04 PM MST")) {
		t.Fatalf("expected the rendered send time, got:\n%s", html)
	}
	linkIdx := strings.Index(html, `<a href="https://github.com/addictedabhi/AirMock"`)
	if linkIdx == -1 {
		t.Fatalf("expected the logo linked to the product site, got:\n%s", html)
	}
	// The link must actually wrap the <img>, not just appear somewhere else
	// in the template — otherwise the logo itself wouldn't be clickable at
	// all, defeating the point. Checked structurally (link opens, then the
	// img appears before the matching </a>) rather than an exact-whitespace
	// substring match, so reformatting the template source doesn't break
	// this test.
	closeIdx := strings.Index(html[linkIdx:], "</a>")
	if closeIdx == -1 {
		t.Fatalf("expected a closing </a> after the link, got:\n%s", html)
	}
	between := html[linkIdx : linkIdx+closeIdx]
	if !strings.Contains(between, `<img src="cid:airmock-logo"`) {
		t.Fatalf("expected the logo <img> between the <a> and </a>, got:\n%s", html)
	}
}
