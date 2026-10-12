package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// Configuration resolution.
//
// This package decides where the database lives, what the process listens on,
// and whether it will serve a login form over plain HTTP. It is also the layer
// where a mistake is hardest to see: everything downstream behaves perfectly
// well with the wrong value, and the operator finds out from somebody else.
//
// Two properties carry most of the weight here.
//
// The first is precedence - defaults, then the file, then the environment, then
// the flags. Getting it backwards is the classic configuration surprise: a file
// that silently overrides the flag somebody typed thirty seconds ago while
// trying to debug the very setting it controls. It is asserted at each boundary
// rather than end to end, because only one of the three layers can be wrong at a
// time and a single test would not say which.
//
// The second is that an invalid combination stops the process. A configuration
// error that degrades quietly - half a TLS pair falling back to HTTP, a
// half-configured provider that fails at the first login - is worse than one
// that refuses at startup, because startup is when somebody is watching.

// parseIn resolves configuration with the environment cleared and the data
// directory pointed somewhere disposable.
//
// The environment is cleared rather than inherited: every TT_ variable is a way
// for the machine running the test to change the answer, and a test that passed
// only because a developer's shell had TT_MODE set would be worse than no test.
func parseIn(t *testing.T, dir string, args ...string) (Config, error) {
	t.Helper()

	for _, key := range []string{
		"TT_MODE", "TT_ADDR", "TT_DATA_DIR", "TT_DB", "TT_LOG_LEVEL", "TT_LOG_FORMAT",
		"TT_TZ", "TT_LANG", "TT_TRUSTED_PROXIES", "TT_VERBOSE", "TT_DEBUG",
		"TT_SECURE_COOKIES", "TT_HARDENING", "TT_TLS_CERT", "TT_TLS_KEY",
		"TT_OIDC_ISSUER", "TT_OIDC_CLIENT_ID", "TT_OIDC_CLIENT_SECRET",
		"TT_OIDC_REDIRECT_URL", "TT_OIDC_LABEL", "TT_OIDC_ROLE_CLAIM",
		"TT_OIDC_ROLE_MAPPING", "TT_SYSLOG_NETWORK", "TT_SYSLOG_ADDRESS",
		"TT_ADMIN_EMAIL", "TT_ADMIN_PASSWORD", "TT_ADMIN_NAME",
		"TT_BACKUP_ENABLED", "TT_BACKUP_DIR", "TT_BACKUP_INTERVAL",
	} {
		t.Setenv(key, "")
	}
	t.Setenv("TT_DATA_DIR", dir)
	return Parse(args)
}

// writeConfig writes a configuration file into a fresh directory and returns
// both paths.
func writeConfig(t *testing.T, contents string) (dir, path string) {
	t.Helper()

	dir = t.TempDir()
	path = filepath.Join(dir, "timetracker.yaml")
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatalf("write the configuration file: %v", err)
	}
	return dir, path
}

// TestDefaultsAreASafeLocalInstall.
//
// What somebody gets by running the binary with no arguments: their own machine,
// a loopback address, no login, and everything under one directory.
func TestDefaultsAreASafeLocalInstall(t *testing.T) {
	dir := t.TempDir()

	cfg, err := parseIn(t, dir)
	if err != nil {
		t.Fatalf("parse with no arguments: %v", err)
	}

	if cfg.Mode != ModeLocal {
		t.Errorf("mode = %q, want local", cfg.Mode)
	}
	if !isLoopback(cfg.Addr) {
		t.Errorf("the default address %q is not loopback; local mode has no login",
			cfg.Addr)
	}
	if cfg.LogLevel != "info" || cfg.LogFormat != "text" {
		t.Errorf("default logging = %s/%s, want info/text", cfg.LogLevel, cfg.LogFormat)
	}
	if cfg.Hardening != "off" {
		t.Errorf("hardening defaults to %q; silently sandboxing a process nobody "+
			"asked to sandbox is how an undiagnosable failure starts", cfg.Hardening)
	}
	if cfg.BackupEnabled {
		t.Error("backups default to on: writing copies of somebody's data on a " +
			"schedule they did not ask for is their decision")
	}
	if len(cfg.TrustedProxies) != 0 {
		t.Errorf("trusted proxies default to %v; believing an unchecked "+
			"X-Forwarded-For makes the audit trail record whatever an attacker types",
			cfg.TrustedProxies)
	}
	if cfg.OIDCEnabled() {
		t.Error("single sign-on is on by default")
	}
}

// TestDerivedPathsFollowTheDataDirectory.
//
// One directory to move, back up or delete. Both derived paths are computed
// after the flags are parsed, so pointing --data-dir somewhere moves the
// database and the backups with it - and naming either explicitly still wins.
func TestDerivedPathsFollowTheDataDirectory(t *testing.T) {
	dir := t.TempDir()

	cfg, err := parseIn(t, dir, "--data-dir="+dir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if want := filepath.Join(dir, "timetracker.db"); cfg.DatabasePath != want {
		t.Errorf("database = %q, want %q", cfg.DatabasePath, want)
	}
	if want := filepath.Join(dir, "backups"); cfg.BackupDir != want {
		t.Errorf("backup directory = %q, want %q", cfg.BackupDir, want)
	}

	elsewhere := filepath.Join(t.TempDir(), "somewhere.db")
	cfg, err = parseIn(t, dir, "--data-dir="+dir, "--db="+elsewhere)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.DatabasePath != elsewhere {
		t.Errorf("an explicit --db was overridden by the data directory: %q", cfg.DatabasePath)
	}
}

// TestFlagsBeatTheEnvironmentBeatsTheFile.
//
// The precedence rule, asserted one boundary at a time so a failure says which
// pair is wrong.
//
// The direction matters more than it sounds. An operator debugging a setting
// changes it in the most immediate place they have - the command line - and a
// file that quietly won would make the evidence of their own change disappear.
func TestFlagsBeatTheEnvironmentBeatsTheFile(t *testing.T) {
	dir, path := writeConfig(t, "addr: 127.0.0.1:1111\nlog:\n  level: warn\n")

	fromFile, err := parseIn(t, dir, "--config="+path)
	if err != nil {
		t.Fatalf("parse with a file: %v", err)
	}
	if fromFile.Addr != "127.0.0.1:1111" {
		t.Errorf("the file did not set the address: %q", fromFile.Addr)
	}
	if fromFile.LogLevel != "warn" {
		t.Errorf("the file did not set the log level: %q", fromFile.LogLevel)
	}
	if fromFile.ConfigFile != path {
		t.Errorf("the resolved config records %q as its source, want %q",
			fromFile.ConfigFile, path)
	}

	t.Setenv("TT_ADDR", "127.0.0.1:2222")
	fromEnv, err := Parse([]string{"--config=" + path})
	if err != nil {
		t.Fatalf("parse with a file and the environment: %v", err)
	}
	if fromEnv.Addr != "127.0.0.1:2222" {
		t.Errorf("the environment did not override the file: %q", fromEnv.Addr)
	}
	if fromEnv.LogLevel != "warn" {
		t.Errorf("the environment reset a setting it never mentioned: log level %q",
			fromEnv.LogLevel)
	}

	fromFlag, err := Parse([]string{"--config=" + path, "--addr=127.0.0.1:3333"})
	if err != nil {
		t.Fatalf("parse with all three: %v", err)
	}
	if fromFlag.Addr != "127.0.0.1:3333" {
		t.Errorf("the flag did not override the environment: %q", fromFlag.Addr)
	}
}

// TestAFileCanTurnASettingOff.
//
// Every field in the file type is a pointer so that "absent" and "present and
// false" are different things. Without that, a file could only ever turn
// something on, and `open_browser: false` would be silently ignored - which is
// the shape of bug an operator cannot debug from the outside, because the file
// they are reading says exactly what they want.
func TestAFileCanTurnASettingOff(t *testing.T) {
	dir, path := writeConfig(t, "open_browser: false\n")

	t.Setenv("TT_DATA_DIR", dir)
	on, err := Parse([]string{"--config=" + path, "--open"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if !on.OpenBrowser {
		t.Error("--open was overridden by a file, which has lower precedence")
	}

	off, err := Parse([]string{"--config=" + path})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if off.OpenBrowser {
		t.Error("open_browser: false in a file did not take effect")
	}
}

// TestATypoInTheFileIsAnError.
//
// The whole reason unknown keys are refused. A misspelled key in a settings file
// is otherwise perfectly silent: the file looks right, the setting never
// applies, and the operator finds out months later.
func TestATypoInTheFileIsAnError(t *testing.T) {
	dir, path := writeConfig(t, "adress: 127.0.0.1:9999\n")

	_, err := parseIn(t, dir, "--config="+path)
	if err == nil {
		t.Fatal("a file with a misspelled key was accepted")
	}
	if !strings.Contains(err.Error(), "adress") {
		t.Errorf("the error does not name the key that was wrong: %v", err)
	}
}

// TestAMissingConfigFile.
//
// Two cases with opposite answers, and the difference is whether somebody asked
// for the file. A named file that is not there is a mistake worth stopping for;
// no file at all is the ordinary case for somebody who has never made one.
func TestAMissingConfigFile(t *testing.T) {
	dir := t.TempDir()

	if _, err := parseIn(t, dir, "--config="+filepath.Join(dir, "absent.yaml")); err == nil {
		t.Error("a named configuration file that does not exist was ignored")
	}
	if _, err := parseIn(t, dir); err != nil {
		t.Errorf("no configuration file at all should be fine: %v", err)
	}
}

// TestTheConfigFlagIsFoundBeforeTheFlagsAreParsed.
//
// --config has to be read in an early pass, because the file supplies the
// defaults the other flags override, and Go's flag package cannot express that
// ordering. The hand-rolled pass has to accept every spelling the standard one
// would, or an operator's working command line breaks for no visible reason.
func TestTheConfigFlagIsFoundBeforeTheFlagsAreParsed(t *testing.T) {
	dir, path := writeConfig(t, "addr: 127.0.0.1:4444\n")

	for _, spelling := range [][]string{
		{"--config=" + path},
		{"-config=" + path},
		{"--config", path},
		{"-config", path},
		{"--addr=127.0.0.1:5555", "--config=" + path}, // not first on the line
	} {
		cfg, err := parseIn(t, dir, spelling...)
		if err != nil {
			t.Errorf("%v: %v", spelling, err)
			continue
		}
		if cfg.ConfigFile != path {
			t.Errorf("%v did not find the configuration file", spelling)
		}
	}

	if _, err := configPathFrom([]string{"--config"}); err == nil {
		t.Error("-config with no path after it was accepted")
	}
	// Everything after -- is not ours to interpret.
	if path, err := configPathFrom([]string{"--", "--config=x"}); err != nil || path != "" {
		t.Errorf("configPathFrom read past --: %q, %v", path, err)
	}
}

// TestTheFileIsFoundInTheDataDirectory.
//
// Somebody who has put a file where the data lives should not also have to pass
// a flag naming it. The search is what makes a systemd unit one line instead of
// fifteen.
func TestTheFileIsFoundInTheDataDirectory(t *testing.T) {
	dir, path := writeConfig(t, "addr: 127.0.0.1:6666\n")

	cfg, err := parseIn(t, dir)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if cfg.ConfigFile != path {
		t.Errorf("the file in the data directory was not found: %q", cfg.ConfigFile)
	}
	if cfg.Addr != "127.0.0.1:6666" {
		t.Errorf("the found file was not applied: %q", cfg.Addr)
	}
	if len(DefaultConfigPaths(dir)) == 0 {
		t.Error("DefaultConfigPaths lists nowhere to look")
	}
}

// TestTheEnvironmentCarriesTheSecrets.
//
// A secret on a command line is readable by every other user on the machine
// through the process list, so the environment is the preferred channel for all
// of them. This is the test that they are actually read.
func TestTheEnvironmentCarriesTheSecrets(t *testing.T) {
	dir := t.TempDir()

	t.Setenv("TT_DATA_DIR", dir)
	t.Setenv("TT_MODE", "server")
	t.Setenv("TT_OIDC_ISSUER", "https://login.example.com")
	t.Setenv("TT_OIDC_CLIENT_ID", "timetracker")
	t.Setenv("TT_OIDC_CLIENT_SECRET", "a-secret")
	t.Setenv("TT_OIDC_REDIRECT_URL", "https://tt.example.com/auth/callback")
	t.Setenv("TT_OIDC_ROLE_CLAIM", "groups")
	t.Setenv("TT_OIDC_ROLE_MAPPING", "tt-admins=admin, tt-staff=member")
	t.Setenv("TT_ADMIN_EMAIL", "admin@example.com")
	t.Setenv("TT_ADMIN_PASSWORD", "a-long-enough-password")
	t.Setenv("TT_TRUSTED_PROXIES", "10.0.0.1,10.0.0.2")
	t.Setenv("TT_SECURE_COOKIES", "1")

	cfg, err := Parse([]string{"--addr=0.0.0.0:8420"})
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	if !cfg.OIDCEnabled() {
		t.Error("single sign-on configured entirely through the environment is not enabled")
	}
	if cfg.OIDCClientSecret != "a-secret" {
		t.Errorf("the client secret did not come through: %q", cfg.OIDCClientSecret)
	}
	if got := cfg.OIDCRoleMapping["tt-admins"]; got != "admin" {
		t.Errorf("role mapping tt-admins = %q, want admin (mapping: %v)",
			got, cfg.OIDCRoleMapping)
	}
	// The spacing in the value above is what somebody actually types.
	if got := cfg.OIDCRoleMapping["tt-staff"]; got != "member" {
		t.Errorf("role mapping tt-staff = %q, want member; the value was written "+
			"with a space after the comma", got)
	}
	if len(cfg.TrustedProxies) != 2 {
		t.Errorf("trusted proxies = %v, want two", cfg.TrustedProxies)
	}
	if !cfg.ForceSecureCookies {
		t.Error("TT_SECURE_COOKIES=1 did not take effect")
	}
	if cfg.AdminPassword != "a-long-enough-password" {
		t.Error("the bootstrap password did not come through")
	}
}

// TestVerboseAndDebugAreAppliedLast.
//
// Both are conveniences over the log level, and both have to win over whatever
// set it - somebody who passes --debug while diagnosing a problem should not
// have it silently lose to a `level: warn` in a file. --debug implying --verbose
// is the same reasoning: asking for the harder one and getting less would be
// surprising.
func TestVerboseAndDebugAreAppliedLast(t *testing.T) {
	dir, path := writeConfig(t, "log:\n  level: error\n  format: text\n")

	verbose, err := parseIn(t, dir, "--config="+path, "--verbose")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if verbose.LogLevel != "debug" {
		t.Errorf("--verbose lost to the file: log level %q", verbose.LogLevel)
	}

	debug, err := parseIn(t, dir, "--config="+path, "--debug")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if debug.LogLevel != "debug" {
		t.Errorf("--debug did not imply --verbose: log level %q", debug.LogLevel)
	}
	if debug.LogFormat != "json" {
		t.Errorf("--debug format = %q, want json - it is what somebody attaches to "+
			"a bug report", debug.LogFormat)
	}
}

// TestInvalidConfigurationsAreRefused.
//
// Each of these is a combination that would run. That is the point: none of them
// crashes anything, and every one of them is a decision nobody made.
func TestInvalidConfigurationsAreRefused(t *testing.T) {
	dir := t.TempDir()
	certPEM, keyPEM := generateTestCertificate(t)
	certFile := filepath.Join(dir, "cert.pem")
	keyFile := filepath.Join(dir, "key.pem")
	if err := os.WriteFile(certFile, certPEM, 0o600); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyFile, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	for _, refusal := range []struct {
		name string
		args []string
		want string
	}{
		{
			name: "a mode that does not exist",
			args: []string{"--mode=multiuser"},
			want: "unknown mode",
		},
		{
			name: "a log level that does not exist",
			args: []string{"--log-level=chatty"},
			want: "unknown log level",
		},
		{
			name: "a log format that does not exist",
			args: []string{"--log-format=xml"},
			want: "unknown log format",
		},
		{
			name: "a hardening mode that does not exist",
			args: []string{"--mode=server", "--addr=127.0.0.1:8420", "--hardening=maximum"},
			want: "unknown hardening",
		},
		{
			name: "local mode on a public address",
			args: []string{"--mode=local", "--addr=0.0.0.0:8420"},
			want: "loopback",
		},
		{
			name: "server mode on a public address with no TLS",
			args: []string{"--mode=server", "--addr=0.0.0.0:8420"},
			want: "without TLS",
		},
		{
			name: "half a TLS pair",
			args: []string{"--mode=server", "--addr=127.0.0.1:8420", "--tls-cert=" + certFile},
			want: "both a certificate and a key",
		},
		{
			name: "an HTTP redirect with nothing to redirect to",
			args: []string{"--mode=server", "--addr=127.0.0.1:8420", "--redirect-http-from=:8080"},
			want: "only makes sense",
		},
		{
			name: "half a single sign-on configuration",
			args: []string{"--mode=server", "--addr=127.0.0.1:8420",
				"--oidc-issuer=https://login.example.com"},
			want: "single sign-on needs",
		},
		{
			name: "an empty listen address",
			args: []string{"--addr="},
			want: "listen address is required",
		},
	} {
		t.Run(refusal.name, func(t *testing.T) {
			_, err := parseIn(t, dir, refusal.args...)
			if err == nil {
				t.Fatalf("%s was accepted", refusal.name)
			}
			if !strings.Contains(err.Error(), refusal.want) {
				t.Errorf("refused for the wrong reason: %v (wanted %q)", err, refusal.want)
			}
		})
	}
}

// TestAnOperatorCanOverrideTheTLSRefusal.
//
// The refusal has to have a way out, or somebody terminating TLS at a load
// balancer cannot run the application at all - and the way they would find is to
// stop using server mode, which is worse. Two ways out, because they mean
// different things: --secure-cookies says TLS happens upstream, and
// --allow-insecure says the operator accepts plain HTTP.
func TestAnOperatorCanOverrideTheTLSRefusal(t *testing.T) {
	dir := t.TempDir()

	for _, escape := range []string{"--secure-cookies", "--allow-insecure"} {
		cfg, err := parseIn(t, dir, "--mode=server", "--addr=0.0.0.0:8420", escape)
		if err != nil {
			t.Errorf("%s did not permit a public address: %v", escape, err)
			continue
		}
		if escape == "--secure-cookies" && !cfg.ForceSecureCookies {
			t.Error("--secure-cookies did not set the flag it names")
		}
	}
}

// TestBackupSettingsAreCheckedOnlyWhenBackupsAreOn.
//
// An interval that is not a duration would otherwise be discovered by a timer,
// hours later, in a process nobody is watching. It is checked when backups are
// enabled and left alone when they are not, since the default interval is
// irrelevant to somebody who never turns them on.
func TestBackupSettingsAreCheckedOnlyWhenBackupsAreOn(t *testing.T) {
	dir, path := writeConfig(t, "backup:\n  enabled: true\n  interval: every-day\n")
	if _, err := parseIn(t, dir, "--config="+path); err == nil {
		t.Error("a backup interval that is not a duration was accepted")
	} else if !strings.Contains(err.Error(), "duration") {
		t.Errorf("refused for the wrong reason: %v", err)
	}

	dir, path = writeConfig(t, "backup:\n  enabled: true\n  interval: 6h\n  keep: 0\n")
	if _, err := parseIn(t, dir, "--config="+path); err == nil {
		t.Error("keeping zero backups was accepted, which is backups that delete themselves")
	}

	dir, path = writeConfig(t, "backup:\n  enabled: false\n  interval: nonsense\n")
	if _, err := parseIn(t, dir, "--config="+path); err != nil {
		t.Errorf("a nonsensical interval was refused with backups off: %v", err)
	}
}

// TestAWholeFileRoundTrips.
//
// One file exercising every section, because the per-field plumbing is exactly
// the kind of code where a copied line assigns the wrong field and nothing ever
// notices - the value is present, the type is right, and it lands one struct
// member away from where it was meant to.
func TestAWholeFileRoundTrips(t *testing.T) {
	dir, path := writeConfig(t, `
mode: server
addr: 0.0.0.0:9000
time_zone: Europe/Stockholm
language: sv
log:
  level: warn
  format: json
tls:
  hsts_max_age: 15552000
  secure_cookies: true
security:
  hardening: enforce
  trusted_proxies:
    - 10.0.0.1
    - 10.0.0.2
syslog:
  network: unixgram
  address: /dev/log
  facility: 10
oidc:
  issuer: https://login.example.com
  client_id: timetracker
  client_secret: from-the-file
  redirect_url: https://tt.example.com/auth/callback
  label: Sign in with Entra ID
  role_claim: groups
  role_mapping:
    tt-admins: admin
admin:
  email: admin@example.com
  name: The Administrator
backup:
  enabled: true
  interval: 12h
  keep: 14
`)

	cfg, err := parseIn(t, dir, "--config="+path)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	for _, field := range []struct {
		name string
		got  any
		want any
	}{
		{"mode", cfg.Mode, ModeServer},
		{"addr", cfg.Addr, "0.0.0.0:9000"},
		{"time zone", cfg.TimeZone, "Europe/Stockholm"},
		{"language", cfg.Language, "sv"},
		{"log level", cfg.LogLevel, "warn"},
		{"log format", cfg.LogFormat, "json"},
		{"HSTS max age", cfg.HSTSMaxAgeSeconds, 15552000},
		{"secure cookies", cfg.ForceSecureCookies, true},
		{"hardening", cfg.Hardening, "enforce"},
		{"syslog network", cfg.SyslogNetwork, "unixgram"},
		{"syslog address", cfg.SyslogAddress, "/dev/log"},
		{"syslog facility", cfg.SyslogFacility, 10},
		{"OIDC issuer", cfg.OIDCIssuer, "https://login.example.com"},
		{"OIDC secret", cfg.OIDCClientSecret, "from-the-file"},
		{"OIDC label", cfg.OIDCLabel, "Sign in with Entra ID"},
		{"OIDC role claim", cfg.OIDCRoleClaim, "groups"},
		{"admin email", cfg.AdminEmail, "admin@example.com"},
		{"admin name", cfg.AdminName, "The Administrator"},
		{"backups enabled", cfg.BackupEnabled, true},
		{"backup interval", cfg.BackupInterval, "12h"},
		{"backups kept", cfg.BackupKeep, 14},
	} {
		if field.got != field.want {
			t.Errorf("%s = %v, want %v", field.name, field.got, field.want)
		}
	}
	if len(cfg.TrustedProxies) != 2 || cfg.TrustedProxies[0] != "10.0.0.1" {
		t.Errorf("trusted proxies = %v", cfg.TrustedProxies)
	}
	if cfg.OIDCRoleMapping["tt-admins"] != "admin" {
		t.Errorf("role mapping = %v", cfg.OIDCRoleMapping)
	}
}

// TestLoopbackRecognition.
//
// The predicate behind the refusal that keeps an unauthenticated instance off
// the network. Worth its own table: getting it wrong in the permissive direction
// publishes somebody's timesheet, and every one of these is an address somebody
// really types.
func TestLoopbackRecognition(t *testing.T) {
	for address, want := range map[string]bool{
		"127.0.0.1:8420": true,
		"localhost:8420": true,
		"[::1]:8420":     true,
		"127.0.0.53:53":  true,
		// An empty host binds every interface, so it is not loopback however
		// often it is written that way.
		":8420": false,
		// No port at all is what somebody types when they mean just the host.
		// The bind would fail later; the safety check runs first and must not
		// say yes to a public address on the way past.
		"0.0.0.0":            false,
		"127.0.0.1":          true,
		"0.0.0.0:8420":       false,
		"192.168.1.10:8420":  false,
		"[2001:db8::1]:8420": false,
		"example.com:8420":   false,
		// The one that looks loopback and is not: a name somebody else can
		// register and point at any address they like.
		"127.0.0.1.example.com:8420": false,
	} {
		if got := isLoopback(address); got != want {
			t.Errorf("isLoopback(%q) = %v, want %v", address, got, want)
		}
	}
}

// TestTheDataDirectoryIsPrivate.
//
// It holds a timesheet, which says who somebody works for and what they charge.
// On a shared machine that is not other users' business, and a directory created
// with the process umask would often be readable by all of them.
func TestTheDataDirectoryIsPrivate(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX permissions")
	}
	dir := filepath.Join(t.TempDir(), "nested", "data")

	if err := EnsureDataDir(dir); err != nil {
		t.Fatalf("create the data directory: %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if perm := info.Mode().Perm(); perm != 0o700 {
		t.Errorf("the data directory is %04o, want 0700: it holds a timesheet",
			perm)
	}

	// Creating it twice is what happens on every start after the first.
	if err := EnsureDataDir(dir); err != nil {
		t.Errorf("creating an existing data directory failed: %v", err)
	}
}

// TestTheDefaultDataDirectoryFollowsThePlatform.
//
// Following each platform's convention rather than inventing one is what puts
// the data where a user's existing backup tool already looks.
func TestTheDefaultDataDirectoryFollowsThePlatform(t *testing.T) {
	if runtime.GOOS == "linux" {
		t.Setenv("XDG_DATA_HOME", "/tmp/xdg-example")
		dir, err := DefaultDataDir()
		if err != nil {
			t.Fatalf("default data dir: %v", err)
		}
		if dir != filepath.Join("/tmp/xdg-example", "timetracker") {
			t.Errorf("XDG_DATA_HOME was ignored: %q", dir)
		}
	}

	t.Setenv("XDG_DATA_HOME", "")
	dir, err := DefaultDataDir()
	if err != nil {
		t.Fatalf("default data dir: %v", err)
	}
	if !filepath.IsAbs(dir) {
		t.Errorf("the default data directory %q is not absolute", dir)
	}
	if strings.Contains(dir, "..") {
		t.Errorf("the default data directory is not clean: %q", dir)
	}
}
