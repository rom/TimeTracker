package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/auth"
)

// Session storage. Only the hash of a cookie value is ever written here, so a
// stolen database yields no usable session cookies.
// See docs/adr/0006-authentication-model.md.

// CreateSession stores a new session.
func (db *DB) CreateSession(ctx context.Context, s auth.Session) error {
	_, err := db.write.ExecContext(ctx, `
		INSERT INTO sessions (id_hash, user_id, created_at, last_seen_at, expires_at,
		                      ip, user_agent, csrf_token)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		s.IDHash, s.UserID, formatTime(s.CreatedAt), formatTime(s.LastSeenAt),
		formatTime(s.ExpiresAt), s.IP, s.UserAgent, s.CSRFToken)
	if err != nil {
		return fmt.Errorf("create session: %w", err)
	}
	return nil
}

// GetSession loads a session by the hash of its cookie value.
func (db *DB) GetSession(ctx context.Context, idHash string) (auth.Session, error) {
	var s auth.Session
	var createdAt, lastSeenAt, expiresAt string

	err := db.read.QueryRowContext(ctx, `
		SELECT id_hash, user_id, created_at, last_seen_at, expires_at, ip, user_agent, csrf_token
		FROM sessions WHERE id_hash = ?`, idHash).
		Scan(&s.IDHash, &s.UserID, &createdAt, &lastSeenAt, &expiresAt, &s.IP, &s.UserAgent, &s.CSRFToken)
	if errors.Is(err, sql.ErrNoRows) {
		return auth.Session{}, ErrNotFound
	}
	if err != nil {
		return auth.Session{}, err
	}

	if s.CreatedAt, err = parseTime(createdAt); err != nil {
		return auth.Session{}, err
	}
	if s.LastSeenAt, err = parseTime(lastSeenAt); err != nil {
		return auth.Session{}, err
	}
	if s.ExpiresAt, err = parseTime(expiresAt); err != nil {
		return auth.Session{}, err
	}
	return s, nil
}

// TouchSession records that a session was used.
//
// It is called on every authenticated request, so it is a single indexed update
// and nothing more. The idle timeout is enforced by comparing last_seen_at on
// read, not by a background job.
func (db *DB) TouchSession(ctx context.Context, idHash string, at time.Time) error {
	_, err := db.write.ExecContext(ctx,
		`UPDATE sessions SET last_seen_at = ? WHERE id_hash = ?`, formatTime(at), idHash)
	return err
}

// DeleteSession revokes one session. This is what makes sign-out immediate,
// rather than a client-side gesture that leaves the credential valid.
func (db *DB) DeleteSession(ctx context.Context, idHash string) error {
	return DeleteSessionTx(ctx, db.write, idHash)
}

// DeleteSessionTx revokes one session on the caller's executor.
func DeleteSessionTx(ctx context.Context, db Execer, idHash string) error {
	_, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE id_hash = ?`, idHash)
	return err
}

// DeleteUserSessions revokes every session belonging to a user.
//
// Used when an account is disabled, its role changes, or its password is reset:
// a privilege change that leaves old sessions alive has not really taken effect.
func (db *DB) DeleteUserSessions(ctx context.Context, userID int64) (int64, error) {
	return DeleteUserSessionsTx(ctx, db.write, userID)
}

// DeleteUserSessionsTx is DeleteUserSessions on the caller's executor, so a
// privilege change and the sign-out it forces commit together.
func DeleteUserSessionsTx(ctx context.Context, db Execer, userID int64) (int64, error) {
	res, err := db.ExecContext(ctx, `DELETE FROM sessions WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	return res.RowsAffected()
}

// ListUserSessions returns a user's active sessions, so they can see where they
// are signed in and revoke individually.
func (db *DB) ListUserSessions(ctx context.Context, userID int64) ([]auth.Session, error) {
	rows, err := db.read.QueryContext(ctx, `
		SELECT id_hash, user_id, created_at, last_seen_at, expires_at, ip, user_agent, csrf_token
		FROM sessions WHERE user_id = ? ORDER BY last_seen_at DESC`, userID)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var sessions []auth.Session
	for rows.Next() {
		var s auth.Session
		var createdAt, lastSeenAt, expiresAt string
		if err := rows.Scan(&s.IDHash, &s.UserID, &createdAt, &lastSeenAt, &expiresAt,
			&s.IP, &s.UserAgent, &s.CSRFToken); err != nil {
			return nil, err
		}
		var perr error
		if s.CreatedAt, perr = parseTime(createdAt); perr != nil {
			return nil, perr
		}
		if s.LastSeenAt, perr = parseTime(lastSeenAt); perr != nil {
			return nil, perr
		}
		if s.ExpiresAt, perr = parseTime(expiresAt); perr != nil {
			return nil, perr
		}
		sessions = append(sessions, s)
	}
	return sessions, rows.Err()
}

// PruneSessions deletes expired sessions.
//
// Both lifetimes are applied: the absolute expiry, and the idle timeout measured
// from last use. Run periodically; expiry is also checked on every read, so a
// missed sweep is a storage-growth problem and never a security one.
func (db *DB) PruneSessions(ctx context.Context, now time.Time) (int64, error) {
	res, err := db.write.ExecContext(ctx,
		`DELETE FROM sessions WHERE expires_at < ? OR last_seen_at < ?`,
		formatTime(now), formatTime(now.Add(-auth.SessionIdleTimeout)))
	if err != nil {
		return 0, fmt.Errorf("prune sessions: %w", err)
	}
	return res.RowsAffected()
}
