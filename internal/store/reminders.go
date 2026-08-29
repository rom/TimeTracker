package store

import (
	"context"
	"fmt"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// Reminder dismissals.
//
// The only thing a reminder stores. Everything else about a nudge is computed
// from the timesheet when a screen renders, so there is nothing to keep in step
// and nothing to clean up when a person records the time the nudge was about
// (docs/adr/0034-reminders-are-shown-not-sent.md).

// DismissReminder records that somebody does not want to be told again.
//
// Idempotent through the unique index rather than through a read first: two tabs
// and a double click both produce one row, and neither produces an error.
func (db *DB) DismissReminder(ctx context.Context, userID int64, kind domain.ReminderKind, scope string) error {
	_, err := db.write.ExecContext(ctx, `
		INSERT INTO reminder_dismissals (user_id, kind, scope, dismissed_at)
		VALUES (?, ?, ?, ?)
		ON CONFLICT (user_id, kind, scope) DO NOTHING`,
		userID, string(kind), scope, formatTime(time.Now()))
	if err != nil {
		return fmt.Errorf("dismiss reminder: %w", err)
	}
	return nil
}

// DismissedReminders returns what this person has waved away for the given
// scopes, as a set keyed "kind\x00scope".
//
// One query for every nudge on the screen rather than one per nudge: the caller
// knows the two or three scopes in play - today, and the week - before it knows
// which reminders it has, and asking once keeps this off the day view's critical
// path.
func (db *DB) DismissedReminders(ctx context.Context, userID int64, scopes []string) (map[string]bool, error) {
	if len(scopes) == 0 {
		return map[string]bool{}, nil
	}

	args := make([]any, 0, len(scopes)+1)
	args = append(args, userID)
	for _, scope := range scopes {
		args = append(args, scope)
	}

	rows, err := db.read.QueryContext(ctx, `
		SELECT kind, scope FROM reminder_dismissals
		WHERE user_id = ? AND scope IN (`+repeatPlaceholders(len(scopes))+`)`, args...)
	if err != nil {
		return nil, fmt.Errorf("read reminder dismissals: %w", err)
	}
	defer func() { _ = rows.Close() }()

	dismissed := map[string]bool{}
	for rows.Next() {
		var kind, scope string
		if err := rows.Scan(&kind, &scope); err != nil {
			return nil, err
		}
		dismissed[DismissalKey(domain.ReminderKind(kind), scope)] = true
	}
	return dismissed, rows.Err()
}

// DismissalKey builds the map key, so the reader and the writer of that map
// cannot disagree about its shape.
//
// A NUL separator rather than a colon or a dash: the scope is a date today, and
// a key that could collide if it ever stops being one is a bug waiting for a
// change nobody connects to it.
func DismissalKey(kind domain.ReminderKind, scope string) string {
	return string(kind) + "\x00" + scope
}
