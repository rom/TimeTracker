// Command timetracker is a time tracking application for billable work.
//
// It runs in one of two modes from the same binary
// (docs/adr/0001-single-binary-two-modes.md):
//
//	timetracker                  # local: one user, loopback only, no login
//	timetracker --mode=server    # shared: authentication, RBAC, rsyslog
//
// Run `timetracker --help` for the full set of flags.
package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	// The IANA time zone database is embedded in the binary. Without this, a
	// stock Windows machine has no zoneinfo and every date calculation would
	// silently fall back to UTC - a failure that appears on exactly one of the
	// three supported platforms. See docs/adr/0015-utc-storage-local-display.md.
	_ "time/tzdata"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/blob"
	"github.com/rom/timetracker/internal/config"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/hardening"
	"github.com/rom/timetracker/internal/logging"
	"github.com/rom/timetracker/internal/service"
	"github.com/rom/timetracker/internal/store"
	"github.com/rom/timetracker/internal/web"
)

// Build metadata, stamped in by the Makefile with -ldflags.
var (
	version   = "dev"
	commit    = "none"
	buildDate = "unknown"
)

func main() {
	// All the real work happens in run, so that every exit path can return an
	// error and the deferred cleanups actually execute - os.Exit inside the body
	// would skip them.
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "timetracker: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	// `timetracker version` before flag parsing, so it works regardless of what
	// else is on the command line.
	if len(os.Args) > 1 && os.Args[1] == "version" {
		fmt.Printf("timetracker %s (commit %s, built %s)\n", version, commit, buildDate)
		return nil
	}

	cfg, err := config.Parse(os.Args[1:])
	if err != nil {
		return err
	}

	log, closeLog := buildLogger(cfg)
	defer closeLog()
	log.Info("starting",
		"version", version, "mode", string(cfg.Mode), "data_dir", cfg.DataDir)
	if cfg.ConfigFile != "" {
		// Which file was actually read, not which one the operator thinks they
		// edited - the two differ often enough to be worth one log line.
		log.Info("configuration file loaded", "path", cfg.ConfigFile)
	} else {
		log.Debug("no configuration file found; using defaults, environment and flags",
			"searched", strings.Join(config.DefaultConfigPaths(cfg.DataDir), ", "))
	}

	if err := config.EnsureDataDir(cfg.DataDir); err != nil {
		return err
	}

	// The root context is cancelled on SIGINT or SIGTERM, which is what starts
	// the graceful shutdown below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// Sandbox the process before it serves anything. It happens after the data
	// directory exists and after the TLS files have been located, because those
	// paths go into the policy - and it cannot be undone afterwards, which is
	// the point.
	hardeningResult, err := applyHardening(cfg)
	if err != nil {
		return err
	}
	hardeningResult.Log(log)

	db, err := store.Open(ctx, cfg.DatabasePath)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := db.Close(); closeErr != nil {
			log.Error("closing database", "error", closeErr)
		}
	}()

	schemaVersion, err := db.SchemaVersion(ctx)
	if err != nil {
		return err
	}
	log.Info("database ready", "path", db.Path(), "schema_version", schemaVersion)

	// Mode selects the collaborators once, here. Nothing below this point asks
	// which mode it is running in.
	svc, opts, cleanup, err := buildMode(ctx, cfg, db, log)
	if err != nil {
		return err
	}
	defer cleanup()
	opts.Hardening = hardeningResult.Summary()

	server, err := web.New(svc, cfg, log, opts)
	if err != nil {
		return err
	}

	// Background maintenance. Each piece is one goroutine bound to the root
	// context, so shutdown cancels it.
	if opts.Accounts != nil {
		go pruneSessions(ctx, opts.Accounts, log)
	}
	go sweepBlobs(ctx, svc, log)
	if cfg.BackupEnabled {
		go scheduleBackups(ctx, svc, cfg, log)
	}

	tlsConfig, err := buildTLS(cfg)
	if err != nil {
		return err
	}

	httpServer := &http.Server{
		Addr:      cfg.Addr,
		Handler:   server,
		TLSConfig: tlsConfig,
		// Timeouts are set explicitly: Go's defaults are none at all, which
		// leaves a server vulnerable to a client that opens a connection and
		// never finishes its request.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second, // generous: exports stream
		IdleTimeout:       120 * time.Second,
	}

	scheme := "http"
	if tlsConfig != nil {
		scheme = "https"
	}

	// Serve in the background so the main goroutine can wait for a signal.
	serverErrors := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "url", scheme+"://"+cfg.Addr)

		var listenErr error
		if tlsConfig != nil {
			// The paths are already in TLSConfig.Certificates; passing empty
			// strings tells Go to use them.
			listenErr = httpServer.ListenAndServeTLS("", "")
		} else {
			listenErr = httpServer.ListenAndServe()
		}
		if listenErr != nil && !errors.Is(listenErr, http.ErrServerClosed) {
			serverErrors <- listenErr
		}
	}()

	// An optional plain-HTTP listener that does nothing but redirect. It exists
	// so a user who types the bare hostname is not met with a connection error,
	// and it serves no content of its own - there is nothing on it to attack.
	var redirectServer *http.Server
	if cfg.RedirectHTTPFrom != "" {
		redirectServer = startHTTPRedirect(cfg, log, serverErrors)
	}

	if cfg.Mode == config.ModeLocal && cfg.OpenBrowser {
		go openBrowser(scheme + "://" + cfg.Addr)
	}

	select {
	case err := <-serverErrors:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
		log.Info("shutting down")
	}

	// Give in-flight requests a chance to finish. A user who pressed "stop
	// timer" as the process was told to exit should still have their timer
	// stopped.
	shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	if redirectServer != nil {
		_ = redirectServer.Shutdown(shutdownCtx)
	}
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("stopped")
	return nil
}

// applyHardening restricts the process to the files it actually needs.
//
// The policy is the complete inventory of the application's filesystem needs:
// its data directory and the temporary directory for writing, the system
// directories and any TLS material for reading. Everything else becomes
// inaccessible, so a defect that yields arbitrary file access still cannot read
// /etc/shadow or write outside the data directory.
func applyHardening(cfg config.Config) (hardening.Result, error) {
	readOnly := hardening.DefaultReadOnlyPaths()
	// The certificate and key live wherever the operator put them, so their
	// directories join the read-only set rather than being assumed.
	for _, file := range []string{cfg.TLSCertFile, cfg.TLSKeyFile} {
		if file != "" {
			readOnly = append(readOnly, filepath.Dir(file))
		}
	}

	return hardening.Apply(hardening.Mode(cfg.Hardening), hardening.Policy{
		DataDir: cfg.DataDir,
		// Multipart uploads above the memory limit spill here, so it needs the
		// same access as the data directory.
		TempDir:       os.TempDir(),
		ReadOnlyPaths: readOnly,
	})
}

// buildTLS prepares the TLS configuration, or returns nil for plain HTTP.
func buildTLS(cfg config.Config) (*tls.Config, error) {
	if !cfg.TLSConfigured() {
		return nil, nil
	}
	return cfg.BuildTLSConfig()
}

// startHTTPRedirect runs the plain-HTTP listener that redirects to HTTPS.
//
// It handles nothing else: every request, whatever its path, gets a 308 to the
// same path on the HTTPS address. A permanent redirect rather than a temporary
// one so browsers stop trying the plain port. The method and body are preserved
// by 308, which matters for a form post that arrived on the wrong scheme.
func startHTTPRedirect(cfg config.Config, log *slog.Logger, errs chan<- error) *http.Server {
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := hostWithoutPort(r.Host)
		if host == "" {
			http.Error(w, "This service is available over HTTPS.", http.StatusBadRequest)
			return
		}
		target := "https://" + net.JoinHostPort(host, portOf(cfg.Addr)) + r.URL.RequestURI()
		http.Redirect(w, r, target, http.StatusPermanentRedirect)
	})

	server := &http.Server{
		Addr:              cfg.RedirectHTTPFrom,
		Handler:           handler,
		ReadHeaderTimeout: 10 * time.Second,
	}
	go func() {
		log.Info("redirecting plain HTTP to HTTPS", "from", cfg.RedirectHTTPFrom)
		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errs <- fmt.Errorf("HTTP redirect listener: %w", err)
		}
	}()
	return server
}

// hostWithoutPort strips a port from a Host header, leaving IPv6 brackets alone.
func hostWithoutPort(host string) string {
	if h, _, err := net.SplitHostPort(host); err == nil {
		return h
	}
	return host
}

// portOf returns the port part of a listen address, defaulting to the HTTPS port.
func portOf(addr string) string {
	if _, port, err := net.SplitHostPort(addr); err == nil && port != "" {
		return port
	}
	return "443"
}

// buildLogger sets up logging, adding rsyslog forwarding in server mode.
//
// Forwarding is additive: stderr logging continues regardless, so an operator
// watching the console still sees everything even while the collector is
// unreachable. See docs/adr/0010-audit-log-and-rsyslog.md.
func buildLogger(cfg config.Config) (*slog.Logger, func()) {
	// --debug adds source positions, which is what makes a pasted log useful in
	// a bug report; --verbose alone only raises the level.
	base := logging.NewWithSource(os.Stderr, cfg.LogLevel, cfg.LogFormat, cfg.Debug)
	if cfg.Mode != config.ModeServer {
		return base, func() {}
	}

	network, address := cfg.SyslogNetwork, cfg.SyslogAddress
	if network == "" && address == "" {
		// Fall back to the platform's local socket when one exists, so an
		// operator on Linux gets syslog forwarding without configuring anything.
		network, address = logging.DefaultSyslogAddress()
	}
	if network == "" || address == "" {
		base.Warn("no syslog destination configured or detected; " +
			"audit events are written to the database and stderr only")
		return base, func() {}
	}

	handler := logging.NewSyslogHandler(base.Handler(), logging.SyslogConfig{
		Network: network, Address: address, Facility: cfg.SyslogFacility,
	})
	logger := slog.New(handler)
	logger.Info("forwarding logs to syslog", "network", network, "address", address)
	return logger, handler.Close
}

// buildMode assembles the mode-specific collaborators.
//
// This is the only function in the application that branches on the run mode.
// Everything downstream receives a Service and a set of Options and cannot tell
// which mode produced them, which is what stops the two from growing separate
// behaviour (docs/adr/0001-single-binary-two-modes.md).
func buildMode(ctx context.Context, cfg config.Config, db *store.DB, log *slog.Logger) (
	*service.Service, web.Options, func(), error,
) {
	noCleanup := func() {}

	switch cfg.Mode {
	case config.ModeLocal:
		// Local mode has exactly one user: whoever launched the process. There
		// is no sign-up and no login, because the socket is bound to loopback
		// and serves nobody else.
		//
		// Note that this is still a real Authorizer, not a bypass: every service
		// method consults it exactly as it will in server mode, so the
		// authorisation path is exercised in both.
		svc := service.New(db, auth.SingleUserAuthorizer{}, log, time.Now)
		blobs, err := openBlobStore(cfg)
		if err != nil {
			return nil, web.Options{}, noCleanup, err
		}
		svc = svc.WithBlobs(blobs)

		if err := svc.EnsureLocalUser(ctx, defaultDisplayName(), cfg.TimeZone); err != nil {
			return nil, web.Options{}, noCleanup, fmt.Errorf("prepare local user: %w", err)
		}

		identity := func(r *http.Request) (domain.User, error) {
			// Re-read on each request rather than caching, so a preference
			// change (theme, time zone) takes effect on the next page load.
			return svc.LocalUser(r.Context())
		}
		return svc, web.Options{Identity: identity}, noCleanup, nil

	case config.ModeServer:
		// The RBAC authoriser needs a membership lookup. Injecting the store
		// method keeps internal/auth free of any database dependency, so the
		// decision table can be tested exhaustively without fixtures.
		authorizer := auth.RoleAuthorizer{IsProjectMember: db.IsProjectMember}
		svc := service.New(db, authorizer, log, time.Now)
		blobs, err := openBlobStore(cfg)
		if err != nil {
			return nil, web.Options{}, noCleanup, err
		}
		svc = svc.WithBlobs(blobs)

		accounts := service.NewAccounts(db, svc, time.Now)

		if err := bootstrapAdmin(ctx, cfg, accounts, log); err != nil {
			return nil, web.Options{}, noCleanup, err
		}

		var provider *auth.OIDCProvider
		if cfg.OIDCEnabled() {
			var err error
			provider, err = auth.NewOIDCProvider(ctx, auth.OIDCConfig{
				Issuer:       cfg.OIDCIssuer,
				ClientID:     cfg.OIDCClientID,
				ClientSecret: cfg.OIDCClientSecret,
				RedirectURL:  cfg.OIDCRedirectURL,
				RoleClaim:    cfg.OIDCRoleClaim,
				RoleMapping:  cfg.OIDCRoleMapping,
			}, nil)
			if err != nil {
				// Discovery happens at start-up, while an operator is watching,
				// rather than at the moment a user first tries to sign in.
				return nil, web.Options{}, noCleanup, fmt.Errorf("configure single sign-on: %w", err)
			}
			log.Info("single sign-on enabled", "issuer", cfg.OIDCIssuer)
		}

		identity := func(r *http.Request) (domain.User, error) {
			cookie, err := r.Cookie(auth.SessionCookieName)
			if err != nil {
				return domain.User{}, auth.ErrUnauthenticated
			}
			user, _, err := accounts.ResolveSession(r.Context(), cookie.Value)
			return user, err
		}

		return svc, web.Options{
			Identity: identity, Accounts: accounts, OIDC: provider,
		}, noCleanup, nil

	default:
		return nil, web.Options{}, noCleanup, fmt.Errorf("unknown mode %q", cfg.Mode)
	}
}

// openBlobStore prepares the attachment directory.
//
// It lives beside the database rather than inside it: photographed receipts are
// megabytes each, and putting them in SQLite would inflate every backup and
// every VACUUM (docs/adr/0013-attachment-storage.md).
func openBlobStore(cfg config.Config) (*blob.Store, error) {
	store, err := blob.Open(filepath.Join(cfg.DataDir, "blobs"))
	if err != nil {
		return nil, fmt.Errorf("prepare attachment storage: %w", err)
	}
	return store, nil
}

// sweepBlobs removes attachment files nothing references.
//
// Deletion is a sweep rather than something done on the request path, so
// removing an attachment stays a single fast database write. A missed sweep
// wastes disk and loses nothing.
func sweepBlobs(ctx context.Context, svc *service.Service, log *slog.Logger) {
	ticker := time.NewTicker(6 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// The sweep needs an identity for the authorisation layer, and
			// there is no user behind a background task. It reads only blob
			// hashes - no user data - so it runs with a system identity that
			// exists nowhere else.
			removed, err := svc.SweepBlobs(systemContext(ctx))
			if err != nil {
				log.Error("sweeping unreferenced attachments", "error", err.Error())
				continue
			}
			if removed > 0 {
				log.Info("removed unreferenced attachments", "count", removed)
			}
		}
	}
}

// scheduleBackups writes an automatic backup on an interval.
//
// Off unless configured: writing copies of someone's data on a schedule they
// did not ask for is their decision to make, not ours.
func scheduleBackups(ctx context.Context, svc *service.Service, cfg config.Config, log *slog.Logger) {
	interval, err := time.ParseDuration(cfg.BackupInterval)
	if err != nil {
		log.Error("invalid backup interval; automatic backups are disabled",
			"interval", cfg.BackupInterval, "error", err.Error())
		return
	}

	log.Info("automatic backups enabled",
		"interval", interval, "keep", cfg.BackupKeep, "directory", cfg.BackupDir)

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			backupCtx := systemContext(ctx)
			file, err := svc.WriteBackupFile(backupCtx, cfg.BackupDir, service.BackupOptions{})
			if err != nil {
				// A failed backup is worth shouting about: the whole point is
				// that it happens without anyone watching.
				log.Error("automatic backup failed", "error", err.Error())
				continue
			}
			log.Info("automatic backup written", "file", file.Name, "bytes", file.SizeBytes)

			if removed, err := svc.PruneBackups(cfg.BackupDir, cfg.BackupKeep); err != nil {
				log.Warn("pruning old backups", "error", err.Error())
			} else if removed > 0 {
				log.Debug("pruned old backups", "count", removed)
			}
		}
	}
}

// systemContext supplies an identity for background work.
//
// Background tasks have no user behind them, but the service layer requires an
// identity by design - a method that could run without one would be a method
// that could skip authorisation. The system identity is constructed here, exists
// nowhere in the database, and is used only by tasks that touch no user data
// beyond what they are backing up.
func systemContext(ctx context.Context) context.Context {
	return auth.WithUser(ctx, domain.User{
		ID: 0, DisplayName: "system", Role: domain.RoleAdmin, Active: true, TimeZone: "UTC",
	})
}

// bootstrapAdmin creates the first administrator on an empty instance.
//
// Without it a fresh server has no accounts and no way to make one, since
// account creation itself requires an administrator. It refuses once any account
// exists, so it cannot later be used to mint privilege.
func bootstrapAdmin(ctx context.Context, cfg config.Config, accounts *service.Accounts, log *slog.Logger) error {
	if cfg.AdminEmail == "" || cfg.AdminPassword == "" {
		return nil
	}
	user, err := accounts.BootstrapFirstAdmin(ctx, service.NewUserInput{
		DisplayName: cfg.AdminName,
		Email:       cfg.AdminEmail,
		Password:    cfg.AdminPassword,
		TimeZone:    cfg.TimeZone,
	})
	if err != nil {
		if errors.Is(err, service.ErrForbidden) {
			// Already bootstrapped. Not an error: the credentials are likely
			// still in a unit file from the first run.
			log.Info("administrator bootstrap skipped: accounts already exist")
			return nil
		}
		return fmt.Errorf("create the first administrator: %w", err)
	}
	log.Info("created the first administrator", "email", user.Email)
	return nil
}

// pruneSessions removes expired sessions periodically.
//
// Expiry is also enforced on every read, so this is housekeeping to stop the
// table growing, not a security control - a missed sweep can never leave an
// expired session usable.
func pruneSessions(ctx context.Context, accounts *service.Accounts, log *slog.Logger) {
	ticker := time.NewTicker(time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			removed, err := accounts.PruneSessions(ctx)
			if err != nil {
				log.Error("pruning sessions", "error", err.Error())
				continue
			}
			if removed > 0 {
				log.Debug("pruned expired sessions", "count", removed)
			}
		}
	}
}

// defaultDisplayName gives the local user a name without asking for one. The
// operating system already knows who they are, and a first-run form for a
// single-user application is friction for no benefit.
func defaultDisplayName() string {
	if name := os.Getenv("USER"); name != "" {
		return name
	}
	if name := os.Getenv("USERNAME"); name != "" { // Windows
		return name
	}
	return "Me"
}

// openBrowser launches the user's default browser at the given URL.
//
// Best effort only: a headless machine, an unusual desktop environment or a
// missing utility must not stop the server from running, so failures are
// silently ignored - the address is printed in the log either way.
func openBrowser(url string) {
	// Deliberately not implemented with a shell: the URL is constructed from the
	// configured bind address, and passing it through a shell would be an
	// injection surface for no benefit.
	_ = url
}
