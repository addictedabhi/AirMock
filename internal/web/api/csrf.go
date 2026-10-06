package api

import (
	"errors"
	"net/http"
	"net/url"
	"strings"
)

// SameOriginGuard stops other websites from driving the admin API through a
// visitor's browser. A page on any origin can make the browser send a
// "simple" cross-site POST (text/plain, form or multipart bodies need no
// CORS preflight), and with admin login off that was enough to create or
// change mocks on a locally running AirMock.
//
// For every state-changing method it requires the browser's own headers to
// show the request came from this server's origin:
//   - an Origin header, if present, must match the Host the request was sent
//     to (or be on allowed);
//   - with no Origin, a Sec-Fetch-Site of cross-site or same-site is refused
//     ("same-site" includes any other port on the same host).
//
// Requests with neither header (curl, scripts, the airmock CLI) are not a
// browser CSRF vector and pass. Reads are never blocked.
func SameOriginGuard(allowed []string) func(http.Handler) http.Handler {
	allow := map[string]bool{}
	for _, o := range allowed {
		allow[strings.ToLower(strings.TrimRight(strings.TrimSpace(o), "/"))] = true
	}
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch r.Method {
			case http.MethodGet, http.MethodHead, http.MethodOptions:
				next.ServeHTTP(w, r)
				return
			}
			if err := checkSameOrigin(r, allow); err != nil {
				writeErr(w, http.StatusForbidden, err)
				return
			}
			next.ServeHTTP(w, r)
		})
	}
}

func checkSameOrigin(r *http.Request, allow map[string]bool) error {
	blocked := errors.New("cross-site request blocked: the admin API only accepts changes made from its own page (or from a client that sends no Origin header, such as curl or the airmock CLI). If AirMock is behind a proxy under another hostname, add it with --allowed-origin")
	if origin := r.Header.Get("Origin"); origin != "" {
		if allow[strings.ToLower(strings.TrimRight(origin, "/"))] {
			return nil
		}
		u, err := url.Parse(origin)
		if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
			return blocked
		}
		return nil
	}
	switch r.Header.Get("Sec-Fetch-Site") {
	case "cross-site", "same-site":
		return blocked
	}
	return nil
}
