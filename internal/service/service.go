// Package service holds the business rules, and is the only place in the
// application where authorisation is decided, transactions are opened and audit
// records are written.
//
// Everything above it (internal/web) renders what it returns; everything below it
// (internal/store) does what it is told. That arrangement is what makes two
// requirements checkable by reading one package rather than auditing every
// handler: ASR-005 (every access is authorised) and ASR-006 (every mutation is
// attributable and logged).
//
// The rules this package must uphold:
//
//   - Every exported method calls Authorizer.Can before it acts.
//   - Every mutation writes its audit row inside the same transaction as the
//     change, so a change cannot exist without its record.
//   - The acting user comes from the context, never from a parameter a caller
//     could forge.
//   - No method inspects the run mode; the mode chose the Authorizer at startup.
//
// See docs/adr/0012-layered-package-structure.md and
// docs/adr/0010-audit-log-and-rsyslog.md.
package service

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/blob"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/store"
)

// Re-exported so the web layer can classify failures without importing the store
// or auth packages directly.
var (
	ErrNotFound = store.ErrNotFound
	// ErrInvalidQuery is search text the user has to fix. Mapped onto a
	// validation failure so the screen shows the message rather than a 500.
	ErrInvalidQuery = store.ErrInvalidQuery
	ErrForbidden    = auth.ErrForbidden
	ErrValidation   = domain.ErrValidation
	ErrConflict     = errors.New("conflict")
	ErrUnauthorized = auth.ErrUnauthenticated
)

// Clock returns the current time. It is injected so that tests can control time
// exactly; time.Now appears in one place in the running application.
type Clock func() time.Time

// Service is the application's business layer.
type Service struct {
	db    *store.DB
	authz auth.Authorizer
	log   *slog.Logger
	now   Clock
	// blobs is nil until WithBlobs is called. Attachment operations then report
	// that the feature is unconfigured rather than failing obscurely.
	blobs *blob.Store
}

// New builds a Service. A nil clock defaults to time.Now.
func New(db *store.DB, authz auth.Authorizer, log *slog.Logger, now Clock) *Service {
	if now == nil {
		now = time.Now
	}
	return &Service{db: db, authz: authz, log: log, now: now}
}

// Now returns the current time through the injected clock.
func (s *Service) Now() time.Time { return s.now() }

// Settings returns the instance-wide defaults.
func (s *Service) Settings(ctx context.Context) (store.Settings, error) {
	return s.db.GetSettings(ctx)
}

// RequestMeta carries the request-scoped facts that belong in the audit trail but
// are not part of the domain. The web layer puts it in the context; the service
// layer copies it onto every audit row.
type RequestMeta struct {
	IP        string
	RequestID string
}

type metaKey struct{}

// WithRequestMeta attaches request metadata to a context.
func WithRequestMeta(ctx context.Context, m RequestMeta) context.Context {
	return context.WithValue(ctx, metaKey{}, m)
}

func requestMetaFrom(ctx context.Context) RequestMeta {
	m, _ := ctx.Value(metaKey{}).(RequestMeta)
	return m
}

// audit writes one audit row inside the caller's transaction and mirrors it to
// the operational log.
//
// It takes a *sql.Tx rather than opening its own, which is the mechanism behind
// the atomicity guarantee: if the surrounding transaction rolls back, so does the
// audit row, and if this insert fails the whole change fails.
//
// The operational log line is emitted by the caller after commit (see auditLog),
// because a log line about a change that was rolled back would be a lie.
func (s *Service) audit(ctx context.Context, tx *sql.Tx, action, resourceType string, resourceID int64, onBehalfOf int64, detail any) error {
	actor, err := auth.MustUser(ctx)
	if err != nil {
		return err
	}
	meta := requestMetaFrom(ctx)

	// Detail is stored as compact JSON so the trail stays queryable. A detail
	// value that cannot be marshalled must not lose the audit record, so the
	// failure is recorded in place of the detail rather than aborting.
	encoded := ""
	if detail != nil {
		if raw, marshalErr := json.Marshal(detail); marshalErr == nil {
			encoded = string(raw)
		} else {
			encoded = fmt.Sprintf(`{"detail_error":%q}`, marshalErr.Error())
		}
	}

	return store.InsertAuditTx(ctx, tx, store.AuditEvent{
		At:           s.now(),
		ActorID:      actor.ID,
		ActorName:    actor.DisplayName,
		OnBehalfOf:   onBehalfOf,
		Action:       action,
		ResourceType: resourceType,
		ResourceID:   resourceID,
		Detail:       encoded,
		IP:           meta.IP,
		RequestID:    meta.RequestID,
	})
}

// auditLog mirrors a committed audit event to the operational log, which in
// server mode also reaches rsyslog. Called after a successful commit only.
//
// Secrets never reach here: this package passes domain facts, and the logging
// package redacts anything sensitive that slips through
// (docs/adr/0010-audit-log-and-rsyslog.md).
func (s *Service) auditLog(ctx context.Context, action, resourceType string, resourceID int64) {
	if s.log == nil {
		return
	}
	actor, _ := auth.UserFrom(ctx)
	meta := requestMetaFrom(ctx)
	s.log.InfoContext(ctx, "audit",
		slog.String("action", action),
		slog.String("resource_type", resourceType),
		slog.Int64("resource_id", resourceID),
		slog.Int64("actor_id", actor.ID),
		slog.String("actor", actor.DisplayName),
		slog.String("request_id", meta.RequestID),
	)
}

// notFoundFor hides the difference between "does not exist" and "you may not see
// it" for resources an actor may not know about, so that probing ids cannot be
// used to enumerate records. Errors that are already validation failures pass
// through unchanged, since those describe the caller's own input.
func notFoundFor(err error) error {
	if errors.Is(err, auth.ErrForbidden) {
		return ErrNotFound
	}
	return err
}

// AuditTrail returns the recorded history of one resource. Reading the trail is
// itself an authorised action.
func (s *Service) AuditTrail(ctx context.Context, resourceType string, resourceID int64) ([]store.AuditEvent, error) {
	if err := s.authz.Can(ctx, auth.ActionView, auth.Resource{Type: resourceType, ID: resourceID}); err != nil {
		return nil, notFoundFor(err)
	}
	return s.db.ListAuditEvents(ctx, resourceType, resourceID, 200)
}
