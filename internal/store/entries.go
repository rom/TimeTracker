package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/domain"
)

// entrySelect is the one place the time-entry column list lives. Every query that
// returns entries uses it, so a new column cannot be added to one path and
// forgotten in another.
const entrySelect = `
	SELECT e.id, e.user_id, e.entered_by, e.assignment_id, e.started_at, e.ended_at,
	       e.duration_seconds, e.note, e.billable, e.kind, e.status, e.time_zone,
	       e.rounding_rule_applied, e.billable_seconds, e.rate_minor, e.amount_minor,
	       e.currency, e.flagged, e.created_at, e.updated_at,
	       e.decided_by, e.decided_at, e.decision_note,
	       a.name, a.colour_key, a.icon, p.name, c.name, u.display_name, eb.display_name,
	       a.project_id, p.customer_id,
	       (SELECT COUNT(*) FROM attachments at
	         WHERE at.owner_type = 'time_entry' AND at.owner_id = e.id)
	FROM time_entries e
	JOIN assignments a  ON a.id  = e.assignment_id
	JOIN projects    p  ON p.id  = a.project_id
	JOIN customers   c  ON c.id  = p.customer_id
	JOIN users       u  ON u.id  = e.user_id
	JOIN users       eb ON eb.id = e.entered_by`

// CreateEntry inserts a time entry. A running entry is one with a nil EndedAt;
// nothing here prevents several from existing at once for the same user, which is
// the point (docs/adr/0004-concurrent-timers.md).
func (db *DB) CreateEntry(ctx context.Context, e domain.TimeEntry) (domain.TimeEntry, error) {
	return CreateEntryTx(ctx, db.write, e)
}

// UpdateEntry saves an edited entry.
func (db *DB) UpdateEntry(ctx context.Context, e domain.TimeEntry) error {
	return UpdateEntryTx(ctx, db.write, e)
}

// Execer is the part of *sql.DB and *sql.Tx these writes need.
//
// It exists so that the insert and the update have exactly one implementation
// each, whether they run on their own or inside a caller's transaction. The
// service layer needs the transactional form so that a change and its audit row
// commit together; it previously did that by keeping its own copy of the SQL,
// and the copies drifted - a column added to one was silently absent from the
// other, which is a stored field that quietly stops being stored.
type Execer interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
}

// CreateEntryTx inserts an entry using the given executor.
func CreateEntryTx(ctx context.Context, db Execer, e domain.TimeEntry) (domain.TimeEntry, error) {
	now := time.Now()
	var endedAt any
	if e.EndedAt != nil {
		endedAt = formatTime(*e.EndedAt)
	}

	res, err := db.ExecContext(ctx, `
		INSERT INTO time_entries (user_id, entered_by, assignment_id, started_at, ended_at,
		    duration_seconds, note, billable, kind, status, time_zone, rounding_rule_applied,
		    billable_seconds, rate_minor, amount_minor, currency, flagged, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.UserID, e.EnteredBy, e.AssignmentID, formatTime(e.StartedAt), endedAt,
		e.DurationSeconds, e.Note, boolToInt(e.Billable),
		string(e.KindOrDefault()), string(e.Status), e.TimeZone,
		e.RoundingRuleApplied, e.BillableSeconds, e.RateMinor, e.AmountMinor, e.Currency,
		boolToInt(e.Flagged), formatTime(now), formatTime(now))
	if err != nil {
		return domain.TimeEntry{}, fmt.Errorf("create time entry: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return domain.TimeEntry{}, err
	}
	e.ID = id
	e.CreatedAt = now
	e.UpdatedAt = now
	return e, nil
}

// CreateEntryWithTagsTx inserts an entry, its tags and its search index entry.
//
// One function so that the three cannot come apart: an entry whose tags were
// written but whose index was not is findable by every route except search,
// which is the sort of inconsistency nobody notices until they need it.
func CreateEntryWithTagsTx(ctx context.Context, tx *sql.Tx, e domain.TimeEntry) (domain.TimeEntry, error) {
	created, err := CreateEntryTx(ctx, tx, e)
	if err != nil {
		return domain.TimeEntry{}, err
	}
	if err := SetEntryTagsTx(ctx, tx, created.ID, e.Tags); err != nil {
		return domain.TimeEntry{}, err
	}
	if err := IndexEntryTx(ctx, tx, created.ID); err != nil {
		return domain.TimeEntry{}, err
	}
	return created, nil
}

// UpdateEntryWithTagsTx is the same for an edit.
func UpdateEntryWithTagsTx(ctx context.Context, tx *sql.Tx, e domain.TimeEntry) error {
	if err := UpdateEntryTx(ctx, tx, e); err != nil {
		return err
	}
	if err := SetEntryTagsTx(ctx, tx, e.ID, e.Tags); err != nil {
		return err
	}
	return IndexEntryTx(ctx, tx, e.ID)
}

// UpdateEntryTx saves an edited entry using the given executor.
func UpdateEntryTx(ctx context.Context, db Execer, e domain.TimeEntry) error {
	var endedAt any
	if e.EndedAt != nil {
		endedAt = formatTime(*e.EndedAt)
	}
	res, err := db.ExecContext(ctx, `
		UPDATE time_entries SET assignment_id = ?, started_at = ?, ended_at = ?,
		       duration_seconds = ?, note = ?, billable = ?, kind = ?, status = ?,
		       time_zone = ?,
		       rounding_rule_applied = ?, billable_seconds = ?, rate_minor = ?,
		       amount_minor = ?, currency = ?, flagged = ?, updated_at = ?
		WHERE id = ?`,
		e.AssignmentID, formatTime(e.StartedAt), endedAt, e.DurationSeconds, e.Note,
		boolToInt(e.Billable), string(e.KindOrDefault()), string(e.Status),
		e.TimeZone, e.RoundingRuleApplied,
		e.BillableSeconds, e.RateMinor, e.AmountMinor, e.Currency, boolToInt(e.Flagged),
		formatTime(time.Now()), e.ID)
	if err != nil {
		return fmt.Errorf("update time entry: %w", err)
	}
	return requireOneRow(res)
}

// BillingSnapshot is what an entry was worth at the moment it was billed.
//
// It is written onto the entry rather than recomputed at report time, so a rate
// change tomorrow cannot alter a figure that was invoiced today.
// See docs/adr/0014-exact-money-and-duration.md.
type BillingSnapshot struct {
	RoundingRule    string
	BillableSeconds int64
	RateMinor       int64
	AmountMinor     int64
	Currency        string
}

// StopEntryTx stops a running entry inside an existing transaction, recording
// its duration and its billing snapshot together.
//
// The `ended_at IS NULL` condition makes this safe against a double submit: two
// concurrent stop requests race, one updates the row and the other affects zero
// rows and learns the timer was already stopped. Doing it this way avoids a lock
// and cannot produce a doubled or negative duration.
func StopEntryTx(ctx context.Context, tx *sql.Tx, id int64, endedAt time.Time, seconds int64, billing BillingSnapshot) (bool, error) {
	res, err := tx.ExecContext(ctx, `
		UPDATE time_entries
		SET ended_at = ?, duration_seconds = ?, updated_at = ?,
		    rounding_rule_applied = ?, billable_seconds = ?, rate_minor = ?,
		    amount_minor = ?, currency = ?
		WHERE id = ? AND ended_at IS NULL`,
		formatTime(endedAt), seconds, formatTime(time.Now()),
		billing.RoundingRule, billing.BillableSeconds, billing.RateMinor,
		billing.AmountMinor, billing.Currency, id)
	if err != nil {
		return false, fmt.Errorf("stop time entry: %w", err)
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return false, err
	}
	return affected == 1, nil
}

// DeleteEntry removes an entry. Entries, unlike catalogue rows, may be deleted by
// their owner: a mistyped entry is noise, not history. The deletion itself is
// recorded in the audit log by the service layer, carrying the prior state.
func (db *DB) DeleteEntry(ctx context.Context, id int64) error {
	res, err := db.write.ExecContext(ctx, `DELETE FROM time_entries WHERE id = ?`, id)
	if err != nil {
		return fmt.Errorf("delete time entry: %w", err)
	}
	return requireOneRow(res)
}

// GetEntry loads one entry with its display names.
func (db *DB) GetEntry(ctx context.Context, id int64) (domain.TimeEntry, error) {
	row := db.read.QueryRowContext(ctx, entrySelect+` WHERE e.id = ?`, id)
	entry, err := scanEntry(row)
	if err != nil {
		return domain.TimeEntry{}, err
	}
	// Tags come with the entry rather than being a separate call the caller has
	// to remember: an entry loaded without them looks like an entry with none,
	// and an edit form built from that would silently clear them on save.
	byEntry, err := db.TagsForEntries(ctx, []int64{id})
	if err != nil {
		return domain.TimeEntry{}, err
	}
	entry.Tags = byEntry[id]
	return entry, nil
}

// GetEntryTx loads one entry inside a transaction, for read-modify-write
// sequences that must see a consistent value.
func GetEntryTx(ctx context.Context, tx *sql.Tx, id int64) (domain.TimeEntry, error) {
	row := tx.QueryRowContext(ctx, entrySelect+` WHERE e.id = ?`, id)
	return scanEntry(row)
}

// ListRunningEntries returns every timer currently running for a user.
//
// This is queried on every page render to draw the running-timer header, which is
// why the schema carries a partial index covering exactly these rows.
func (db *DB) ListRunningEntries(ctx context.Context, userID int64) ([]domain.TimeEntry, error) {
	rows, err := db.read.QueryContext(ctx,
		entrySelect+` WHERE e.user_id = ? AND e.ended_at IS NULL ORDER BY e.started_at`, userID)
	if err != nil {
		return nil, fmt.Errorf("list running entries: %w", err)
	}
	return collectEntries(rows)
}

// EntryFilter describes a query over entries. A zero value means "everything for
// this user", and each populated field narrows it.
type EntryFilter struct {
	UserID int64
	// From and To bound started_at. To is exclusive, so a day is [00:00, 24:00).
	From time.Time
	To   time.Time
	// AssignmentID, ProjectID and CustomerID narrow to one node of the hierarchy.
	AssignmentID int64
	ProjectID    int64
	CustomerID   int64
	// Scope restricts the query to what the actor may see. It is applied in
	// addition to any explicit narrowing above.
	Scope Scope
	// Statuses limits to particular workflow states; empty means all of them.
	Statuses []domain.EntryStatus
	// BillableOnly restricts to entries marked billable.
	BillableOnly bool
	// Tags narrows to entries carrying all of them. All rather than any: asking
	// for #incident and #billable-review means entries that are both, which is
	// what somebody looking for a specific slice expects.
	Tags []string
	// Query is free text, matched against the note, the assignment, the project,
	// the customer and the tags. UseRegexp treats it as a regular expression
	// instead of a substring.
	Query     string
	UseRegexp bool
	// Kinds limits to work, overtime or travel; empty means all of them.
	Kinds []domain.EntryKind
	// Limit caps the number of rows; 0 means unlimited.
	Limit int
}

// ListEntries returns entries matching a filter, newest first.
//
// The second result says how a free-text query was matched, so the interface can
// tell the user which mechanism answered them - a search that silently used a
// different one from the one asked for produces results nobody can explain.
func (db *DB) ListEntries(ctx context.Context, f EntryFilter) ([]domain.TimeEntry, error) {
	entries, _, err := db.SearchEntries(ctx, f)
	return entries, err
}

// SearchEntries is ListEntries with the search mode reported back.
func (db *DB) SearchEntries(ctx context.Context, f EntryFilter) ([]domain.TimeEntry, SearchMode, error) {
	query, args, mode, err := f.buildSearch()
	if err != nil {
		return nil, SearchNone, err
	}
	rows, err := db.read.QueryContext(ctx, entrySelect+query, args...)
	if err != nil {
		return nil, SearchNone, fmt.Errorf("list entries: %w", err)
	}
	entries, err := collectEntries(rows)
	if err != nil {
		return nil, SearchNone, err
	}

	// Tags come back in one query for the whole page rather than one per row.
	if len(entries) > 0 {
		ids := make([]int64, len(entries))
		for i := range entries {
			ids[i] = entries[i].ID
		}
		byEntry, tagErr := db.TagsForEntries(ctx, ids)
		if tagErr != nil {
			return nil, SearchNone, tagErr
		}
		for i := range entries {
			entries[i].Tags = byEntry[entries[i].ID]
		}
	}
	return entries, mode, nil
}

// build turns a filter into a WHERE clause and its bound arguments.
//
// Every user-supplied value becomes a placeholder argument; nothing is
// interpolated into the SQL text. The conditions themselves are literals written
// here, which is what keeps this free of injection risk.
// It reports which search mechanism the free-text condition chose, so the
// interface can say - a search that quietly used a different one from the one
// asked for produces results nobody can explain.
func (f EntryFilter) buildSearch() (string, []any, SearchMode, error) {
	var conditions []string
	var args []any

	if f.UserID != 0 {
		conditions = append(conditions, `e.user_id = ?`)
		args = append(args, f.UserID)
	}
	if !f.From.IsZero() {
		conditions = append(conditions, `e.started_at >= ?`)
		args = append(args, formatTime(f.From))
	}
	if !f.To.IsZero() {
		conditions = append(conditions, `e.started_at < ?`)
		args = append(args, formatTime(f.To))
	}
	if f.AssignmentID != 0 {
		conditions = append(conditions, `e.assignment_id = ?`)
		args = append(args, f.AssignmentID)
	}
	if f.ProjectID != 0 {
		conditions = append(conditions, `a.project_id = ?`)
		args = append(args, f.ProjectID)
	}
	if f.CustomerID != 0 {
		conditions = append(conditions, `p.customer_id = ?`)
		args = append(args, f.CustomerID)
	}
	if len(f.Statuses) > 0 {
		// The placeholder list is built from the *count* of statuses, never from
		// their values, so this stays parameterised.
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(f.Statuses)), ",")
		conditions = append(conditions, `e.status IN (`+placeholders+`)`)
		for _, s := range f.Statuses {
			args = append(args, string(s))
		}
	}
	if len(f.Kinds) > 0 {
		placeholders := strings.TrimSuffix(strings.Repeat("?,", len(f.Kinds)), ",")
		// An entry stored before kinds existed has an empty column and is
		// ordinary work, so a filter for work has to match it too.
		condition := `e.kind IN (` + placeholders + `)`
		for _, kind := range f.Kinds {
			args = append(args, string(kind))
			if kind == domain.KindWork {
				condition = `(` + condition + ` OR e.kind = '')`
			}
		}
		conditions = append(conditions, condition)
	}
	if tagCondition, tagArgs := tagFilterCondition(f.Tags); tagCondition != "" {
		conditions = append(conditions, tagCondition)
		args = append(args, tagArgs...)
	}
	if f.BillableOnly {
		conditions = append(conditions, `e.billable = 1`)
	}
	if scoped, scopeArgs := f.Scope.condition("a.project_id", "p.customer_id"); scoped != "" {
		conditions = append(conditions, scoped)
		args = append(args, scopeArgs...)
	}

	// Free text last, because it is the condition most likely to be rejected
	// and there is no point building the rest of the query to throw it away.
	mode, err := SearchNone, error(nil)
	if searchCond, searchArgs, searchMode, searchErr := searchCondition(f.Query, f.UseRegexp); searchErr != nil {
		return "", nil, SearchNone, searchErr
	} else if searchCond != "" {
		conditions = append(conditions, searchCond)
		args = append(args, searchArgs...)
		mode = searchMode
	}

	query := whereClause(conditions) + ` ORDER BY e.started_at DESC, e.id DESC`
	if f.Limit > 0 {
		query += ` LIMIT ?`
		args = append(args, f.Limit)
	}
	return query, args, mode, err
}

// RecentAssignments returns the assignments a user has logged time against most
// recently, most recent first. It drives the quick-start list and the fuzzy match
// in quick add, both of which are far more useful ordered by habit than by name.
func (db *DB) RecentAssignments(ctx context.Context, userID int64, limit int) ([]domain.Assignment, error) {
	if limit <= 0 {
		limit = 10
	}
	rows, err := db.read.QueryContext(ctx, `
		SELECT a.id, a.project_id, a.name, a.code, a.colour_key, a.icon, a.billable_default,
		       a.rate_minor, a.sort_order, a.favourite, a.archived_at, a.created_at,
		       p.name, p.customer_id, c.name, c.currency
		FROM assignments a
		JOIN projects  p ON p.id = a.project_id
		JOIN customers c ON c.id = p.customer_id
		JOIN (
			SELECT assignment_id, MAX(started_at) AS last_used
			FROM time_entries WHERE user_id = ?
			GROUP BY assignment_id
		) recent ON recent.assignment_id = a.id
		WHERE a.archived_at IS NULL
		ORDER BY recent.last_used DESC
		LIMIT ?`, userID, limit)
	if err != nil {
		return nil, fmt.Errorf("recent assignments: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var assignments []domain.Assignment
	for rows.Next() {
		a, err := scanAssignment(rows)
		if err != nil {
			return nil, err
		}
		assignments = append(assignments, a)
	}
	return assignments, rows.Err()
}

// collectEntries drains a result set of entries.
func collectEntries(rows *sql.Rows) ([]domain.TimeEntry, error) {
	defer func() { _ = rows.Close() }()
	var entries []domain.TimeEntry
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		entries = append(entries, e)
	}
	return entries, rows.Err()
}

func scanEntry(row rowScanner) (domain.TimeEntry, error) {
	var e domain.TimeEntry
	var endedAt sql.NullString
	var startedAt, createdAt, updatedAt, status, decidedAt string
	var billable, flagged int

	var kind string
	err := row.Scan(&e.ID, &e.UserID, &e.EnteredBy, &e.AssignmentID, &startedAt, &endedAt,
		&e.DurationSeconds, &e.Note, &billable, &kind, &status, &e.TimeZone,
		&e.RoundingRuleApplied, &e.BillableSeconds, &e.RateMinor, &e.AmountMinor,
		&e.Currency, &flagged, &createdAt, &updatedAt,
		&e.DecidedBy, &decidedAt, &e.DecisionNote,
		&e.AssignmentName, &e.ColourKey, &e.Icon, &e.ProjectName, &e.CustomerName,
		&e.UserName, &e.EnteredByName,
		&e.ProjectID, &e.CustomerID, &e.AttachmentCount)
	if errors.Is(err, sql.ErrNoRows) {
		return domain.TimeEntry{}, ErrNotFound
	}
	if err != nil {
		return domain.TimeEntry{}, err
	}

	e.Billable = billable != 0
	e.Flagged = flagged != 0
	e.Kind = domain.EntryKind(kind)
	e.Status = domain.EntryStatus(status)
	if e.StartedAt, err = parseTime(startedAt); err != nil {
		return domain.TimeEntry{}, err
	}
	if e.EndedAt, err = nullableTime(endedAt); err != nil {
		return domain.TimeEntry{}, err
	}
	if e.CreatedAt, err = parseTime(createdAt); err != nil {
		return domain.TimeEntry{}, err
	}
	if e.UpdatedAt, err = parseTime(updatedAt); err != nil {
		return domain.TimeEntry{}, err
	}
	if decidedAt != "" {
		if e.DecidedAt, err = parseTime(decidedAt); err != nil {
			return domain.TimeEntry{}, err
		}
	}
	return e, nil
}

// --------------------------------------------------------------------- audit --

// AuditEvent is one row of the append-only audit trail.
type AuditEvent struct {
	ID           int64
	At           time.Time
	ActorID      int64
	ActorName    string
	OnBehalfOf   int64
	Action       string
	ResourceType string
	ResourceID   int64
	Detail       string
	IP           string
	RequestID    string
}

// InsertAuditTx writes an audit row inside the caller's transaction.
//
// It only exists in a transactional form, deliberately: an audit record must
// commit with the change it describes, so there is no way to write one on its own
// and no way for a mutation to be committed without one.
// See docs/adr/0010-audit-log-and-rsyslog.md.
func InsertAuditTx(ctx context.Context, tx *sql.Tx, e AuditEvent) error {
	_, err := tx.ExecContext(ctx, `
		INSERT INTO audit_events (at, actor_id, actor_name, on_behalf_of, action,
		    resource_type, resource_id, detail, ip, request_id)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		formatTime(e.At), e.ActorID, e.ActorName, e.OnBehalfOf, e.Action,
		e.ResourceType, e.ResourceID, e.Detail, e.IP, e.RequestID)
	if err != nil {
		return fmt.Errorf("write audit event: %w", err)
	}
	return nil
}

// ListAuditEvents returns the trail for one resource, newest first. There is
// deliberately no update or delete counterpart anywhere in this package.
func (db *DB) ListAuditEvents(ctx context.Context, resourceType string, resourceID int64, limit int) ([]AuditEvent, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := db.read.QueryContext(ctx, `
		SELECT id, at, actor_id, actor_name, on_behalf_of, action, resource_type,
		       resource_id, detail, ip, request_id
		FROM audit_events
		WHERE resource_type = ? AND resource_id = ?
		ORDER BY at DESC, id DESC LIMIT ?`, resourceType, resourceID, limit)
	if err != nil {
		return nil, fmt.Errorf("list audit events: %w", err)
	}
	defer func() { _ = rows.Close() }()

	var events []AuditEvent
	for rows.Next() {
		var e AuditEvent
		var at string
		if err := rows.Scan(&e.ID, &at, &e.ActorID, &e.ActorName, &e.OnBehalfOf, &e.Action,
			&e.ResourceType, &e.ResourceID, &e.Detail, &e.IP, &e.RequestID); err != nil {
			return nil, err
		}
		var perr error
		if e.At, perr = parseTime(at); perr != nil {
			return nil, perr
		}
		events = append(events, e)
	}
	return events, rows.Err()
}

// ListEntriesEnteredBy returns entries this user recorded for somebody else.
//
// It is how the author of a proposal sees what is still waiting and what was
// declined - a proposal that vanishes silently is worse than one that is
// refused.
func (db *DB) ListEntriesEnteredBy(ctx context.Context, enteredBy int64, scope Scope) ([]domain.TimeEntry, error) {
	conditions := []string{`e.entered_by = ?`, `e.user_id != e.entered_by`}
	args := []any{enteredBy}

	if scoped, scopeArgs := scope.condition("a.project_id", "p.customer_id"); scoped != "" {
		conditions = append(conditions, scoped)
		args = append(args, scopeArgs...)
	}

	rows, err := db.read.QueryContext(ctx,
		entrySelect+whereClause(conditions)+` ORDER BY e.started_at DESC LIMIT 200`, args...)
	if err != nil {
		return nil, fmt.Errorf("list proposals: %w", err)
	}
	return collectEntries(rows)
}
