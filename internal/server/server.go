package server

import (
	"context"
	"database/sql"
	"fmt"
	"log"
	"net"
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	"github.com/addictedabhi/airmock/internal/apiclient"
	"github.com/addictedabhi/airmock/internal/auth"
	"github.com/addictedabhi/airmock/internal/certs"
	"github.com/addictedabhi/airmock/internal/config"
	"github.com/addictedabhi/airmock/internal/engine"
	diameterengine "github.com/addictedabhi/airmock/internal/engine/diameter"
	ftpengine "github.com/addictedabhi/airmock/internal/engine/ftp"
	httpengine "github.com/addictedabhi/airmock/internal/engine/http"
	jmsengine "github.com/addictedabhi/airmock/internal/engine/jms"
	kafkaengine "github.com/addictedabhi/airmock/internal/engine/kafka"
	mqttengine "github.com/addictedabhi/airmock/internal/engine/mqtt"
	smppengine "github.com/addictedabhi/airmock/internal/engine/smpp"
	smtpengine "github.com/addictedabhi/airmock/internal/engine/smtp"
	tcpengine "github.com/addictedabhi/airmock/internal/engine/tcp"
	"github.com/addictedabhi/airmock/internal/hitlog"
	"github.com/addictedabhi/airmock/internal/mock"
	"github.com/addictedabhi/airmock/internal/scheduledevent"
	"github.com/addictedabhi/airmock/internal/settings"
	"github.com/addictedabhi/airmock/internal/smtp"
	web "github.com/addictedabhi/airmock/internal/web"
	"github.com/addictedabhi/airmock/internal/web/api"
	"github.com/addictedabhi/airmock/internal/wslock"
)

const shutdownTimeout = 5 * time.Second

// Server bootstraps the admin HTTP server and the mock gateway engines.
type Server struct {
	cfg *config.Config
	db  *sql.DB

	admin                      *http.Server
	authManager                *auth.Manager
	httpEngine                 *httpengine.Engine
	certStore                  *certs.Store
	mockStore                  *mock.Store
	callbackWorker             *mock.CallbackWorker
	retentionWorker            *hitlog.RetentionWorker
	scheduledEventWorker       *scheduledevent.Worker
	loadTestRunRetentionWorker *apiclient.LoadTestRunRetentionWorker
}

func New(cfg *config.Config, db *sql.DB) (*Server, error) {
	mockStore := mock.NewStore(db)
	certStore := certs.NewStore(db)
	apiClientStore := apiclient.NewStore(db)
	smtpStore := smtp.NewStore(db)
	settingsStore := settings.NewStore(db)
	mockStore.SetSettingsProvider(settingsStore)
	hitLogStore := hitlog.NewStore(db)
	hitLogBroadcaster := hitlog.NewBroadcaster()
	broadcastingHitLogStore := hitlog.NewBroadcastingStore(hitLogStore, hitLogBroadcaster)
	retentionWorker := hitlog.NewRetentionWorker(hitLogStore, settingsStore)
	loadTestRunRetentionWorker := apiclient.NewLoadTestRunRetentionWorker(apiClientStore, settingsStore)

	// authManager gates the admin UI/API (not the separate mock gateway
	// engine below, which stays reachable by API clients with no login at
	// all) behind either a persisted password (set via the Settings UI —
	// see authStore) or cfg.AdminPassword, the persisted one taking
	// precedence if both exist — see auth.NewManager's own doc comment.
	// Enabled() is false (every check a no-op) when neither is set, which
	// is the default: today's zero-friction local behavior, unless a
	// deployment or an admin visiting Settings explicitly opts in.
	authStore := auth.NewStore(db)
	authManager, err := auth.NewManager(cfg.AdminPassword, authStore)
	if err != nil {
		return nil, fmt.Errorf("init auth manager: %w", err)
	}

	// wsLockTracker is a separate, smaller-scoped session mechanism from
	// authManager above — a per-Collections-workspace PIN/password (see
	// apiclient.Workspace's lock fields) that keeps working even when
	// authManager.Enabled() is false (no admin login configured at all,
	// the common case), so it can't be keyed by authManager's own session
	// cookie. See internal/wslock's package doc for the full reasoning.
	wsLockTracker := wslock.New()

	httpEngine := httpengine.New()
	httpEngine.SetCallbackScheduler(mockStore)
	httpEngine.SetScenarioStepper(mockStore)
	httpEngine.SetHitLogger(broadcastingHitLogStore)
	httpEngine.SetDynamicValues(mockStore)
	httpEngine.SetProjectPortResolver(mockStore)
	httpEngine.SetProjectTLSResolver(api.NewProjectTLSResolver(mockStore, certStore))
	httpEngine.SetEmailTemplateResolver(smtpStore)
	engine.Register(httpEngine)

	// Apply the persisted redacted-headers list and hit-log body-capture
	// limit (both fall back to their hardcoded defaults inside
	// settingsStore.Get itself if never saved) — SettingsHandler re-applies
	// both live on every future save.
	if loaded, err := settingsStore.Get(); err == nil {
		httpEngine.SetRedactedHeaders(loaded.RedactedHeaders)
		httpEngine.SetCORS(api.EngineCORS(loaded.CORS))
		httpEngine.SetBodyCaptureLimit(loaded.MaxCapturedBodyBytes)
		authManager.SetSessionTimeout(time.Duration(loaded.SessionTimeoutMinutes) * time.Minute)
	}

	tcpEngine := tcpengine.New()
	tcpEngine.SetHitLogger(broadcastingHitLogStore)
	tcpEngine.SetDynamicValues(mockStore)
	tcpEngine.SetCertProvider(certStore)
	tcpEngine.SetCallbackScheduler(mockStore)
	tcpEngine.SetEmailTemplateResolver(smtpStore)
	engine.Register(tcpEngine)

	smtpEngine := smtpengine.New()
	smtpEngine.SetHitLogger(broadcastingHitLogStore)
	smtpEngine.SetCertProvider(certStore)
	engine.Register(smtpEngine)

	mqttEngine := mqttengine.New()
	mqttEngine.SetHitLogger(broadcastingHitLogStore)
	mqttEngine.SetDynamicValues(mockStore)
	engine.Register(mqttEngine)

	ftpEngine := ftpengine.New()
	ftpEngine.SetHitLogger(broadcastingHitLogStore)
	engine.Register(ftpEngine)

	kafkaEngine := kafkaengine.New()
	kafkaEngine.SetHitLogger(broadcastingHitLogStore)
	kafkaEngine.SetDynamicValues(mockStore)
	engine.Register(kafkaEngine)

	smppEngine := smppengine.New()
	smppEngine.SetHitLogger(broadcastingHitLogStore)
	smppEngine.SetDynamicValues(mockStore)
	engine.Register(smppEngine)

	diameterEngine := diameterengine.New()
	diameterEngine.SetHitLogger(broadcastingHitLogStore)
	engine.Register(diameterEngine)

	jmsEngine := jmsengine.New()
	jmsEngine.SetHitLogger(broadcastingHitLogStore)
	jmsEngine.SetDynamicValues(mockStore)
	engine.Register(jmsEngine)

	callbackWorker := mock.NewCallbackWorker(mockStore, smtpStore, broadcastingHitLogStore)
	scheduledEventStore := scheduledevent.NewStore(db)
	scheduledEventWorker := scheduledevent.NewWorker(scheduledEventStore, broadcastingHitLogStore)

	tlsAddr := fmt.Sprintf(":%d", cfg.GatewayTLSPort)

	r := chi.NewRouter()
	r.Use(middleware.Logger)
	r.Use(middleware.Recoverer)

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	})
	// At the bare root, not under /api, matching where every Prometheus
	// scraper (and its default scrape-config template) looks by convention.
	r.Get("/metrics", api.NewMetricsHandler(mockStore, hitLogStore, certStore, scheduledEventStore).ServeHTTP)

	authHandler := api.NewAuthHandler(authManager)

	r.Route("/api", func(r chi.Router) {
		// Before auth and every handler: refuses cross-site browser writes.
		r.Use(api.SameOriginGuard(cfg.AllowedOrigins))
		// A single mount — Routes itself applies RequireAuth per-route to
		// set-password/clear-password (chi panics if /auth were Mount()ed
		// twice, which splitting it across this outer router and the
		// authenticated group below would do). status/login/logout stay
		// reachable with no session at all, which is the whole point.
		r.Route("/auth", authHandler.Routes)

		r.Group(func(r chi.Router) {
			r.Use(authHandler.RequireAuth)
			r.Route("/mocks", api.NewMocksHandler(mockStore, apiClientStore, wsLockTracker).Routes)
			r.Route("/mocks/{id}/sessions", api.NewSessionsHandler(mockStore).Routes)
			r.Route("/mock-projects", api.NewMockProjectsHandler(mockStore, httpEngine, apiClientStore, wsLockTracker).Routes)
			r.Route("/wsdl", api.NewWSDLHandler(mockStore).Routes)
			r.Route("/graphql-schema", api.NewGraphQLHandler(mockStore).Routes)
			r.Route("/openapi", api.NewOpenAPIHandler(mockStore).Routes)
			r.Route("/traffic-import", api.NewTrafficImportHandler(mockStore).Routes)
			r.Route("/certificates", api.NewCertificatesHandler(certStore, mockStore, httpEngine, tlsAddr).Routes)
			r.Route("/cert-bundles", api.NewCertBundlesHandler(certStore).Routes)
			r.Route("/gateway", api.NewGatewayHandler(certStore, httpEngine, tlsAddr).Routes)
			r.Route("/tools", api.NewToolsHandler().Routes)
			r.Route("/apiclient", api.NewAPIClientHandler(apiClientStore, certStore, broadcastingHitLogStore, wsLockTracker).Routes)
			r.Route("/smtp", api.NewSMTPHandler(smtpStore).Routes)
			r.Route("/hitlog", api.NewHitLogHandler(hitLogStore, mockStore, hitLogBroadcaster).Routes)
			r.Route("/settings", api.NewSettingsHandler(settingsStore, httpEngine, mockStore, authManager).Routes)
			r.Route("/scheduled-events", api.NewScheduledEventsHandler(scheduledEventStore, scheduledEventWorker).Routes)
			r.Route("/backup", api.NewBackupHandler(certStore, mockStore, apiClientStore, smtpStore, scheduledEventStore).Routes)
			r.Route("/config", api.NewConfigHandler(cfg).Routes)
		})
	})

	r.Handle("/*", http.FileServer(http.FS(web.DistFS())))

	return &Server{
		cfg: cfg,
		db:  db,
		admin: &http.Server{
			Addr:    net.JoinHostPort(cfg.AdminHost, strconv.Itoa(cfg.AdminPort)),
			Handler: r,
		},
		authManager:                authManager,
		httpEngine:                 httpEngine,
		certStore:                  certStore,
		mockStore:                  mockStore,
		callbackWorker:             callbackWorker,
		retentionWorker:            retentionWorker,
		scheduledEventWorker:       scheduledEventWorker,
		loadTestRunRetentionWorker: loadTestRunRetentionWorker,
	}, nil
}

// Run loads existing mocks and gateway TLS settings into their engines,
// starts the admin server, the callback worker, and every registered
// protocol engine, and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	if err := s.loadExistingMocks(); err != nil {
		return fmt.Errorf("load existing mocks: %w", err)
	}

	if err := s.httpEngine.Start(ctx, engine.ListenerConfig{Addr: fmt.Sprintf(":%d", s.cfg.GatewayPort)}); err != nil {
		return fmt.Errorf("start gateway engine: %w", err)
	}

	if err := s.restoreGatewayTLS(ctx); err != nil {
		return fmt.Errorf("restore gateway TLS settings: %w", err)
	}

	s.callbackWorker.Start(ctx)
	s.retentionWorker.Start(ctx)
	s.loadTestRunRetentionWorker.Start(ctx)
	s.scheduledEventWorker.Start(ctx)

	if !config.IsLoopbackHost(s.cfg.AdminHost) && !s.authManager.Enabled() {
		log.Printf("airmock: WARNING: the admin UI/API is listening on %s with login DISABLED, so anyone who can reach this port can create mocks, export certificate private keys and make the server fetch URLs. Enable login (Settings > Security, or --admin-password) or bind to 127.0.0.1 (--admin-host).", s.admin.Addr)
	}

	errCh := make(chan error, 1)
	go func() {
		if err := s.admin.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
		defer cancel()
		for _, e := range engine.All() {
			e.Stop(shutdownCtx)
		}
		return s.admin.Shutdown(shutdownCtx)
	case err := <-errCh:
		return err
	}
}

// loadExistingMocks registers every enabled, persisted mock with its engine
// on startup. A single mock failing to register (e.g. its listener port is
// now taken by something else) must not prevent every other mock — and the
// whole app — from starting, so failures are logged and skipped rather than
// aborting the loop.
func (s *Server) loadExistingMocks() error {
	defs, err := s.mockStore.List()
	if err != nil {
		return err
	}
	for _, d := range defs {
		if !d.Enabled {
			continue
		}
		dispatchMockSafely(d)
	}
	return nil
}

// dispatchMockSafely calls engine.Dispatch inside a recover() boundary. A
// stored Definition can be malformed in a way that PANICS an engine rather
// than returning an error — e.g. a REST mock with an invalid HTTP Method
// panics chi's router (validateMockShape now rejects this at create/update/
// import time, but a row written to the database before that check existed
// would still panic here). Without this recover, one such row would crash
// the entire server on every future startup, leaving no way to recover
// short of manually editing the database.
func dispatchMockSafely(d *mock.Definition) {
	defer func() {
		if r := recover(); r != nil {
			log.Printf("airmock: mock %q (%s) panicked while registering on startup, skipping: %v", d.Name, d.ID, r)
		}
	}()
	if err := engine.Dispatch(d); err != nil {
		log.Printf("airmock: failed to register mock %q (%s) on startup, skipping: %v", d.Name, d.ID, err)
	}
}

func (s *Server) restoreGatewayTLS(ctx context.Context) error {
	gs, err := s.certStore.GetGatewaySettings()
	if err != nil {
		return err
	}
	if !gs.Enabled {
		return nil // nothing to restore; TLS stays off until enabled via the admin API
	}
	tlsAddr := fmt.Sprintf(":%d", s.cfg.GatewayTLSPort)
	return api.ApplyGatewayTLS(ctx, s.certStore, s.httpEngine, tlsAddr, gs)
}
