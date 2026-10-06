package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"log"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/addictedabhi/airmock/internal/smtp"
	"github.com/addictedabhi/airmock/internal/web"
)

type SMTPHandler struct {
	store *smtp.Store
}

func NewSMTPHandler(store *smtp.Store) *SMTPHandler {
	return &SMTPHandler{store: store}
}

func (h *SMTPHandler) Routes(r chi.Router) {
	r.Get("/settings", h.getSettings)
	r.Put("/settings", h.saveSettings)
	r.Post("/test", h.testSend)

	r.Route("/templates", func(r chi.Router) {
		r.Get("/", h.listTemplates)
		r.Post("/", h.createTemplate)
		r.Get("/{id}", h.getTemplate)
		r.Put("/{id}", h.updateTemplate)
		r.Delete("/{id}", h.deleteTemplate)
	})
}

// settingsResponse omits Password (like certs.Certificate.KeyPEM, the one
// other credential this app stores) and reports whether one is set instead
// — the UI can then show "(password set)" without the raw secret ever
// round-tripping back down to the browser on a plain GET.
type settingsResponse struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	PasswordSet bool   `json:"passwordSet"`
	FromName    string `json:"fromName"`
	FromAddress string `json:"fromAddress"`
	UseTLS      bool   `json:"useTls"`
}

func toSettingsResponse(st smtp.Settings) settingsResponse {
	return settingsResponse{
		Host: st.Host, Port: st.Port, Username: st.Username,
		PasswordSet: st.Password != "", FromName: st.FromName, FromAddress: st.FromAddress, UseTLS: st.UseTLS,
	}
}

func (h *SMTPHandler) getSettings(w http.ResponseWriter, r *http.Request) {
	st, err := h.store.GetSettings()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettingsResponse(st))
}

type saveSettingsRequest struct {
	Host        string `json:"host"`
	Port        int    `json:"port"`
	Username    string `json:"username"`
	Password    string `json:"password"` // empty means "keep the existing password" — see saveSettings
	FromName    string `json:"fromName"`
	FromAddress string `json:"fromAddress"`
	UseTLS      bool   `json:"useTls"`
}

func (h *SMTPHandler) saveSettings(w http.ResponseWriter, r *http.Request) {
	var body saveSettingsRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}

	password := body.Password
	if password == "" {
		existing, err := h.store.GetSettings()
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err)
			return
		}
		password = existing.Password
	}

	saved, err := h.store.SaveSettings(smtp.Settings{
		Host: body.Host, Port: body.Port, Username: body.Username, Password: password,
		FromName: body.FromName, FromAddress: body.FromAddress, UseTLS: body.UseTLS,
	})
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, toSettingsResponse(saved))
}

type testSendRequest struct {
	To string `json:"to"`
}

// testSend delivers a real test email through whatever's currently saved —
// the SMTP equivalent of the certificate store's "Test TLS/Certificate"
// action, so a relay config can be verified before any real mock depends
// on it. Uses AirMock's own logo (see airmockLogoBytes) as a proper
// CID-embedded inline image rather than a bare <p>, so what a user sees
// actually resembles a real transactional email instead of an obviously-
// fake placeholder.
func (h *SMTPHandler) testSend(w http.ResponseWriter, r *http.Request) {
	var body testSendRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if body.To == "" {
		writeErr(w, http.StatusBadRequest, errors.New("to is required"))
		return
	}
	settings, err := h.store.GetSettings()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}

	html := testEmailHTML(time.Now())
	if logo, logoErr := airmockLogoBytes(); logoErr == nil {
		err = smtp.SendWithInlineImage(settings, body.To, "AirMock test email", html, smtp.InlineImage{
			ContentID:   "airmock-logo",
			Bytes:       logo,
			ContentType: "image/png",
		})
	} else {
		// The logo is embedded into the binary at build time (see
		// web.DistFS) so this genuinely shouldn't happen — degrading to a
		// logo-less email (the <img> tag just won't render) rather than
		// failing the whole test send is the safer choice if it somehow
		// ever does.
		log.Printf("airmock: test email logo asset unavailable, sending without it: %v", logoErr)
		err = smtp.Send(settings, body.To, "AirMock test email", html)
	}
	if err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]bool{"sent": true})
}

// airmockLogoBytes reads AirMock's own product icon out of the embedded
// SPA build (see web.DistFS) — a white-stroke variant of the app's icon
// (see ui/public/favicon.svg for the full-color one used in the browser
// tab/sidebar), pre-rasterized to PNG since email clients have poor/no
// inline SVG support. White, not the usual blue-gradient strokes, because
// this renders on the email's own solid blue header (see testEmailHTML) —
// the blue-on-blue full-color version was nearly invisible there. Lives in
// ui/public/, which Vite copies verbatim to the dist root with no content
// hash (unlike ui/src/assets/, which JS imports pull in as hashed, bundled
// assets) — a fixed path, so no globbing needed.
func airmockLogoBytes() ([]byte, error) {
	return fs.ReadFile(web.DistFS(), "airmock-icon-email.png")
}

// testEmailHTML renders the "Send test email" body as a real transactional-
// looking design — table-based layout with every style inlined, the same
// approach genuine transactional email templates use since many mail
// clients (Outlook desktop especially) don't reliably support <style>
// blocks or modern CSS layout (flexbox/grid). References the logo via
// cid:airmock-logo (see airmockLogoBytes/SendWithInlineImage) rather than
// a data: URI or a remote-hosted URL, both of which fail in large swaths
// of real mail clients. The logo links out to github.com/addictedabhi/AirMock — the
// one clickable element a recipient would actually expect a product logo
// in an email to be.
func testEmailHTML(sentAt time.Time) string {
	return fmt.Sprintf(`<!DOCTYPE html>
<html>
  <body style="margin:0;padding:0;background-color:#f1f5f9;font-family:'Segoe UI',Helvetica,Arial,sans-serif;">
    <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="background-color:#f1f5f9;padding:32px 16px;">
      <tr>
        <td align="center">
          <table role="presentation" width="480" cellpadding="0" cellspacing="0" style="background-color:#ffffff;border-radius:12px;overflow:hidden;box-shadow:0 1px 3px rgba(0,0,0,.08);">
            <tr>
              <td style="background-color:#0052cc;background-image:linear-gradient(135deg,#0052cc,#0a78d4);padding:28px 32px;text-align:center;">
                <a href="https://github.com/addictedabhi/AirMock" style="display:block;width:48px;margin:0 auto 12px;">
                  <img src="cid:airmock-logo" width="48" height="48" alt="AirMock" style="display:block;border:0;border-radius:10px;" />
                </a>
                <div style="color:#ffffff;font-size:20px;font-weight:700;letter-spacing:.2px;">AirMock</div>
                <div style="color:rgba(255,255,255,.75);font-size:12px;margin-top:2px;">Cross-protocol mock server</div>
              </td>
            </tr>
            <tr>
              <td style="padding:32px;">
                <span style="display:inline-block;background-color:#dcfce7;color:#16a34a;font-size:12px;font-weight:700;padding:4px 12px;border-radius:16px;">&#10003; Relay working</span>
                <h1 style="margin:16px 0 12px;font-size:18px;color:#1e293b;">Your SMTP relay is configured correctly</h1>
                <p style="margin:0 0 20px;font-size:14px;line-height:1.6;color:#475569;">
                  This is a test email sent from AirMock's SMTP Settings page. If you're reading this, outgoing mail through your
                  configured relay is working — an async mock's own email callback will deliver the exact same way.
                </p>
                <table role="presentation" width="100%%" cellpadding="0" cellspacing="0" style="border-collapse:collapse;">
                  <tr>
                    <td style="padding:10px 0;border-top:1px solid #e2e8f0;font-size:12px;color:#64748b;">Sent</td>
                    <td style="padding:10px 0;border-top:1px solid #e2e8f0;font-size:12px;color:#1e293b;text-align:right;">%s</td>
                  </tr>
                </table>
              </td>
            </tr>
            <tr>
              <td style="padding:16px 32px;background-color:#f8fafc;text-align:center;">
                <span style="font-size:11px;color:#94a3b8;">Sent by AirMock — a lightweight, self-contained, cross-protocol API mocking tool.</span>
              </td>
            </tr>
          </table>
        </td>
      </tr>
    </table>
  </body>
</html>`, sentAt.Format("Jan 2, 2006 3:04 PM MST"))
}

func (h *SMTPHandler) listTemplates(w http.ResponseWriter, r *http.Request) {
	list, err := h.store.ListTemplates()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, list)
}

func (h *SMTPHandler) createTemplate(w http.ResponseWriter, r *http.Request) {
	var t smtp.Template
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	if t.Name == "" {
		writeErr(w, http.StatusBadRequest, errors.New("name is required"))
		return
	}
	created, err := h.store.CreateTemplate(&t)
	if errors.Is(err, smtp.ErrDuplicateName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusCreated, created)
}

func (h *SMTPHandler) getTemplate(w http.ResponseWriter, r *http.Request) {
	t, err := h.store.GetTemplate(chi.URLParam(r, "id"))
	if errors.Is(err, smtp.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, t)
}

func (h *SMTPHandler) updateTemplate(w http.ResponseWriter, r *http.Request) {
	var t smtp.Template
	if err := json.NewDecoder(r.Body).Decode(&t); err != nil {
		writeErr(w, http.StatusBadRequest, err)
		return
	}
	t.ID = chi.URLParam(r, "id")
	updated, err := h.store.UpdateTemplate(&t)
	if errors.Is(err, smtp.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if errors.Is(err, smtp.ErrDuplicateName) {
		writeErr(w, http.StatusConflict, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	writeJSON(w, http.StatusOK, updated)
}

func (h *SMTPHandler) deleteTemplate(w http.ResponseWriter, r *http.Request) {
	err := h.store.DeleteTemplate(chi.URLParam(r, "id"))
	if errors.Is(err, smtp.ErrNotFound) {
		writeErr(w, http.StatusNotFound, err)
		return
	}
	if err != nil {
		writeErr(w, http.StatusInternalServerError, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
