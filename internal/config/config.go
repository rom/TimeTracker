// Package config resolves the application's settings from defaults, the
// environment and command-line flags.
//
// Configuration is validated once, at startup, and an invalid combination stops
// the process rather than degrading quietly. A server that starts without a
// session secret, or binds a public address with no TLS in front of it, is a
// configuration error and not a warning to be scrolled past.
package config

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// Mode selects how the application runs. See
// docs/adr/0001-single-binary-two-modes.md.
type Mode string

const (
	// ModeLocal is one person on their own machine: loopback only, no login.
	ModeLocal Mode = "local"
	// ModeServer is a shared instance: authentication, RBAC and rsyslog.
	ModeServer Mode = "server"
)

// Config is the resolved configuration for one run.
type Config struct {
	Mode Mode
	// Addr is the listen address, e.g. "127.0.0.1:8420".
	Addr string
	// DataDir holds the database and, later, the attachment blobs.
	DataDir string
	// DatabasePath is derived from DataDir unless overridden.
	DatabasePath string
	// OpenBrowser launches the user's browser on start. Local mode only.
	OpenBrowser bool
	// LogLevel is one of debug, info, warn, error.
	LogLevel string
	// LogFormat is "text" (readable) or "json" (for a log collector).
	LogFormat string
	// TimeZone is the default IANA zone for a newly created user.
	TimeZone string
	// TrustedProxies lists the addresses whose X-Forwarded-For header may be
	// believed. Empty means believe nobody, which is the safe default: an
	// unchecked forwarded header makes the audit trail record whatever an
	// attacker types.
	TrustedProxies []string

	// ---- server mode only ----

	// TLSCertFile and TLSKeyFile make the server terminate TLS itself. Leave
	// them empty when a reverse proxy does it instead.
	TLSCertFile string
	TLSKeyFile  string
	// RedirectHTTPFrom, when set to an address, runs a second tiny listener that
	// answers plain HTTP with a permanent redirect to the HTTPS address. Nothing
	// else is served there.
	RedirectHTTPFrom string
	// HSTSMaxAgeSeconds sets Strict-Transport-Security. Zero disables it.
	// Deliberately not enabled by default: HSTS is hard to undo, and a
	// misconfigured header can make a host unreachable in a browser for months.
	HSTSMaxAgeSeconds int

	// ForceSecureCookies sets the Secure attribute regardless of how the request
	// arrived. Needed when TLS terminates somewhere that does not set
	// X-Forwarded-Proto.
	ForceSecureCookies bool
	// Hardening selects how hard to try to sandbox the process: off, auto or
	// enforce. It defaults to off, because silently restricting a process's
	// filesystem access and breaking it in a way nobody can diagnose is worse
	// than requiring one flag. The systemd unit in deploy/ turns it on, which is
	// where hardening belongs.
	Hardening string

	// AllowInsecure acknowledges binding a non-loopback address without TLS.
	// Without it the server refuses, because serving a login form over plain
	// HTTP puts passwords on the wire.
	AllowInsecure bool

	// Syslog forwarding. An empty network disables it.
	SyslogNetwork  string
	SyslogAddress  string
	SyslogFacility int

	// Single sign-on. An empty issuer disables it and leaves local accounts as
	// the only way in.
	OIDCIssuer       string
	OIDCClientID     string
	OIDCClientSecret string
	OIDCRedirectURL  string
	// OIDCLabel is the text on the sign-in button, e.g. "Sign in with Entra ID".
	OIDCLabel string
	// OIDCRoleClaim and OIDCRoleMapping map a provider claim onto an application
	// role. Unmapped users get the least privilege.
	OIDCRoleClaim   string
	OIDCRoleMapping map[string]string

	// Bootstrap credentials for the first administrator on an empty instance.
	// Used once and then ignored.
	AdminEmail    string
	AdminPassword string
	AdminName     string
}

// OIDCEnabled reports whether single sign-on is configured.
func (c Config) OIDCEnabled() bool {
	return c.OIDCIssuer != "" && c.OIDCClientID != "" && c.OIDCRedirectURL != ""
}

// Parse resolves configuration from the command line and the environment.
//
// Precedence is defaults, then environment (TT_*), then flags, so an operator can
// set a baseline in a unit file and still override it for one run.
func Parse(args []string) (Config, error) {
	cfg := Config{
		Mode:        ModeLocal,
		Addr:        "127.0.0.1:8420",
		LogLevel:    "info",
		LogFormat:   "text",
		OpenBrowser: false,
		Hardening:   "off",
	}

	defaultDataDir, err := DefaultDataDir()
	if err != nil {
		return Config{}, err
	}
	cfg.DataDir = defaultDataDir

	// Environment first, so flags can override it.
	applyEnv(&cfg)

	fs := flag.NewFlagSet("timetracker", flag.ContinueOnError)
	mode := fs.String("mode", string(cfg.Mode), "run mode: local (single user, no login) or server (multi-user)")
	fs.StringVar(&cfg.Addr, "addr", cfg.Addr, "address to listen on")
	fs.StringVar(&cfg.DataDir, "data-dir", cfg.DataDir, "directory for the database and attachments")
	fs.StringVar(&cfg.DatabasePath, "db", cfg.DatabasePath, "database file (defaults to <data-dir>/timetracker.db)")
	fs.BoolVar(&cfg.OpenBrowser, "open", cfg.OpenBrowser, "open a browser on start (local mode only)")
	fs.StringVar(&cfg.LogLevel, "log-level", cfg.LogLevel, "log level: debug, info, warn, error")
	fs.StringVar(&cfg.LogFormat, "log-format", cfg.LogFormat, "log format: text or json")
	fs.StringVar(&cfg.TimeZone, "tz", cfg.TimeZone, "default IANA time zone for new users")
	proxies := fs.String("trusted-proxies", strings.Join(cfg.TrustedProxies, ","),
		"comma-separated proxy addresses whose X-Forwarded-For header is believed")

	// Server-mode flags. Secrets may also be given through the environment,
	// which is what a systemd unit or a container orchestrator will use - a
	// password on a command line is visible in the process list to every other
	// user on the machine.
	fs.StringVar(&cfg.TLSCertFile, "tls-cert", cfg.TLSCertFile,
		"PEM certificate chain; enables HTTPS when given with -tls-key")
	fs.StringVar(&cfg.TLSKeyFile, "tls-key", cfg.TLSKeyFile,
		"PEM private key for -tls-cert")
	fs.StringVar(&cfg.RedirectHTTPFrom, "redirect-http-from", cfg.RedirectHTTPFrom,
		"also listen here and redirect plain HTTP to the HTTPS address, e.g. :8080")
	fs.IntVar(&cfg.HSTSMaxAgeSeconds, "hsts-max-age", cfg.HSTSMaxAgeSeconds,
		"Strict-Transport-Security max-age in seconds (0 disables; only sent over HTTPS)")
	fs.BoolVar(&cfg.ForceSecureCookies, "secure-cookies", cfg.ForceSecureCookies,
		"always set the Secure cookie attribute (use when TLS terminates upstream)")
	fs.StringVar(&cfg.Hardening, "hardening", cfg.Hardening,
		"process sandboxing: off, auto (apply what the kernel supports), "+
			"or enforce (refuse to start without it)")
	fs.BoolVar(&cfg.AllowInsecure, "allow-insecure", cfg.AllowInsecure,
		"permit binding a public address without TLS in front (not for production)")
	fs.StringVar(&cfg.SyslogNetwork, "syslog-network", cfg.SyslogNetwork,
		"syslog transport: unixgram, unix, tcp, tcp+tls, udp (empty disables forwarding)")
	fs.StringVar(&cfg.SyslogAddress, "syslog-address", cfg.SyslogAddress,
		"syslog socket path or host:port")
	fs.IntVar(&cfg.SyslogFacility, "syslog-facility", cfg.SyslogFacility,
		"syslog facility number (default 10, authpriv)")
	fs.StringVar(&cfg.OIDCIssuer, "oidc-issuer", cfg.OIDCIssuer, "OIDC issuer URL")
	fs.StringVar(&cfg.OIDCClientID, "oidc-client-id", cfg.OIDCClientID, "OIDC client id")
	fs.StringVar(&cfg.OIDCRedirectURL, "oidc-redirect-url", cfg.OIDCRedirectURL,
		"OIDC redirect URL, matching what is registered with the provider")
	fs.StringVar(&cfg.OIDCLabel, "oidc-label", cfg.OIDCLabel, "text on the single sign-on button")

	if err := fs.Parse(args); err != nil {
		return Config{}, err
	}

	cfg.Mode = Mode(*mode)
	if *proxies != "" {
		cfg.TrustedProxies = strings.Split(*proxies, ",")
	}
	if cfg.DatabasePath == "" {
		cfg.DatabasePath = filepath.Join(cfg.DataDir, "timetracker.db")
	}

	if err := cfg.validate(); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// applyEnv reads the TT_* environment variables.
func applyEnv(cfg *Config) {
	if v := os.Getenv("TT_MODE"); v != "" {
		cfg.Mode = Mode(v)
	}
	if v := os.Getenv("TT_ADDR"); v != "" {
		cfg.Addr = v
	}
	if v := os.Getenv("TT_DATA_DIR"); v != "" {
		cfg.DataDir = v
	}
	if v := os.Getenv("TT_DB"); v != "" {
		cfg.DatabasePath = v
	}
	if v := os.Getenv("TT_LOG_LEVEL"); v != "" {
		cfg.LogLevel = v
	}
	if v := os.Getenv("TT_LOG_FORMAT"); v != "" {
		cfg.LogFormat = v
	}
	if v := os.Getenv("TT_TZ"); v != "" {
		cfg.TimeZone = v
	}
	if v := os.Getenv("TT_TRUSTED_PROXIES"); v != "" {
		cfg.TrustedProxies = strings.Split(v, ",")
	}

	// Secrets come from the environment by preference: a value on the command
	// line is readable by every other user on the machine through the process
	// list.
	cfg.OIDCIssuer = envOr("TT_OIDC_ISSUER", cfg.OIDCIssuer)
	cfg.OIDCClientID = envOr("TT_OIDC_CLIENT_ID", cfg.OIDCClientID)
	cfg.OIDCClientSecret = envOr("TT_OIDC_CLIENT_SECRET", cfg.OIDCClientSecret)
	cfg.OIDCRedirectURL = envOr("TT_OIDC_REDIRECT_URL", cfg.OIDCRedirectURL)
	cfg.OIDCLabel = envOr("TT_OIDC_LABEL", cfg.OIDCLabel)
	cfg.OIDCRoleClaim = envOr("TT_OIDC_ROLE_CLAIM", cfg.OIDCRoleClaim)
	cfg.SyslogNetwork = envOr("TT_SYSLOG_NETWORK", cfg.SyslogNetwork)
	cfg.SyslogAddress = envOr("TT_SYSLOG_ADDRESS", cfg.SyslogAddress)
	cfg.AdminEmail = envOr("TT_ADMIN_EMAIL", cfg.AdminEmail)
	cfg.AdminPassword = envOr("TT_ADMIN_PASSWORD", cfg.AdminPassword)
	cfg.AdminName = envOr("TT_ADMIN_NAME", cfg.AdminName)
	cfg.TLSCertFile = envOr("TT_TLS_CERT", cfg.TLSCertFile)
	cfg.TLSKeyFile = envOr("TT_TLS_KEY", cfg.TLSKeyFile)
	cfg.Hardening = envOr("TT_HARDENING", cfg.Hardening)

	if v := os.Getenv("TT_OIDC_ROLE_MAPPING"); v != "" {
		// "group-name=role,other-group=role"
		cfg.OIDCRoleMapping = map[string]string{}
		for _, pair := range strings.Split(v, ",") {
			if name, role, ok := strings.Cut(strings.TrimSpace(pair), "="); ok {
				cfg.OIDCRoleMapping[name] = role
			}
		}
	}
	if os.Getenv("TT_SECURE_COOKIES") == "1" {
		cfg.ForceSecureCookies = true
	}
}

// envOr returns the environment value when set, otherwise the current value.
func envOr(key, current string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return current
}

// validate rejects combinations that would be unsafe or nonsensical.
func (c Config) validate() error {
	switch c.Mode {
	case ModeLocal, ModeServer:
	default:
		return fmt.Errorf("unknown mode %q: expected 'local' or 'server'", c.Mode)
	}

	switch c.LogLevel {
	case "debug", "info", "warn", "error":
	default:
		return fmt.Errorf("unknown log level %q", c.LogLevel)
	}

	switch c.LogFormat {
	case "text", "json":
	default:
		return fmt.Errorf("unknown log format %q: expected 'text' or 'json'", c.LogFormat)
	}

	switch c.Hardening {
	case "off", "auto", "enforce", "":
	default:
		return fmt.Errorf("unknown hardening mode %q: expected 'off', 'auto' or 'enforce'",
			c.Hardening)
	}

	if c.Addr == "" {
		return fmt.Errorf("listen address is required")
	}

	// Local mode is unauthenticated by design, which is only defensible while the
	// socket is unreachable from the network. Binding it anywhere else would
	// publish someone's timesheet to their whole network with no login, so it is
	// refused rather than warned about.
	if c.Mode == ModeLocal && !isLoopback(c.Addr) {
		return fmt.Errorf(
			"local mode has no authentication and may only bind a loopback address; "+
				"%q is not loopback - use --mode=server for a shared instance", c.Addr)
	}

	// Half a TLS configuration is a configuration error, not a reason to fall
	// back to plain HTTP silently.
	if (c.TLSCertFile == "") != (c.TLSKeyFile == "") {
		return fmt.Errorf("TLS needs both a certificate and a key; only one was given")
	}
	if c.RedirectHTTPFrom != "" && !c.TLSConfigured() {
		return fmt.Errorf("-redirect-http-from only makes sense when this process serves HTTPS")
	}

	if c.Mode == ModeServer {
		// A login form served over plain HTTP puts every password on the wire in
		// clear. Refusing is the only defensible default; an operator who really
		// is terminating TLS somewhere this cannot detect says so explicitly.
		if !isLoopback(c.Addr) && !c.AllowInsecure && !c.ForceSecureCookies && !c.TLSConfigured() {
			return fmt.Errorf(
				"refusing to serve %q without TLS: pass --tls-cert and --tls-key "+
					"(see scripts/gen-cert.sh), or terminate TLS in front of this "+
					"process and pass --secure-cookies, or pass --allow-insecure "+
					"if you accept sending passwords in clear", c.Addr)
		}
		// A partially configured provider fails at the moment a user tries to
		// sign in, which is the worst time to discover it.
		anyOIDC := c.OIDCIssuer != "" || c.OIDCClientID != "" || c.OIDCRedirectURL != ""
		if anyOIDC && !c.OIDCEnabled() {
			return fmt.Errorf(
				"single sign-on needs an issuer, a client id and a redirect URL; " +
					"one or more is missing")
		}
	}
	return nil
}

// isWindows is a variable rather than a direct runtime check so tests can
// exercise both branches of the key-permission check.
var isWindows = func() bool { return runtime.GOOS == "windows" }

// isLoopback reports whether the address binds only the loopback interface.
func isLoopback(addr string) bool {
	host := addr
	if i := strings.LastIndex(addr, ":"); i >= 0 {
		host = addr[:i]
	}
	host = strings.Trim(host, "[]")
	switch host {
	case "", "localhost", "127.0.0.1", "::1":
		return true
	default:
		// Any 127.x.x.x address is loopback.
		return strings.HasPrefix(host, "127.")
	}
}

// DefaultDataDir returns the conventional per-platform location for application
// data. Following each platform's convention rather than inventing one means the
// data ends up where a user's backup tool already looks (ASR-002).
func DefaultDataDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("locate home directory: %w", err)
	}

	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "TimeTracker"), nil
	case "windows":
		// LOCALAPPDATA is the right home for per-machine application data; it is
		// excluded from roaming profiles, which a database file should be.
		if dir := os.Getenv("LOCALAPPDATA"); dir != "" {
			return filepath.Join(dir, "TimeTracker"), nil
		}
		return filepath.Join(home, "AppData", "Local", "TimeTracker"), nil
	default:
		// XDG on Linux and the BSDs.
		if dir := os.Getenv("XDG_DATA_HOME"); dir != "" {
			return filepath.Join(dir, "timetracker"), nil
		}
		return filepath.Join(home, ".local", "share", "timetracker"), nil
	}
}

// EnsureDataDir creates the data directory if it does not exist.
//
// The mode is 0700: this directory holds a timesheet, which says who a person
// works for and what they charge. Other users on a shared machine have no
// business reading it.
func EnsureDataDir(dir string) error {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("create data directory %s: %w", dir, err)
	}
	return nil
}
