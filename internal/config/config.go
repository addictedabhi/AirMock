package config

import (
	"net"
	"path/filepath"
	"strings"

	"github.com/spf13/viper"
)

type Config struct {
	// AdminHost is the interface the admin UI/API binds to. Loopback by
	// default so a fresh `airmock serve` is not reachable from the network;
	// use 0.0.0.0 (or a specific address) to expose it, ideally with admin
	// login enabled.
	AdminHost string `mapstructure:"admin_host"`
	// AllowedOrigins are extra browser origins (e.g. a reverse proxy's
	// public URL) permitted to make state-changing admin API calls; see
	// api.SameOriginGuard.
	AllowedOrigins []string `mapstructure:"allowed_origins"`
	AdminPort      int      `mapstructure:"admin_port"`
	GatewayPort    int      `mapstructure:"gateway_port"`
	GatewayTLSPort int      `mapstructure:"gateway_tls_port"`
	DataDir        string   `mapstructure:"data_dir"`
	Headless       bool     `mapstructure:"headless"`
	// AdminPassword, when set, requires a login (see internal/auth) before
	// the admin UI/API will do anything — left empty (the default), auth
	// is off entirely, matching every prior release's zero-friction local
	// behavior. AutomaticEnv below maps this to AIRMOCK_ADMIN_PASSWORD.
	AdminPassword string `mapstructure:"admin_password"`
	// Version is set programmatically by cmd/airmock's main.go, after Load
	// returns, from the ldflags-injected build version — there's no
	// "version" viper key/flag/env var; this is never meant to come from
	// CLI/env configuration.
	Version string `mapstructure:"-"`
	// Commit/BuildDate/Modified identify the exact build (from the VCS info
	// the Go toolchain embeds, or release ldflags); set by main.go.
	Commit    string `mapstructure:"-"`
	BuildDate string `mapstructure:"-"`
	Modified  bool   `mapstructure:"-"`
}

func Load(v *viper.Viper) (*Config, error) {
	v.SetDefault("admin_host", "127.0.0.1")
	v.SetDefault("admin_port", 8080)
	v.SetDefault("gateway_port", 8081)
	v.SetDefault("gateway_tls_port", 8443)
	v.SetDefault("headless", false)

	v.SetEnvPrefix("AIRMOCK")
	v.AutomaticEnv()

	cfg := &Config{}
	if err := v.Unmarshal(cfg); err != nil {
		return nil, err
	}
	return cfg, nil
}

func (c *Config) DBPath() string {
	return filepath.Join(c.DataDir, "airmock.db")
}

// IsLoopbackHost reports whether host only accepts connections from the
// local machine.
func IsLoopbackHost(host string) bool {
	h := strings.Trim(host, "[]")
	if strings.EqualFold(h, "localhost") {
		return true
	}
	ip := net.ParseIP(h)
	return ip != nil && ip.IsLoopback()
}

// AdminDialHost is the host to use when connecting to this instance's own
// admin port (browser URL, "already running" probe): localhost for loopback
// and wildcard binds, otherwise the specific address bound.
func (c *Config) AdminDialHost() string {
	h := strings.Trim(c.AdminHost, "[]")
	if h == "" || IsLoopbackHost(h) {
		return "localhost"
	}
	if ip := net.ParseIP(h); ip != nil && ip.IsUnspecified() {
		return "localhost"
	}
	return h
}
