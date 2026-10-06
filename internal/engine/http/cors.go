package httpengine

import (
	"net/http"
	"strconv"
	"strings"
)

// CORSConfig controls the Cross-Origin Resource Sharing headers the mock
// gateway adds, so a browser front end on another origin can call the mocks.
// It applies to every mock on the shared gateway and on project ports.
type CORSConfig struct {
	Enabled bool
	// AllowOrigin is "*" or a comma-separated list of exact origins. With
	// AllowCredentials the request's own Origin is echoed instead of "*",
	// since browsers reject a wildcard on credentialed requests.
	AllowOrigin string
	// AllowMethods answers a preflight's Access-Control-Request-Method.
	AllowMethods string
	// AllowHeaders is "*" (reflect whatever the preflight asked for) or a
	// fixed comma-separated list.
	AllowHeaders     string
	AllowCredentials bool
	MaxAgeSecs       int
}

// DefaultCORS is permissive, matching what a developer expects of a mock
// server: any origin, common methods, any headers, no credentials.
func DefaultCORS() CORSConfig {
	return CORSConfig{
		Enabled:      true,
		AllowOrigin:  "*",
		AllowMethods: "GET, POST, PUT, PATCH, DELETE, HEAD, OPTIONS",
		AllowHeaders: "*",
		MaxAgeSecs:   600,
	}
}

// SetCORS replaces the gateway's CORS settings; applies to the next request.
func (e *Engine) SetCORS(c CORSConfig) {
	e.cors.Store(&c)
}

func (e *Engine) corsConfig() CORSConfig {
	if c := e.cors.Load(); c != nil {
		return *c
	}
	return DefaultCORS()
}

// allowedOrigin returns the Access-Control-Allow-Origin value for a request
// from origin, or "" if that origin is not allowed.
func (c CORSConfig) allowedOrigin(origin string) string {
	ao := strings.TrimSpace(c.AllowOrigin)
	if ao == "" || ao == "*" {
		if c.AllowCredentials {
			return origin
		}
		return "*"
	}
	for _, o := range strings.Split(ao, ",") {
		if strings.EqualFold(strings.TrimSpace(o), origin) {
			return origin
		}
	}
	return ""
}

// corsMiddleware answers preflights itself and adds Allow-Origin to normal
// responses. Requests without an Origin header are not CORS requests and
// pass through untouched.
func (e *Engine) corsMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		cfg := e.corsConfig()
		origin := r.Header.Get("Origin")
		if !cfg.Enabled || origin == "" {
			next.ServeHTTP(w, r)
			return
		}
		allow := cfg.allowedOrigin(origin)
		h := w.Header()
		h.Add("Vary", "Origin")

		if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
			if allow != "" {
				h.Set("Access-Control-Allow-Origin", allow)
				if cfg.AllowCredentials {
					h.Set("Access-Control-Allow-Credentials", "true")
				}
				h.Set("Access-Control-Allow-Methods", cfg.AllowMethods)
				reqHeaders := r.Header.Get("Access-Control-Request-Headers")
				switch {
				case strings.TrimSpace(cfg.AllowHeaders) == "*" && reqHeaders != "":
					h.Set("Access-Control-Allow-Headers", reqHeaders)
				case strings.TrimSpace(cfg.AllowHeaders) == "*":
					h.Set("Access-Control-Allow-Headers", "*")
				default:
					h.Set("Access-Control-Allow-Headers", cfg.AllowHeaders)
				}
				if cfg.MaxAgeSecs > 0 {
					h.Set("Access-Control-Max-Age", strconv.Itoa(cfg.MaxAgeSecs))
				}
				h.Add("Vary", "Access-Control-Request-Method")
				h.Add("Vary", "Access-Control-Request-Headers")
			}
			w.WriteHeader(http.StatusNoContent)
			return
		}

		if allow != "" {
			h.Set("Access-Control-Allow-Origin", allow)
			if cfg.AllowCredentials {
				h.Set("Access-Control-Allow-Credentials", "true")
			} else {
				h.Set("Access-Control-Expose-Headers", "*")
			}
		}
		next.ServeHTTP(w, r)
	})
}
