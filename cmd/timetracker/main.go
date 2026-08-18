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

	log := logging.New(os.Stderr, cfg.LogLevel, cfg.LogFormat)
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
	authorizer, identity, err := buildIdentity(ctx, cfg, db, log)
	if err != nil {
		return err
	}

	svc := service.New(db, authorizer, log, time.Now)
	server, err := web.New(svc, cfg, log, identity)
	if err != nil {
		return err
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

// buildIdentity assembles the mode-specific authoriser and identity resolver.
//
// This is the only function in the application that branches on the run mode.
// Everything downstream receives an Authorizer and an identity function and
// cannot tell which mode produced them, which is what stops the two modes from
// growing separate behaviour (docs/adr/0001-single-binary-two-modes.md).
func buildIdentity(ctx context.Context, cfg config.Config, db *store.DB, log *slog.Logger) (
	auth.Authorizer, func(*http.Request) (domain.User, error), error,
) {
	switch cfg.Mode {
	case config.ModeLocal:
		// Local mode has exactly one user: whoever launched the process. There
		// is no sign-up and no login, because the socket is bound to loopback
		// and serves nobody else.
		//
		// Note that this is still a real Authorizer, not a bypass: every service
		// method consults it exactly as it will in server mode, so the
		// authorisation path is exercised in both.
		bootstrap := service.New(db, auth.SingleUserAuthorizer{}, log, time.Now)
		if err := bootstrap.EnsureLocalUser(ctx, defaultDisplayName(), cfg.TimeZone); err != nil {
			return nil, nil, fmt.Errorf("prepare local user: %w", err)
		}

		identity := func(r *http.Request) (domain.User, error) {
			// Re-read on each request rather than caching, so a preference
			// change (theme, time zone) takes effect on the next page load.
			return bootstrap.LocalUser(r.Context())
		}
		return auth.SingleUserAuthorizer{}, identity, nil

	case config.ModeServer:
		// Server mode needs sessions, password verification and OIDC, which
		// arrive in layer 2 (docs/MVP_PLAN.md). Refusing to start is the honest
		// answer: silently falling back to the single-user identity would serve
		// everyone's timesheet to anyone who connected.
		return nil, nil, errors.New(
			"server mode is not implemented yet: authentication, RBAC and rsyslog " +
				"arrive in layer 2 (see docs/MVP_PLAN.md). Run without --mode=server " +
				"for the local single-user application")

	default:
		return nil, nil, fmt.Errorf("unknown mode %q", cfg.Mode)
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
