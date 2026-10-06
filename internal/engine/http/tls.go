package httpengine

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"net/http"
	"time"
)

// TLSSettings is what the shared HTTPS listener currently presents. Held
// behind an atomic pointer so it can be hot-rebound (swap to a different
// stored cert, or change client-cert handling) without restarting the
// listener. ClientCertMode is "" (same as "none"), "optional", or
// "required" — see the same three-state values in certs.ClientCertMode,
// kept as a bare string here so this package doesn't need to import certs
// just for an enum.
type TLSSettings struct {
	CertPEM        []byte
	KeyPEM         []byte
	ClientCAPEM    []byte
	ClientCertMode string
}

// UpdateTLSSettings hot-swaps the certificate/mTLS config the HTTPS
// listener presents on the next handshake — no restart required.
func (e *Engine) UpdateTLSSettings(s TLSSettings) {
	e.tlsSettings.Store(&s)
}

// EnsureTLS applies settings and, if the HTTPS listener isn't already
// running, starts it on addr. Safe to call repeatedly (e.g. on every
// admin API update) — an already-running listener just gets new settings.
func (e *Engine) EnsureTLS(ctx context.Context, addr string, settings TLSSettings) error {
	e.UpdateTLSSettings(settings)

	e.tlsMu.Lock()
	running := e.tlsSrv != nil
	e.tlsMu.Unlock()
	if running {
		return nil
	}
	return e.StartTLS(ctx, addr)
}

// DisableTLS stops the HTTPS listener if running. Safe to call when it
// isn't.
func (e *Engine) DisableTLS(ctx context.Context) error {
	e.tlsMu.Lock()
	srv := e.tlsSrv
	e.tlsSrv = nil
	e.tlsMu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

// StartTLS starts the gateway's optional HTTPS listener, serving the same
// route table as the plain listener via GetConfigForClient so cert/mTLS
// settings can be rebound live (see UpdateTLSSettings). Prefer EnsureTLS
// from calling code; StartTLS is exported mainly for the engine_test.go
// suite, which wants direct control over start timing.
func (e *Engine) StartTLS(ctx context.Context, addr string) error {
	tlsConfig := &tls.Config{
		GetConfigForClient: func(*tls.ClientHelloInfo) (*tls.Config, error) {
			return e.buildTLSConfig()
		},
	}

	srv := &http.Server{
		Addr: addr,
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			e.mux.Load().ServeHTTP(w, r)
		}),
		TLSConfig: tlsConfig,
	}
	e.tlsMu.Lock()
	e.tlsSrv = srv
	e.tlsMu.Unlock()

	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServeTLS("", ""); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-time.After(150 * time.Millisecond):
		return nil // listener is up (best-effort: no error within the startup window)
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (e *Engine) StopTLS(ctx context.Context) error {
	e.tlsMu.Lock()
	srv := e.tlsSrv
	e.tlsMu.Unlock()
	if srv == nil {
		return nil
	}
	return srv.Shutdown(ctx)
}

func (e *Engine) buildTLSConfig() (*tls.Config, error) {
	s := e.tlsSettings.Load()
	if s == nil {
		return nil, fmt.Errorf("gateway TLS is not configured")
	}
	return tlsConfigFromSettings(s)
}

// tlsConfigFromSettings builds a *tls.Config from settings — shared by the
// shared gateway's buildTLSConfig above and each project's own dedicated
// TLS listener (see startExtraListener), so both present a certificate the
// exact same way.
func tlsConfigFromSettings(s *TLSSettings) (*tls.Config, error) {
	cert, err := tls.X509KeyPair(s.CertPEM, s.KeyPEM)
	if err != nil {
		return nil, fmt.Errorf("load cert: %w", err)
	}
	cfg := &tls.Config{Certificates: []tls.Certificate{cert}}

	switch s.ClientCertMode {
	case "optional", "required":
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(s.ClientCAPEM) {
			return nil, fmt.Errorf("no valid client CA certificates found")
		}
		cfg.ClientCAs = pool
		if s.ClientCertMode == "required" {
			cfg.ClientAuth = tls.RequireAndVerifyClientCert
		} else {
			cfg.ClientAuth = tls.VerifyClientCertIfGiven
		}
	}
	return cfg, nil
}
