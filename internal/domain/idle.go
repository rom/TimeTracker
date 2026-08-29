package domain

import (
	"fmt"
	"time"
)

// Idle observations, and what resolving one does to an entry.
//
// The vocabulary is deliberately about what was seen rather than about what it
// meant. The application observes that its own page stopped running, or that
// nothing was typed or clicked in it; it does not observe that a person was
// away, and cannot. Which of those a stretch was is the person's to say, and
// that is the whole reason a resolution exists as a stored decision rather than
// as a rule applied automatically (ADR-0033).

// IdleSource is what the page saw.
type IdleSource string

const (
	// IdleAsleep means the page's clock jumped: wall-clock time passed while
	// nothing in the tab ran. Either the machine slept or the browser suspended
	// the tab, and from inside the page those are indistinguishable - which is
	// why this is named for the observation rather than for the machine.
	IdleAsleep IdleSource = "asleep"
	// IdleUntouched means the page ran and was visible throughout, and saw no
	// pointer, key or scroll event. The weaker of the two: a visible tab on a
	// second monitor is untouched all day by somebody working hard.
	IdleUntouched IdleSource = "untouched"
)

// KnownIdleSource reports whether s is a source this application records.
func KnownIdleSource(s IdleSource) bool {
	return s == IdleAsleep || s == IdleUntouched
}

// IdleResolution is what a person decided about an observed stretch.
type IdleResolution string

const (
	// IdleKeep means the stretch was work. Nothing changes.
	IdleKeep IdleResolution = "keep"
	// IdleDiscard ends the entry where the stretch began. Anything after it is
	// dropped, which is right when the person walked away and never came back,
	// and wrong when they did - so the interface says how much would be lost.
	IdleDiscard IdleResolution = "discard"
	// IdleSplit ends the entry where the stretch began and records a second
	// entry for what followed, so a break is removed without losing the work on
	// the other side of it.
	IdleSplit IdleResolution = "split"
)

// KnownIdleResolution reports whether r is a decision this application accepts.
func KnownIdleResolution(r IdleResolution) bool {
	return r == IdleKeep || r == IdleDiscard || r == IdleSplit
}

// IdleObservation is one stretch of an entry during which the application saw
// nothing.
type IdleObservation struct {
	ID      int64
	EntryID int64
	UserID  int64
	// StartedAt and EndedAt bound the stretch, always inside the entry.
	StartedAt time.Time
	EndedAt   time.Time
	Source    IdleSource
	// Resolution is empty until somebody decides.
	Resolution IdleResolution
	ResolvedAt time.Time
	CreatedAt  time.Time

	// Denormalised for display, so a review panel does not need the entry too.
	AssignmentName string
	ProjectName    string
	CustomerName   string
	ColourKey      string
	EntryNote      string
}

// Seconds is the length of the observed stretch.
func (o IdleObservation) Seconds() int64 {
	return SecondsBetween(o.StartedAt, o.EndedAt)
}

// Resolved reports whether somebody has already decided about this one.
func (o IdleObservation) Resolved() bool { return o.Resolution != "" }

// IdleOutcome is what applying a resolution does, as plain intervals, so the
// service can write it and the interface can describe it beforehand from the
// same arithmetic. Computing "what would happen" and "what happens" in one
// function is what keeps the sentence on the button true.
type IdleOutcome struct {
	// KeepFrom and KeepTo are the entry's interval afterwards. Both ends can
	// move: a stretch in the middle or at the end shortens it from the back, a
	// stretch at the very beginning shortens it from the front.
	KeepFrom time.Time
	KeepTo   time.Time
	// SplitFrom and SplitTo bound the second entry, when there is one.
	SplitFrom time.Time
	SplitTo   time.Time
	Splits    bool
	// KeptSeconds is what remains on the timesheet across both entries, and
	// RemovedSeconds what leaves it: the observed stretch for a split, the
	// stretch plus everything after it for a discard.
	KeptSeconds    int64
	RemovedSeconds int64
}

// ClampIdle fits an observed stretch inside the entry it belongs to.
//
// The times come from a browser, which is to say from a clock nobody controls
// and a request anybody can forge. The clamp is not primarily a security
// measure - an observation can only ever offer its own owner a choice - but a
// stretch reaching outside its entry would make every figure downstream
// nonsense, and a machine whose clock is a few minutes off is ordinary rather
// than hostile.
//
// It returns false when nothing usable is left: a stretch that missed the entry
// entirely, or one shorter than the threshold once trimmed.
func ClampIdle(entry TimeEntry, from, to time.Time, now time.Time, minimumSeconds int64) (time.Time, time.Time, bool) {
	end := now
	if !entry.Running() {
		end = *entry.EndedAt
	}
	if end.After(now) {
		// An entry ending in the future is already wrong, but it must not let
		// an observation claim time that has not happened.
		end = now
	}

	if from.Before(entry.StartedAt) {
		from = entry.StartedAt
	}
	if to.After(end) {
		to = end
	}
	if !to.After(from) {
		return time.Time{}, time.Time{}, false
	}
	if minimumSeconds > 0 && SecondsBetween(from, to) < minimumSeconds {
		return time.Time{}, time.Time{}, false
	}
	return from, to, true
}

// ResolveIdle computes what a resolution does to an entry.
//
// The entry must be stopped: a resolution rewrites an interval, and rewriting
// one that is still being measured would race the timer. That is why the
// interface offers the choice after stopping and only reports the observation
// before.
func ResolveIdle(entry TimeEntry, o IdleObservation, resolution IdleResolution) (IdleOutcome, error) {
	if entry.Running() {
		return IdleOutcome{}, fmt.Errorf("cannot resolve idle time on a running timer")
	}
	if !KnownIdleResolution(resolution) {
		return IdleOutcome{}, fmt.Errorf("unknown resolution %q", resolution)
	}

	entryEnd := *entry.EndedAt
	total := SecondsBetween(entry.StartedAt, entryEnd)

	if resolution == IdleKeep {
		return IdleOutcome{
			KeepFrom: entry.StartedAt, KeepTo: entryEnd, KeptSeconds: total,
		}, nil
	}

	// The stretch is clamped when it is recorded, so this is a guard against a
	// row written before a change rather than an expected case.
	from, to := o.StartedAt, o.EndedAt
	if from.Before(entry.StartedAt) {
		from = entry.StartedAt
	}
	if to.After(entryEnd) {
		to = entryEnd
	}
	if !to.After(from) {
		return IdleOutcome{}, fmt.Errorf("the observed stretch is not inside the entry")
	}

	before := SecondsBetween(entry.StartedAt, from)
	after := SecondsBetween(to, entryEnd)
	stretch := SecondsBetween(from, to)

	switch {
	case before <= 0 && after <= 0:
		// The stretch is the entry. There is nothing to keep either side of it,
		// and an entry of zero length is not a record of anything, so this is
		// refused rather than silently emptied - deleting the entry is a
		// different decision, and one the person can already make.
		return IdleOutcome{}, fmt.Errorf("that would leave nothing of the entry; delete it instead")

	case before <= 0:
		// The stretch begins where the entry does, so the entry starts after it
		// instead. A discard would additionally drop the work that followed,
		// which for a leading stretch is the entire entry - so both decisions
		// mean the same thing here, and it is the harmless one.
		return IdleOutcome{
			KeepFrom: to, KeepTo: entryEnd,
			KeptSeconds: after, RemovedSeconds: stretch,
		}, nil

	case resolution == IdleDiscard, after <= 0:
		// A trailing stretch has no second entry to make, so a split is a
		// discard by another name. Answered as one rather than refused: it is
		// the same request.
		return IdleOutcome{
			KeepFrom: entry.StartedAt, KeepTo: from,
			KeptSeconds: before, RemovedSeconds: total - before,
		}, nil

	default:
		return IdleOutcome{
			KeepFrom: entry.StartedAt, KeepTo: from,
			SplitFrom: to, SplitTo: entryEnd, Splits: true,
			KeptSeconds: before + after, RemovedSeconds: stretch,
		}, nil
	}
}
