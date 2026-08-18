package domain

import (
	"strings"
	"time"
)

// Expense is a cost incurred while doing the work.
//
// The design point that matters: **billable** and **reimbursable** are separate
// questions, and this application never conflates them. A taxi the consultant
// paid for and re-charges is both. A hotel the client booked directly is
// neither. Someone's own train ticket on an internal project is reimbursable
// but not billable. Reports total them separately because they are different
// kinds of money: one joins an invoice, the other is owed back to a person.
type Expense struct {
	ID     int64
	UserID int64
	// EnteredBy differs from UserID for an expense recorded on someone's behalf,
	// exactly as for a time entry.
	EnteredBy int64
	ProjectID int64
	// SpentOn is a calendar date, not an instant: a receipt has a day.
	SpentOn      string
	Category     string
	Description  string
	AmountMinor  int64
	Currency     string
	Billable     bool
	Reimbursable bool
	// MarkupPercent applies only to the billable side.
	MarkupPercent int64
	// A quantity-priced expense: 42.5 km at 2.50/km, or 3 days of per diem. The
	// quantity is in thousandths of a unit so a distance stays exact without a
	// float touching a persisted field. Unit is empty for an ordinary expense
	// whose amount was simply typed in.
	QuantityMilli int64
	Unit          ExpenseUnit
	UnitRateMinor int64
	// BilledMinor is the amount to invoice, including markup, frozen when the
	// expense is recorded - the same reasoning that freezes a rate onto a time
	// entry (docs/adr/0014-exact-money-and-duration.md).
	BilledMinor int64
	Status      EntryStatus
	CreatedAt   time.Time
	UpdatedAt   time.Time

	// Denormalised for display.
	ProjectName     string
	CustomerID      int64
	CustomerName    string
	UserName        string
	EnteredByName   string
	AttachmentCount int
}

// Counts reports whether this expense contributes to totals, on the same rule as
// a time entry: a proposal made on someone's behalf does not count until they
// accept it.
func (e Expense) Counts() bool { return e.Status == StatusConfirmed }

// IsProxy reports whether someone other than the subject recorded this.
func (e Expense) IsProxy() bool { return e.EnteredBy != 0 && e.EnteredBy != e.UserID }

// Amount returns the cost as Money.
func (e Expense) Amount() Money { return NewMoney(e.AmountMinor, e.Currency) }

// Billed returns the amount to invoice, including any markup.
func (e Expense) Billed() Money { return NewMoney(e.BilledMinor, e.Currency) }

// ApplyMarkup computes the billed amount from the cost and the markup.
//
// A non-billable expense is billed at nothing, rather than carrying a hidden
// figure that some later change could start invoicing.
func (e *Expense) ApplyMarkup() {
	if !e.Billable {
		e.BilledMinor = 0
		return
	}
	e.BilledMinor = e.Amount().ApplyPercent(e.MarkupPercent).Minor
}

// Validate checks the rules that hold regardless of storage.
func (e Expense) Validate() error {
	if e.ProjectID == 0 {
		return invalid("an expense must belong to a project")
	}
	if e.UserID == 0 {
		return invalid("an expense must belong to a user")
	}
	if _, err := time.Parse("2006-01-02", e.SpentOn); err != nil {
		return invalid("an expense needs a date in YYYY-MM-DD form, got %q", e.SpentOn)
	}
	if e.AmountMinor < 0 {
		return invalid("an expense amount cannot be negative")
	}
	if e.AmountMinor == 0 {
		return invalid("an expense needs an amount")
	}
	if e.MarkupPercent < 0 || e.MarkupPercent > 1000 {
		return invalid("markup must be between 0 and 1000 percent")
	}
	if len(e.Description) > 2000 {
		return invalid("description is too long (max 2000 characters)")
	}
	// An expense that is neither billable nor reimbursable is a cost nobody
	// owes anybody, which is almost always a mistake in the form rather than an
	// intention. It is allowed - a client-paid hotel is a real thing to record -
	// but it is worth nothing to any total, and that is the caller's problem to
	// surface, not this type's to forbid.
	return nil
}

// Attachment is a file or photograph attached to a time entry or an expense.
//
// The bytes live on disk, content-addressed by their SHA-256; this describes
// them. See docs/adr/0013-attachment-storage.md.
type Attachment struct {
	ID int64
	// OwnerType is "time_entry" or "expense".
	OwnerType string
	OwnerID   int64
	// SHA256 is both the integrity check and the storage path.
	SHA256 string
	// Filename is what the user's file was called. It is shown to people and is
	// never used as a path component, which removes traversal and
	// case-collision problems in one move.
	Filename string
	// MIME is determined by sniffing the content on the server, never taken
	// from the client's claim.
	MIME       string
	SizeBytes  int64
	UploadedBy int64
	CreatedAt  time.Time
}

// IsImage reports whether this can be shown inline as a thumbnail.
func (a Attachment) IsImage() bool {
	return strings.HasPrefix(a.MIME, "image/")
}

// ShortHash returns the first twelve hex characters, which is plenty to
// distinguish files by eye and short enough to display.
func (a Attachment) ShortHash() string {
	if len(a.SHA256) < 12 {
		return a.SHA256
	}
	return a.SHA256[:12]
}

// Owner types, as stored.
const (
	AttachmentOwnerTimeEntry = "time_entry"
	AttachmentOwnerExpense   = "expense"
)
