package domain

import (
	"errors"
	"fmt"
	"strings"
	"time"
)

// ErrValidation wraps every rule violation produced in this package, so callers
// can distinguish "the user typed something invalid" (show it to them) from "the
// database is unreachable" (log it and apologise).
var ErrValidation = errors.New("validation")

func invalid(format string, args ...any) error {
	return fmt.Errorf("%w: %s", ErrValidation, fmt.Sprintf(format, args...))
}

// Role is a user's system-wide role. Local mode runs everyone as RoleAdmin
// because there is only one person; server mode uses the full set with per-project
// membership on top. See docs/adr/0008-rbac-model.md.
type Role string

const (
	RoleAdmin   Role = "admin"   // everything, including users and settings
	RoleManager Role = "manager" // their customers and projects, approvals, money
	RoleMember  Role = "member"  // own time, read the projects they belong to
	RoleClient  Role = "client"  // read-only reports for their own customer
)

// EntryStatus tracks a time entry through the proxy-confirmation and approval
// workflow. Only Confirmed entries count towards any total.
// See docs/adr/0005-proxy-time-entry.md.
type EntryStatus string

const (
	// StatusConfirmed is an entry the owner created themselves, or a proxy entry
	// the owner has accepted. This is the only status that counts.
	StatusConfirmed EntryStatus = "confirmed"
	// StatusPending is a proxy entry awaiting the subject's decision. It is
	// excluded from every total, report and export.
	StatusPending EntryStatus = "pending"
	// StatusRejected is a proxy entry the subject declined. Retained, never
	// deleted, so the record of what was claimed survives.
	StatusRejected EntryStatus = "rejected"
)

// User is a person who records time.
//
// It deliberately carries no credential material: no password hash, no session
// token, no TOTP secret. Those live in store.Account and never leave the storage
// and service layers, so there is no field here for a template or a JSON
// response to leak by accident.
type User struct {
	ID          int64
	DisplayName string
	Email       string
	Role        Role
	// TimeZone is an IANA name such as "Europe/Stockholm". It decides which day
	// an entry belongs to; see docs/adr/0015-utc-storage-local-display.md.
	TimeZone string
	Theme    string
	// Language is a BCP 47 tag such as "sv". Empty means the browser's
	// Accept-Language header decides, which is the right default for someone who
	// has never expressed a preference.
	Language string
	Active   bool
	// ClientCustomerID scopes a user with the client role to one customer. It is
	// zero for every other role.
	ClientCustomerID int64
	// UsesSSO is display-only: it tells an administrator that this account signs
	// in through the identity provider rather than with a local password.
	UsesSSO     bool
	LastLoginAt time.Time
	CreatedAt   time.Time
}

// CanSeeMoney reports whether this user's role may see rates and amounts at all.
// It is a presentation convenience; the authoritative check is auth.Can with
// ActionViewMoney.
func (u User) CanSeeMoney() bool {
	return u.Role == RoleAdmin || u.Role == RoleManager || u.Role == RoleMember
}

// Customer is the party that gets billed.
type Customer struct {
	ID       int64
	Name     string
	Code     string // short identifier used in exports and on invoices
	Currency string
	// ColourKey names a palette entry rather than holding a hex value, so the
	// colour stays legible in all seven themes.
	// See docs/adr/0011-theming-via-css-custom-properties.md.
	ColourKey string
	Icon      string
	Notes     string
	// RateMinor is the customer-level default hourly rate in minor units, used
	// when neither the assignment nor the project sets one. 0 means "no default".
	RateMinor int64
	// ArchivedAt is set instead of deleting. Deleting a customer would orphan
	// invoiced history, so nothing in this application removes one.
	ArchivedAt *time.Time
	CreatedAt  time.Time
}

// Archived reports whether the record has been retired from the pickers.
func (c Customer) Archived() bool { return c.ArchivedAt != nil }

// Validate checks the rules that hold regardless of storage.
func (c Customer) Validate() error {
	if strings.TrimSpace(c.Name) == "" {
		return invalid("customer name is required")
	}
	if len(c.Name) > 200 {
		return invalid("customer name is too long (max 200)")
	}
	if c.Currency != "" && len(c.Currency) != 3 {
		return invalid("currency must be a three-letter ISO-4217 code, got %q", c.Currency)
	}
	return nil
}

// Project is a body of work for one customer.
type Project struct {
	ID              int64
	CustomerID      int64
	Name            string
	Code            string
	ColourKey       string
	Icon            string
	BillableDefault bool
	// RateMinor is the hourly rate in minor units, 0 meaning "inherit". Rate
	// resolution runs entry → person-on-project → project → customer → global.
	RateMinor    int64
	RoundingRule string
	// BudgetSeconds and BudgetMinor are optional caps used for burn reporting.
	BudgetSeconds int64
	BudgetMinor   int64
	ArchivedAt    *time.Time
	CreatedAt     time.Time

	// CustomerName is populated by queries that join, for display. It is not
	// persisted on the project row.
	CustomerName string
	Currency     string
}

func (p Project) Archived() bool { return p.ArchivedAt != nil }

func (p Project) Validate() error {
	if strings.TrimSpace(p.Name) == "" {
		return invalid("project name is required")
	}
	if p.CustomerID == 0 {
		return invalid("project must belong to a customer")
	}
	if p.RateMinor < 0 {
		return invalid("rate cannot be negative")
	}
	return nil
}

// Assignment is the thing a timer actually runs against - the granularity people
// describe their day in ("I was on the migration"). Its colour and icon are what
// make the day view scannable; its code appears on invoices.
type Assignment struct {
	ID              int64
	ProjectID       int64
	Name            string
	Code            string
	ColourKey       string
	Icon            string
	BillableDefault bool
	RateMinor       int64 // 0 means inherit from the project
	SortOrder       int64
	Favourite       bool
	ArchivedAt      *time.Time
	CreatedAt       time.Time

	// Denormalised for display; populated by the joining queries.
	ProjectName  string
	CustomerID   int64
	CustomerName string
	Currency     string
}

func (a Assignment) Archived() bool { return a.ArchivedAt != nil }

// Label is the human-readable path of an assignment, used in pickers, exports and
// the quick-add matcher.
func (a Assignment) Label() string {
	switch {
	case a.CustomerName != "" && a.ProjectName != "":
		return a.CustomerName + " / " + a.ProjectName + " / " + a.Name
	case a.ProjectName != "":
		return a.ProjectName + " / " + a.Name
	default:
		return a.Name
	}
}

func (a Assignment) Validate() error {
	if strings.TrimSpace(a.Name) == "" {
		return invalid("assignment name is required")
	}
	if a.ProjectID == 0 {
		return invalid("assignment must belong to a project")
	}
	if a.RateMinor < 0 {
		return invalid("rate cannot be negative")
	}
	return nil
}

// TimeEntry is an interval of work on an assignment.
//
// EndedAt is nil while the timer is running. There is deliberately no constraint
// limiting a user to one running entry, and overlapping intervals are legal:
// see docs/adr/0004-concurrent-timers.md.
type TimeEntry struct {
	ID           int64
	UserID       int64 // whose time this is
	EnteredBy    int64 // who recorded it; differs from UserID for a proxy entry
	AssignmentID int64
	StartedAt    time.Time
	EndedAt      *time.Time
	// DurationSeconds is derived from the interval and stored, so that reporting
	// queries can sum in SQL without recomputing from timestamps.
	DurationSeconds int64
	Note            string
	Billable        bool
	Status          EntryStatus
	// TimeZone is the IANA zone the entry was recorded in. It decides which day
	// the entry belongs to, independently of where a report is later run from.
	TimeZone string
	// The billing snapshot. These are filled when the entry is billed and are
	// never recomputed afterwards, so an invoiced amount cannot change under a
	// later rate or policy change.
	RoundingRuleApplied string
	BillableSeconds     int64
	RateMinor           int64
	AmountMinor         int64
	Currency            string
	// Flagged marks an entry needing human review, e.g. a timer that ran past the
	// maximum. Flagged entries are excluded from totals until resolved.
	Flagged   bool
	CreatedAt time.Time
	UpdatedAt time.Time

	// Denormalised for display.
	AssignmentName string
	ProjectName    string
	CustomerName   string
	ColourKey      string
	Icon           string
	UserName       string
	EnteredByName  string
}

// Running reports whether the timer is still going.
func (e TimeEntry) Running() bool { return e.EndedAt == nil }

// IsProxy reports whether someone other than the owner recorded this entry.
func (e TimeEntry) IsProxy() bool { return e.EnteredBy != 0 && e.EnteredBy != e.UserID }

// Counts reports whether this entry contributes to totals. Pending proxy entries
// do not count until the subject accepts them, and flagged entries do not count
// until a human resolves them. Every aggregation goes through this rule rather
// than repeating the condition at each call site.
func (e TimeEntry) Counts() bool {
	return e.Status == StatusConfirmed && !e.Flagged
}

// ElapsedSeconds returns the duration of the entry, computing it live for a
// running timer so the UI can show a total that includes work in progress.
func (e TimeEntry) ElapsedSeconds(now time.Time) int64 {
	if e.EndedAt != nil {
		return e.DurationSeconds
	}
	elapsed := SecondsBetween(e.StartedAt, now)
	if elapsed < 0 {
		// Clock skew or an entry started "in the future" by a manual edit.
		return 0
	}
	return elapsed
}

// LocalDay returns the calendar date the entry belongs to: its start instant
// projected into the entry's own time zone. Using the entry's zone rather than
// the reader's is what stops a Monday evening in Stockholm from becoming a Sunday
// when the report is run from New York.
func (e TimeEntry) LocalDay() time.Time {
	loc := loadLocation(e.TimeZone)
	local := e.StartedAt.In(loc)
	return time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
}

// loadLocation resolves an IANA zone name, falling back to UTC. The time zone
// database is embedded in the binary (see cmd/timetracker), because a stock
// Windows machine has no system zoneinfo and would otherwise fail here in a way
// that appears on only one platform.
func loadLocation(name string) *time.Location {
	if name == "" {
		return time.UTC
	}
	loc, err := time.LoadLocation(name)
	if err != nil {
		return time.UTC
	}
	return loc
}

// Validate checks the invariants of an entry. Overlap with other entries is
// deliberately not checked: overlapping is legal, and this type cannot see its
// siblings anyway.
func (e TimeEntry) Validate() error {
	if e.AssignmentID == 0 {
		return invalid("an entry must be on an assignment")
	}
	if e.UserID == 0 {
		return invalid("an entry must belong to a user")
	}
	if e.StartedAt.IsZero() {
		return invalid("an entry must have a start time")
	}
	if e.EndedAt != nil {
		if e.EndedAt.Before(e.StartedAt) {
			return invalid("an entry cannot end before it starts")
		}
		if SecondsBetween(e.StartedAt, *e.EndedAt) > maxEntrySeconds {
			return invalid("an entry cannot be longer than %d days", maxEntrySeconds/86400)
		}
	}
	if len(e.Note) > 10000 {
		return invalid("note is too long (max 10000 characters)")
	}
	switch e.Status {
	case StatusConfirmed, StatusPending, StatusRejected, "":
	default:
		return invalid("unknown status %q", e.Status)
	}
	return nil
}

// maxEntrySeconds is an upper bound on a single entry (7 days). It exists to
// catch data-entry accidents such as a mistyped year, which would otherwise
// produce a total nobody can explain.
const maxEntrySeconds = 7 * 24 * 3600

// Tag is a cross-cutting label, independent of the customer hierarchy.
type Tag struct {
	ID        int64
	Name      string
	ColourKey string
}
