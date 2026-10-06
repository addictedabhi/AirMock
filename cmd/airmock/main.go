package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/pkg/browser"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"

	"github.com/addictedabhi/airmock/internal/config"
	"github.com/addictedabhi/airmock/internal/server"
	"github.com/addictedabhi/airmock/internal/storage"
)

var version = "dev"

func main() {
	// A returned error is caught below, but a panic isn't — it unwinds past
	// that check entirely, and Go's default panic handler writes to
	// os.Stderr, same dead end as everywhere else in this file for
	// airmockw.exe. Recover here specifically so a panic anywhere in
	// startup (config parsing, engine construction, anything) still leaves
	// a trace in airmock.log instead of vanishing.
	defer func() {
		if r := recover(); r != nil {
			logFatalToFile(fmt.Errorf("panic: %v\n%s", r, debug.Stack()))
			panic(r) // still crash with the normal Go panic trace/exit code
		}
	}()

	// A bare invocation (no subcommand at all) defaults to "serve" — the
	// Windows Start Menu shortcut and "Launch AirMock now" finish-page
	// action (see build/windows/airmock.nsi) both launch airmockw.exe with
	// no arguments, and a plain double-click of either .exe from the zip
	// release is effectively the same thing. Without this, Cobra's own
	// default for a root command with no RunE of its own is to print help
	// and exit immediately — for airmockw.exe (built -H=windowsgui, no
	// console attached at all) that's not even visible text, just an
	// instant, silent exit indistinguishable from "nothing happened".
	// Every other invocation (airmock version, airmock mocks ..., airmock
	// serve --data-dir ...) is completely unaffected.
	if len(os.Args) == 1 {
		os.Args = append(os.Args, "serve")
	}
	if err := newRootCmd().Execute(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		logFatalToFile(err)
		os.Exit(1)
	}
}

// logFatalToFile is the last-resort net under the per-request logw teeing
// done inside newServeCmd: any error returned before that point exists (e.g.
// config.Load, or a data-dir the process can't create) — or, for
// airmockw.exe specifically, any error at all, since it has no console for
// the os.Stderr line above to reach — would otherwise leave no trace
// whatsoever. Always targets the default ~/.airmock location rather than
// trying to re-resolve a possibly-custom --data-dir from raw os.Args, since
// that's where the install/uninstall instructions already point users to
// look.
func logFatalToFile(err error) {
	home, homeErr := os.UserHomeDir()
	if homeErr != nil {
		return
	}
	dir := filepath.Join(home, ".airmock")
	if mkErr := os.MkdirAll(dir, 0o700); mkErr != nil {
		return
	}
	f, openErr := os.OpenFile(filepath.Join(dir, "airmock.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if openErr != nil {
		return
	}
	defer f.Close()
	fmt.Fprintf(f, "airmock: fatal: %v\n", err)
}

func newRootCmd() *cobra.Command {
	root := &cobra.Command{
		Use:   "airmock",
		Short: "AirMock — a lightweight, self-contained, cross-protocol API mocking tool",
		// Makes "airmock --version" work like "airmock version".
		Version: versionString(),
	}
	root.SetVersionTemplate("airmock {{.Version}}\n")
	root.AddCommand(newServeCmd())
	root.AddCommand(newVersionCmd())
	root.AddCommand(newMocksCmd())
	root.AddCommand(newScheduledEventsCmd())
	return root
}

// Release builds inject these with -ldflags "-X main.commit=... -X
// main.buildDate=..."; a plain `go build` inside the git checkout gets the
// same information from the VCS data the toolchain embeds (see buildInfo).
var (
	commit    = ""
	buildDate = ""
)

// buildInfo resolves the commit, build time and dirty flag of this binary.
func buildInfo() (rev, date string, modified bool) {
	rev, date = commit, buildDate
	if bi, ok := debug.ReadBuildInfo(); ok {
		for _, s := range bi.Settings {
			switch s.Key {
			case "vcs.revision":
				if rev == "" {
					rev = s.Value
				}
			case "vcs.time":
				if date == "" {
					date = s.Value
				}
			case "vcs.modified":
				modified = s.Value == "true"
			}
		}
	}
	if len(rev) > 12 {
		rev = rev[:12]
	}
	return rev, date, modified
}

// versionString is "dev (commit abc123def456, built 2026-10-05T12:00:00Z)",
// with "+dirty" after the commit if the tree had local changes.
func versionString() string {
	rev, date, modified := buildInfo()
	out := version
	var parts []string
	if rev != "" {
		if modified {
			rev += "+dirty"
		}
		parts = append(parts, "commit "+rev)
	}
	if date != "" {
		parts = append(parts, "built "+date)
	}
	if len(parts) > 0 {
		out += " (" + strings.Join(parts, ", ") + ")"
	}
	return out
}

func newVersionCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "version",
		Short: "Print the AirMock version and build",
		RunE: func(cmd *cobra.Command, args []string) error {
			fmt.Println("airmock " + versionString())
			return nil
		},
	}
}

func newServeCmd() *cobra.Command {
	v := viper.New()
	var adminPort, gatewayPort, gatewayTLSPort int
	var dataDir string
	var headless bool
	var adminPassword, adminHost string
	var allowedOrigins []string

	cmd := &cobra.Command{
		Use:   "serve",
		Short: "Start the AirMock admin UI and mock gateway",
		RunE: func(cmd *cobra.Command, args []string) error {
			v.BindPFlag("admin_host", cmd.Flags().Lookup("admin-host"))
			v.BindPFlag("allowed_origins", cmd.Flags().Lookup("allowed-origin"))
			v.BindPFlag("admin_port", cmd.Flags().Lookup("admin-port"))
			v.BindPFlag("gateway_port", cmd.Flags().Lookup("gateway-port"))
			v.BindPFlag("gateway_tls_port", cmd.Flags().Lookup("gateway-tls-port"))
			v.BindPFlag("data_dir", cmd.Flags().Lookup("data-dir"))
			v.BindPFlag("headless", cmd.Flags().Lookup("headless"))
			v.BindPFlag("admin_password", cmd.Flags().Lookup("admin-password"))

			cfg, err := config.Load(v)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			cfg.Version = version
			cfg.Commit, cfg.BuildDate, cfg.Modified = buildInfo()
			if cfg.DataDir == "" {
				home, _ := os.UserHomeDir()
				cfg.DataDir = filepath.Join(home, ".airmock")
			}
			// 0o700: the data dir holds the SQLite DB, which in turn holds
			// plaintext secrets (certificate private keys, SMTP relay
			// credentials, mock/collection auth tokens) — no reason for
			// other local users to even list its contents.
			if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
				return fmt.Errorf("create data dir: %w", err)
			}
			// MkdirAll only applies the mode to a dir it actually creates —
			// tighten it explicitly too, in case it already existed from
			// before this permission was hardened.
			if err := os.Chmod(cfg.DataDir, 0o700); err != nil {
				fmt.Fprintf(os.Stderr, "airmock: warning: failed to restrict data dir permissions: %v\n", err)
			}

			// airmockw.exe (the -H=windowsgui build the Start Menu shortcut
			// and installer finish-page launch) has no console at all, so
			// os.Stderr writes go nowhere — a startup problem (e.g. the
			// auto-open-browser step failing, or the port-conflict below)
			// was previously invisible. Tee output to a log file in the
			// data dir so it's diagnosable without needing cmd.exe.
			logw := io.Writer(os.Stderr)
			if logFile, err := os.OpenFile(filepath.Join(cfg.DataDir, "airmock.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600); err == nil {
				defer logFile.Close()
				logw = io.MultiWriter(os.Stderr, logFile)
			}

			adminURL := fmt.Sprintf("http://%s", net.JoinHostPort(cfg.AdminDialHost(), strconv.Itoa(cfg.AdminPort)))

			// AirMock keeps running in the background after its browser tab
			// is closed (there's no tray icon or window to tie its lifetime
			// to) — so relaunching the Start Menu shortcut or double-
			// clicking the exe again is a very likely second invocation
			// while the first is still alive. Rather than fail on the
			// resulting port conflict (previously silent on the windowsgui
			// build — see above), detect an already-running instance up
			// front and just reopen the browser to it, like double-clicking
			// a shortcut for an app that's already open normally would.
			if conn, err := net.DialTimeout("tcp", net.JoinHostPort(cfg.AdminDialHost(), strconv.Itoa(cfg.AdminPort)), 300*time.Millisecond); err == nil {
				conn.Close()
				fmt.Fprintf(logw, "AirMock is already running at %s, opening browser to it\n", adminURL)
				if !cfg.Headless {
					if err := browser.OpenURL(adminURL); err != nil {
						fmt.Fprintf(logw, "airmock: warning: failed to open browser automatically: %v (open %s manually)\n", err, adminURL)
					}
				}
				return nil
			}

			db, err := storage.Open(cfg.DBPath())
			if err != nil {
				return fmt.Errorf("open storage: %w", err)
			}
			defer db.Close()

			srv, err := server.New(cfg, db)
			if err != nil {
				return fmt.Errorf("init server: %w", err)
			}

			ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
			defer cancel()

			fmt.Fprintf(logw, "AirMock admin UI: %s\n", adminURL)
			if !cfg.Headless {
				go openBrowserWhenReady(logw, net.JoinHostPort(cfg.AdminDialHost(), strconv.Itoa(cfg.AdminPort)), adminURL)
			}
			if err := srv.Run(ctx); err != nil {
				fmt.Fprintf(logw, "airmock: fatal: %v\n", err)
				return err
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&adminHost, "admin-host", "127.0.0.1", "interface the admin UI/API listens on (env: AIRMOCK_ADMIN_HOST); use 0.0.0.0 to reach it from other machines, ideally with --admin-password")
	cmd.Flags().StringSliceVar(&allowedOrigins, "allowed-origin", nil, "extra browser origin allowed to make admin changes, e.g. a reverse proxy's public URL (repeatable)")
	cmd.Flags().IntVar(&adminPort, "admin-port", 8080, "admin UI / API port")
	cmd.Flags().IntVar(&gatewayPort, "gateway-port", 8081, "mock gateway port")
	cmd.Flags().IntVar(&gatewayTLSPort, "gateway-tls-port", 8443, "mock gateway HTTPS port (used only if gateway TLS is enabled)")
	cmd.Flags().StringVar(&dataDir, "data-dir", "", "directory for the database and generated certs (default: ~/.airmock)")
	cmd.Flags().BoolVar(&headless, "headless", false, "do not attempt to open a browser on startup")
	cmd.Flags().StringVar(&adminPassword, "admin-password", "", "require this password to use the admin UI/API (env: AIRMOCK_ADMIN_PASSWORD) — leave unset to disable login entirely")

	return cmd
}

// openBrowserWhenReady polls the admin port until it accepts connections
// (the server starts listening asynchronously inside srv.Run) and then opens
// the default browser, so "just works" installs don't flash a browser tab
// pointed at nothing yet. Failures are written to logw (see the airmock.log
// tee above) rather than ignored, since a stuck or unsupported browser-open
// call previously had no visible symptom at all on the windowsgui build.
func openBrowserWhenReady(logw io.Writer, addr string, url string) {
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			conn.Close()
			if err := browser.OpenURL(url); err != nil {
				fmt.Fprintf(logw, "airmock: warning: failed to open browser automatically: %v (open %s manually)\n", err, url)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintf(logw, "airmock: warning: admin address %s did not accept connections within 10s, did not attempt to open a browser\n", addr)
}
