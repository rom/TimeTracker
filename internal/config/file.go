package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"
)

// Configuration files.
//
// Flags are convenient for one run and awkward for a permanent deployment: a
// systemd unit with fifteen flags on its ExecStart line is unreadable and
// unreviewable. A YAML file gives an operator somewhere to put the settings,
// with comments explaining why each one is set.
//
// The precedence is defaults → file → environment → flags, so a file provides
// the baseline and either of the other two can override it for one run. That
// order is the conventional one and the one people expect; getting it backwards
// (a file that silently overrides an explicit flag) is a classic surprise.

// fileConfig mirrors Config in a shape suited to YAML.
//
// It is a separate type from Config on purpose. Every field is a pointer, so
// "absent from the file" and "present and set to the zero value" are
// distinguishable - without that, a file saying `open_browser: false` could not
// override a default of true, and a file that omits a key would silently reset
// whatever the environment had set.
type fileConfig struct {
	Mode        *string `yaml:"mode"`
	Addr        *string `yaml:"addr"`
	DataDir     *string `yaml:"data_dir"`
	Database    *string `yaml:"database"`
	OpenBrowser *bool   `yaml:"open_browser"`
	TimeZone    *string `yaml:"time_zone"`
	Language    *string `yaml:"language"`

	Log *struct {
		Level   *string `yaml:"level"`
		Format  *string `yaml:"format"`
		Verbose *bool   `yaml:"verbose"`
		Debug   *bool   `yaml:"debug"`
	} `yaml:"log"`

	TLS *struct {
		Cert             *string `yaml:"cert"`
		Key              *string `yaml:"key"`
		RedirectHTTPFrom *string `yaml:"redirect_http_from"`
		HSTSMaxAge       *int    `yaml:"hsts_max_age"`
		SecureCookies    *bool   `yaml:"secure_cookies"`
		AllowInsecure    *bool   `yaml:"allow_insecure"`
	} `yaml:"tls"`

	Security *struct {
		Hardening      *string   `yaml:"hardening"`
		TrustedProxies *[]string `yaml:"trusted_proxies"`
	} `yaml:"security"`

	Syslog *struct {
		Network  *string `yaml:"network"`
		Address  *string `yaml:"address"`
		Facility *int    `yaml:"facility"`
	} `yaml:"syslog"`

	OIDC *struct {
		Issuer       *string            `yaml:"issuer"`
		ClientID     *string            `yaml:"client_id"`
		ClientSecret *string            `yaml:"client_secret"`
		RedirectURL  *string            `yaml:"redirect_url"`
		Label        *string            `yaml:"label"`
		RoleClaim    *string            `yaml:"role_claim"`
		RoleMapping  *map[string]string `yaml:"role_mapping"`
	} `yaml:"oidc"`

	Admin *struct {
		Email    *string `yaml:"email"`
		Password *string `yaml:"password"`
		Name     *string `yaml:"name"`
	} `yaml:"admin"`

	Backup *struct {
		Enabled  *bool   `yaml:"enabled"`
		Dir      *string `yaml:"dir"`
		Interval *string `yaml:"interval"`
		Keep     *int    `yaml:"keep"`
	} `yaml:"backup"`
}

// LoadFile reads a configuration file into cfg.
//
// Unknown keys are an error rather than a warning: a typo in a settings file is
// silent otherwise, and the operator discovers months later that the setting
// they thought they had applied never took effect.
func LoadFile(path string, cfg *Config) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read configuration file %s: %w", path, err)
	}

	var file fileConfig
	decoder := yaml.NewDecoder(strings.NewReader(string(raw)))
	decoder.KnownFields(true)
	if err := decoder.Decode(&file); err != nil {
		return fmt.Errorf("parse configuration file %s: %w", path, err)
	}

	file.applyTo(cfg)
	cfg.ConfigFile = path
	return nil
}

// applyTo copies every field that the file actually set.
func (f fileConfig) applyTo(cfg *Config) {
	setString(&cfg.Mode, f.Mode, func(s string) Mode { return Mode(s) })
	setPlain(&cfg.Addr, f.Addr)
	setPlain(&cfg.DataDir, f.DataDir)
	setPlain(&cfg.DatabasePath, f.Database)
	setPlain(&cfg.OpenBrowser, f.OpenBrowser)
	setPlain(&cfg.TimeZone, f.TimeZone)
	setPlain(&cfg.Language, f.Language)

	if f.Log != nil {
		setPlain(&cfg.LogLevel, f.Log.Level)
		setPlain(&cfg.LogFormat, f.Log.Format)
		setPlain(&cfg.Verbose, f.Log.Verbose)
		setPlain(&cfg.Debug, f.Log.Debug)
	}
	if f.TLS != nil {
		setPlain(&cfg.TLSCertFile, f.TLS.Cert)
		setPlain(&cfg.TLSKeyFile, f.TLS.Key)
		setPlain(&cfg.RedirectHTTPFrom, f.TLS.RedirectHTTPFrom)
		setPlain(&cfg.HSTSMaxAgeSeconds, f.TLS.HSTSMaxAge)
		setPlain(&cfg.ForceSecureCookies, f.TLS.SecureCookies)
		setPlain(&cfg.AllowInsecure, f.TLS.AllowInsecure)
	}
	if f.Security != nil {
		setPlain(&cfg.Hardening, f.Security.Hardening)
		setPlain(&cfg.TrustedProxies, f.Security.TrustedProxies)
	}
	if f.Syslog != nil {
		setPlain(&cfg.SyslogNetwork, f.Syslog.Network)
		setPlain(&cfg.SyslogAddress, f.Syslog.Address)
		setPlain(&cfg.SyslogFacility, f.Syslog.Facility)
	}
	if f.OIDC != nil {
		setPlain(&cfg.OIDCIssuer, f.OIDC.Issuer)
		setPlain(&cfg.OIDCClientID, f.OIDC.ClientID)
		setPlain(&cfg.OIDCClientSecret, f.OIDC.ClientSecret)
		setPlain(&cfg.OIDCRedirectURL, f.OIDC.RedirectURL)
		setPlain(&cfg.OIDCLabel, f.OIDC.Label)
		setPlain(&cfg.OIDCRoleClaim, f.OIDC.RoleClaim)
		setPlain(&cfg.OIDCRoleMapping, f.OIDC.RoleMapping)
	}
	if f.Admin != nil {
		setPlain(&cfg.AdminEmail, f.Admin.Email)
		setPlain(&cfg.AdminPassword, f.Admin.Password)
		setPlain(&cfg.AdminName, f.Admin.Name)
	}
	if f.Backup != nil {
		setPlain(&cfg.BackupEnabled, f.Backup.Enabled)
		setPlain(&cfg.BackupDir, f.Backup.Dir)
		setPlain(&cfg.BackupInterval, f.Backup.Interval)
		setPlain(&cfg.BackupKeep, f.Backup.Keep)
	}
}

// setPlain assigns when the file supplied a value.
func setPlain[T any](target *T, value *T) {
	if value != nil {
		*target = *value
	}
}

// setString assigns a value that needs converting to a named type.
func setString[T any](target *T, value *string, convert func(string) T) {
	if value != nil {
		*target = convert(*value)
	}
}

// DefaultConfigPaths lists where a configuration file is looked for when none is
// named on the command line, most specific first.
//
// A file beside the data directory comes first because that is where a
// single-user installation keeps everything; the system path is for a service.
func DefaultConfigPaths(dataDir string) []string {
	paths := []string{}
	if dataDir != "" {
		paths = append(paths, filepath.Join(dataDir, "timetracker.yaml"))
	}
	if home, err := os.UserHomeDir(); err == nil {
		paths = append(paths, filepath.Join(home, ".config", "timetracker", "config.yaml"))
	}
	paths = append(paths, filepath.Join("/etc", "timetracker", "config.yaml"))
	return paths
}

// findConfigFile returns the first default path that exists.
func findConfigFile(dataDir string) string {
	for _, path := range DefaultConfigPaths(dataDir) {
		if info, err := os.Stat(path); err == nil && !info.IsDir() {
			return path
		}
	}
	return ""
}

// ErrNoConfigFile is returned when an explicitly named file is missing.
//
// An explicitly named file that does not exist is an error, while a missing
// default is not: the operator who typed --config meant that file, and starting
// with defaults instead would run a service configured differently from what
// they asked for.
var ErrNoConfigFile = errors.New("configuration file not found")
