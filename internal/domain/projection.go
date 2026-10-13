package domain

import "time"

// The narrowed shape a client sees.
//
// ADR-0008 promised that a client receives "a deliberately narrowed projection
// of the data, not a filtered view of the full record - internal notes, cost
// rates, and colleague identities are removed before the data leaves the service
// layer, so a template bug cannot leak them". This is that removal.
//
// It lives in the domain rather than in a template helper for exactly the reason
// the ADR gives. A template that forgets a condition renders a note; a struct
// that never held the note cannot. Everything a client is not entitled to is
// gone from the value by the time anything outside the service can see it -
// including the export writers, which never learn there was a note.

// ForClient returns the entry as a client may see it.
//
// What survives: when the work happened, how long it took, which customer,
// project and assignment it was against, and who did it. That is the answer to
// "what am I paying for", which is the whole reason the role exists.
//
// What does not, and why:
//
//   - The note. It is written by staff for staff, in the middle of a working
//     day, with no expectation of an audience - which is precisely when
//     somebody writes "chasing them again about the invoice".
//   - Money: the rate, the amount, the currency, and the rounding rule that
//     produced them. A client's bill is an invoice, which is a document somebody
//     sends deliberately; it is not a column on a portal that updates as work is
//     recorded and re-priced. The authorisation model already refuses a client
//     the money action, and this makes that true of the data rather than of the
//     question.
//   - Who entered it, and any decision on a proxy proposal. That somebody
//     recorded time on a colleague's behalf, and that the colleague accepted or
//     rejected it, is internal workflow. The subject of the entry - who did the
//     work - is what the client is told, which is what ADR-0008 means by "no
//     personnel detail beyond who did the work".
//   - Tags. They are internal labels chosen for internal filing, and nothing
//     stops one being a candid opinion.
//   - The attachment count. A client cannot fetch an attachment, and a count of
//     files they cannot see is an invitation to ask about them.
//
// The billable duration stays: it is a quantity rather than a price, and it is
// the figure a client's own records are most likely to be reconciled against.
func (e TimeEntry) ForClient() TimeEntry {
	e.Note = ""

	e.RateMinor = 0
	e.AmountMinor = 0
	e.Currency = ""
	e.RoundingRuleApplied = ""

	e.EnteredBy = 0
	e.EnteredByName = ""
	e.DecidedBy = 0
	e.DecidedAt = time.Time{}
	e.DecisionNote = ""

	e.Tags = nil
	e.AttachmentCount = 0

	return e
}

// ProjectEntriesForClient narrows a whole listing.
//
// A slice rather than one entry, because every read path in the service returns
// a slice and a helper that took one would be called in a loop somebody
// eventually writes wrongly.
func ProjectEntriesForClient(entries []TimeEntry) []TimeEntry {
	for i := range entries {
		entries[i] = entries[i].ForClient()
	}
	return entries
}

// ForClient returns the customer as a client may see it.
//
// The catalogue carries cost data too, and it was the first place this leaked:
// a client opening the administration screen was shown their own customer's
// negotiated hourly rate, in a table, beside forms they could not submit. The
// name and the code are what a client needs to recognise their own account on a
// report; the rate and the internal notes are the two things the role exists to
// withhold.
func (c Customer) ForClient() Customer {
	c = c.WithoutMoney()
	c.Notes = ""
	// Whether a customer bills under dated terms, and what those terms are, is
	// commercial. A badge saying "this account bills differently" is a small
	// leak but a leak.
	c.HasTerms = false
	return c
}

// WithoutMoney returns the customer with its commercial figures removed and
// everything a colleague may read left in place.
//
// A narrower removal than ForClient, for a different reader: a member is staff,
// so the internal note is theirs, and the negotiated rate is not. ADR-0008 gives
// a member "own rates only" - which the catalogue screen did not honour, because
// the only narrowing that existed was the client's and nothing applied it to
// anybody else.
func (c Customer) WithoutMoney() Customer {
	c.RateMinor = 0
	return c
}

// ForClient returns the project as a client may see it.
//
// The rate, the rounding rule and the budget are all commercial. The budget in
// particular is somebody's estimate of the engagement, which the client is often
// negotiating against.
func (p Project) ForClient() Project {
	return p.WithoutMoney()
}

// WithoutMoney returns the project with its commercial figures removed.
//
// The same four fields the client projection removes, and for the same reasons:
// a rate and a rounding rule are how the work is billed, and a budget is
// somebody's estimate of the engagement. A member is not negotiating against it,
// but they are not entitled to it either - and a member who happens to work on
// the project is exactly who would mention it.
func (p Project) WithoutMoney() Project {
	p.RateMinor = 0
	p.RoundingRule = ""
	p.BudgetSeconds = 0
	p.BudgetMinor = 0
	return p
}

// WithoutMoney returns the assignment with its rate removed.
//
// An assignment's rate is the most specific of the three - it overrides the
// project's, which overrides the customer's - so it is the one a member working
// on it would most easily read off a screen. Nothing narrowed assignments at
// all until now, for any role.
func (a Assignment) WithoutMoney() Assignment {
	a.RateMinor = 0
	return a
}

// ProjectCustomersForClient and ProjectProjectsForClient narrow listings, for
// the same reason ProjectEntriesForClient does.
func ProjectCustomersForClient(customers []Customer) []Customer {
	for i := range customers {
		customers[i] = customers[i].ForClient()
	}
	return customers
}

// ProjectProjectsForClient narrows a listing of projects.
func ProjectProjectsForClient(projects []Project) []Project {
	for i := range projects {
		projects[i] = projects[i].ForClient()
	}
	return projects
}

// CustomersWithoutMoney, ProjectsWithoutMoney and AssignmentsWithoutMoney narrow
// listings for a reader who may not see commercial figures, for the same reason
// the client helpers above exist: a helper that took one record would be called
// in a loop somebody eventually writes wrongly.
func CustomersWithoutMoney(customers []Customer) []Customer {
	for i := range customers {
		customers[i] = customers[i].WithoutMoney()
	}
	return customers
}

// ProjectsWithoutMoney narrows a listing of projects.
func ProjectsWithoutMoney(projects []Project) []Project {
	for i := range projects {
		projects[i] = projects[i].WithoutMoney()
	}
	return projects
}

// AssignmentsWithoutMoney narrows a listing of assignments.
func AssignmentsWithoutMoney(assignments []Assignment) []Assignment {
	for i := range assignments {
		assignments[i] = assignments[i].WithoutMoney()
	}
	return assignments
}
