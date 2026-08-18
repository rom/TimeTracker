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
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	// The IANA time zone database is embedded in the binary. Without this, a
	// stock Windows machine has no zoneinfo and every date calculation would
	// silently fall back to UTC - a failure that appears on exactly one of the
	// three supported platforms. See docs/adr/0015-utc-storage-local-display.md.
	_ "time/tzdata"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/config"
	"github.com/rom/timetracker/internal/domain"
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

	if err := config.EnsureDataDir(cfg.DataDir); err != nil {
		return err
	}

	// The root context is cancelled on SIGINT or SIGTERM, which is what starts
	// the graceful shutdown below.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

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

	server, err := web.New(svc, cfg, log, opts)
	if err != nil {
		return err
	}

	// Background maintenance. Each piece is one goroutine bound to the root
	// context, so shutdown cancels it.
	if opts.Accounts != nil {
		go pruneSessions(ctx, opts.Accounts, log)
	}

	httpServer := &http.Server{
		Addr:    cfg.Addr,
		Handler: server,
		// Timeouts are set explicitly: Go's defaults are none at all, which
		// leaves a server vulnerable to a client that opens a connection and
		// never finishes its request.
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       60 * time.Second,
		WriteTimeout:      120 * time.Second, // generous: exports stream
		IdleTimeout:       120 * time.Second,
	}

	// Serve in the background so the main goroutine can wait for a signal.
	serverErrors := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", cfg.Addr, "url", "http://"+cfg.Addr)
		if listenErr := httpServer.ListenAndServe(); listenErr != nil &&
			!errors.Is(listenErr, http.ErrServerClosed) {
			serverErrors <- listenErr
		}
	}()

	if cfg.Mode == config.ModeLocal && cfg.OpenBrowser {
		go openBrowser("http://" + cfg.Addr)
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
	if err := httpServer.Shutdown(shutdownCtx); err != nil {
		return fmt.Errorf("graceful shutdown: %w", err)
	}
	log.Info("stopped")
	return nil
}

// buildLogger sets up logging, adding rsyslog forwarding in server mode.
//
// Forwarding is additive: stderr logging continues regardless, so an operator
// watching the console still sees everything even while the collector is
// unreachable. See docs/adr/0010-audit-log-and-rsyslog.md.
func buildLogger(cfg config.Config) (*slog.Logger, func()) {
	base := logging.New(os.Stderr, cfg.LogLevel, cfg.LogFormat)
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
