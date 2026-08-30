package store

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Session storage.
//
// Sessions are the credential. Every request in server mode is authenticated by
// looking one up, and everything that can go wrong here goes wrong in the same
// direction: a session that outlives what it should is a login somebody thought
// they had ended.
//
// The tests are written against the store rather than through the HTTP layer
// because the guarantees are in the SQL - what the delete matches, what the
// prune sweeps, what a read returns for a row that should be gone. A test
// through a handler would prove the handler asked, not what the answer was.

// newSession builds a session for a user, at a given time.
func newSession(t *testing.T, userID int64, hash string, at time.Time) auth.Session {
	t.Helper()

	return auth.Session{
		IDHash:     hash,
		UserID:     userID,
		CreatedAt:  at,
		LastSeenAt: at,
		ExpiresAt:  at.Add(auth.SessionAbsoluteTimeout),
		IP:         "203.0.113.10",
		UserAgent:  "a browser",
		CSRFToken:  "csrf-" + hash,
	}
}

// seedUser creates one user and returns it.
func seedUser(t *testing.T, db *DB, name string) domain.User {
	t.Helper()

	user, err := db.CreateUser(context.Background(), domain.User{
		DisplayName: name, Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create %s: %v", name, err)
	}
	return user
}

// TestSessionRoundTrip.
//
// Everything the cookie carries has to survive storage, including the CSRF token
// - which is stored with the session rather than derived, so a session read that
// lost it would break every form on the site in a way that looks like a CSRF
// attack.
func TestSessionRoundTrip(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user := seedUser(t, db, "Session User")

	at := time.Date(2026, 5, 4, 9, 30, 0, 0, time.UTC)
	session := newSession(t, user.ID, "hash-1", at)
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	loaded, err := db.GetSession(ctx, "hash-1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if loaded.UserID != user.ID {
		t.Errorf("user = %d, want %d", loaded.UserID, user.ID)
	}
	if loaded.CSRFToken != session.CSRFToken {
		t.Errorf("CSRF token = %q, want %q", loaded.CSRFToken, session.CSRFToken)
	}
	if loaded.IP != "203.0.113.10" || loaded.UserAgent != "a browser" {
		t.Errorf("the session's origin was lost: %+v", loaded)
	}
	if !loaded.CreatedAt.Equal(at) {
		t.Errorf("created at %v, want %v", loaded.CreatedAt, at)
	}
	if !loaded.ExpiresAt.Equal(session.ExpiresAt) {
		t.Errorf("expires at %v, want %v", loaded.ExpiresAt, session.ExpiresAt)
	}
}

// TestAnUnknownSessionIsNotFound.
//
// Not an error to be logged and not an empty session that would authenticate
// nobody as user zero: a distinct answer the caller has to handle.
func TestAnUnknownSessionIsNotFound(t *testing.T) {
	db := newTestDB(t)

	_, err := db.GetSession(context.Background(), "a-hash-nobody-issued")
	if !errors.Is(err, ErrNotFound) {
		t.Errorf("unknown session: got %v, want ErrNotFound", err)
	}
}

// TestSigningOutRevokesImmediately.
//
// Sign-out has to be a server-side fact. A client-side gesture that drops the
// cookie leaves the credential valid for whoever copied it, which is exactly the
// case somebody signs out to prevent.
func TestSigningOutRevokesImmediately(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user := seedUser(t, db, "Session User")

	now := time.Now().UTC()
	if err := db.CreateSession(ctx, newSession(t, user.ID, "hash-1", now)); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if err := db.DeleteSession(ctx, "hash-1"); err != nil {
		t.Fatalf("delete session: %v", err)
	}
	if _, err := db.GetSession(ctx, "hash-1"); !errors.Is(err, ErrNotFound) {
		t.Errorf("a signed-out session is still readable: %v", err)
	}

	// Deleting one that is already gone is what a double-click does.
	if err := db.DeleteSession(ctx, "hash-1"); err != nil {
		t.Errorf("signing out twice failed: %v", err)
	}
}

// TestRevokingAUsersSessionsLeavesEverybodyElseSignedIn.
//
// Used when an account is disabled, its role changes, or its password is reset.
// The blast radius matters in both directions: missing one of the user's own
// sessions means the privilege change did not take effect, and catching somebody
// else's means an unrelated person is signed out with no explanation.
func TestRevokingAUsersSessionsLeavesEverybodyElseSignedIn(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	subject := seedUser(t, db, "Subject")
	bystander := seedUser(t, db, "Bystander")

	now := time.Now().UTC()
	for _, session := range []auth.Session{
		newSession(t, subject.ID, "subject-laptop", now),
		newSession(t, subject.ID, "subject-phone", now),
		newSession(t, bystander.ID, "bystander-laptop", now),
	} {
		if err := db.CreateSession(ctx, session); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}

	revoked, err := db.DeleteUserSessions(ctx, subject.ID)
	if err != nil {
		t.Fatalf("revoke: %v", err)
	}
	if revoked != 2 {
		t.Errorf("revoked %d sessions, want 2", revoked)
	}
	for _, hash := range []string{"subject-laptop", "subject-phone"} {
		if _, err := db.GetSession(ctx, hash); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s survived the revocation: %v", hash, err)
		}
	}
	if _, err := db.GetSession(ctx, "bystander-laptop"); err != nil {
		t.Errorf("an unrelated user was signed out: %v", err)
	}
}

// TestListingSessionsShowsWhereSomebodyIsSignedIn.
//
// The screen this feeds is the one where a person notices a session they do not
// recognise, so the ordering is by most recent use - the unfamiliar one is
// usually the one at the top or the bottom, and either way the order has to be
// stable and meaningful rather than by insertion.
func TestListingSessionsShowsWhereSomebodyIsSignedIn(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user := seedUser(t, db, "Session User")
	other := seedUser(t, db, "Somebody Else")

	now := time.Now().UTC()
	older := newSession(t, user.ID, "older", now)
	older.LastSeenAt = now.Add(-2 * time.Hour)
	newer := newSession(t, user.ID, "newer", now)
	newer.LastSeenAt = now.Add(-time.Minute)

	for _, session := range []auth.Session{older, newer, newSession(t, other.ID, "theirs", now)} {
		if err := db.CreateSession(ctx, session); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}

	sessions, err := db.ListUserSessions(ctx, user.ID)
	if err != nil {
		t.Fatalf("list sessions: %v", err)
	}
	if len(sessions) != 2 {
		t.Fatalf("listed %d sessions, want this user's 2", len(sessions))
	}
	if sessions[0].IDHash != "newer" {
		t.Errorf("sessions are not ordered by most recent use: %s first",
			sessions[0].IDHash)
	}
	for _, session := range sessions {
		if session.UserID != user.ID {
			t.Errorf("somebody else's session is on this user's screen: %+v", session)
		}
	}
}

// TestTouchingASessionKeepsItAlive.
//
// The idle timeout is measured from last use, and the update is what a person's
// activity amounts to. It has to move last_seen_at and nothing else - an update
// that also refreshed the absolute expiry would turn a bounded session into an
// unbounded one for anybody who keeps a tab open.
func TestTouchingASessionKeepsItAlive(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user := seedUser(t, db, "Session User")

	start := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	session := newSession(t, user.ID, "hash-1", start)
	if err := db.CreateSession(ctx, session); err != nil {
		t.Fatalf("create session: %v", err)
	}

	later := start.Add(3 * time.Hour)
	if err := db.TouchSession(ctx, "hash-1", later); err != nil {
		t.Fatalf("touch: %v", err)
	}

	loaded, err := db.GetSession(ctx, "hash-1")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if !loaded.LastSeenAt.Equal(later) {
		t.Errorf("last seen = %v, want %v", loaded.LastSeenAt, later)
	}
	if !loaded.ExpiresAt.Equal(session.ExpiresAt) {
		t.Errorf("using a session extended its absolute expiry to %v; a session that "+
			"never ends while somebody keeps a tab open is not a bounded session",
			loaded.ExpiresAt)
	}
	if !loaded.CreatedAt.Equal(start) {
		t.Errorf("using a session rewrote when it started: %v", loaded.CreatedAt)
	}
}

// TestPruningSweepsBothLifetimes.
//
// Two independent limits, and a sweep that applied only one would leave half the
// dead sessions in the table. Expiry is checked on every read as well, so a
// missed sweep is a storage problem rather than a security one - but a sweep
// that quietly stopped matching would never be noticed at all without this.
func TestPruningSweepsBothLifetimes(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user := seedUser(t, db, "Session User")

	now := time.Date(2026, 5, 4, 12, 0, 0, 0, time.UTC)

	fresh := newSession(t, user.ID, "fresh", now)

	expired := newSession(t, user.ID, "expired", now)
	expired.ExpiresAt = now.Add(-time.Minute)

	idle := newSession(t, user.ID, "idle", now)
	idle.LastSeenAt = now.Add(-auth.SessionIdleTimeout - time.Minute)

	// Just inside the idle timeout: the boundary is where an off-by-one lives,
	// and signing somebody out a minute early is a bug they will report as
	// flakiness.
	nearlyIdle := newSession(t, user.ID, "nearly-idle", now)
	nearlyIdle.LastSeenAt = now.Add(-auth.SessionIdleTimeout + time.Minute)

	for _, session := range []auth.Session{fresh, expired, idle, nearlyIdle} {
		if err := db.CreateSession(ctx, session); err != nil {
			t.Fatalf("create session: %v", err)
		}
	}

	pruned, err := db.PruneSessions(ctx, now)
	if err != nil {
		t.Fatalf("prune: %v", err)
	}
	if pruned != 2 {
		t.Errorf("pruned %d sessions, want the expired one and the idle one", pruned)
	}
	for _, hash := range []string{"fresh", "nearly-idle"} {
		if _, err := db.GetSession(ctx, hash); err != nil {
			t.Errorf("%s was swept and should not have been: %v", hash, err)
		}
	}
	for _, hash := range []string{"expired", "idle"} {
		if _, err := db.GetSession(ctx, hash); !errors.Is(err, ErrNotFound) {
			t.Errorf("%s survived the sweep", hash)
		}
	}
}

// TestOnlyTheHashOfACookieIsStored.
//
// The property the whole scheme rests on: a stolen database yields no usable
// cookies. It is asserted here as a fact about the table - the value handed to
// the browser must not be findable in it - because the alternative is trusting
// that every call site remembered to hash.
func TestOnlyTheHashOfACookieIsStored(t *testing.T) {
	db := newTestDB(t)
	ctx := context.Background()
	user := seedUser(t, db, "Session User")

	cookie, hash, err := auth.NewSessionID()
	if err != nil {
		t.Fatalf("new session id: %v", err)
	}
	if err := db.CreateSession(ctx, newSession(t, user.ID, hash, time.Now().UTC())); err != nil {
		t.Fatalf("create session: %v", err)
	}

	// The cookie value must appear nowhere in the sessions table, in any column.
	var found int
	err = db.read.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM sessions
		WHERE id_hash = ? OR csrf_token = ? OR ip = ? OR user_agent = ?`,
		cookie, cookie, cookie, cookie).Scan(&found)
	if err != nil {
		t.Fatalf("search the sessions table: %v", err)
	}
	if found != 0 {
		t.Error("the session cookie value itself is stored; a stolen database " +
			"would hand somebody working credentials")
	}
	if _, err := db.GetSession(ctx, hash); err != nil {
		t.Errorf("the session cannot be found by its hash: %v", err)
	}
}
