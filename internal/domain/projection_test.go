package domain

import (
	"reflect"
	"testing"
	"time"
)

// The projections, field by field.
//
// This file is the enforcement of ADR-0008's central promise - that what a
// reader is not entitled to is "removed before the data leaves the service
// layer, so a template bug cannot leak them" - and the failure mode is a field
// added to a struct a year from now that nobody thinks to remove. Every test
// here therefore names both halves: what goes, and what stays. A field in
// neither list fails the test, so adding one is a decision rather than an
// omission.

// filled returns the value with every field set to something non-zero, so that
// "this field was removed" is distinguishable from "this field was never set".
func filled[T any](t *testing.T, value T) T {
	t.Helper()

	v := reflect.ValueOf(&value).Elem()
	for i := 0; i < v.NumField(); i++ {
		field := v.Field(i)
		if !field.CanSet() {
			continue
		}
		switch field.Kind() {
		case reflect.String:
			field.SetString("something")
		case reflect.Int64, reflect.Int:
			field.SetInt(77)
		case reflect.Bool:
			field.SetBool(true)
		case reflect.Ptr:
			if field.Type().Elem() == reflect.TypeOf(time.Time{}) {
				when := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
				field.Set(reflect.ValueOf(&when))
			}
		case reflect.Slice:
			if field.Type().Elem().Kind() == reflect.String {
				field.Set(reflect.ValueOf([]string{"something"}))
			}
		case reflect.Struct:
			if field.Type() == reflect.TypeOf(time.Time{}) {
				field.Set(reflect.ValueOf(time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)))
			}
		}
	}
	return value
}

// assertProjection checks that exactly the named fields were zeroed.
func assertProjection[T any](t *testing.T, what string, before, after T, removed map[string]string, kept []string) {
	t.Helper()

	keptSet := map[string]bool{}
	for _, name := range kept {
		keptSet[name] = true
	}

	beforeValue := reflect.ValueOf(before)
	afterValue := reflect.ValueOf(after)
	for i := 0; i < beforeValue.NumField(); i++ {
		name := beforeValue.Type().Field(i).Name
		was := beforeValue.Field(i)
		is := afterValue.Field(i)
		zero := is.IsZero()

		why, shouldGo := removed[name]
		switch {
		case shouldGo && !zero:
			t.Errorf("%s: %s survived the projection (%v), and it should not: %s",
				what, name, is.Interface(), why)
		case keptSet[name] && zero && !was.IsZero():
			t.Errorf("%s: %s was removed and it should have stayed", what, name)
		case !shouldGo && !keptSet[name]:
			t.Errorf("%s: %s is in neither list. Decide whether this reader may "+
				"see it: add it to the removed map with the reason, or to the "+
				"kept list.", what, name)
		}
	}
}

// TestACustomerForAClient.
func TestACustomerForAClient(t *testing.T) {
	before := filled(t, Customer{})
	assertProjection(t, "Customer.ForClient", before, before.ForClient(),
		map[string]string{
			"Notes":     "written by staff for staff",
			"RateMinor": "the negotiated rate is the whole point of the role",
			"HasTerms":  "a badge saying the account bills differently is a small leak but a leak",
		},
		[]string{"ID", "Name", "Code", "Currency", "ColourKey", "Icon",
			"ArchivedAt", "CreatedAt"})
}

// TestACustomerForAColleagueWhoMayNotSeeMoney.
//
// A narrower removal than the client's, for a different reader: a member is
// staff, so the internal note is theirs and the rate is not.
func TestACustomerForAColleagueWhoMayNotSeeMoney(t *testing.T) {
	before := filled(t, Customer{})
	assertProjection(t, "Customer.WithoutMoney", before, before.WithoutMoney(),
		map[string]string{
			"RateMinor": "ADR-0008 gives a member own rates only",
		},
		[]string{"ID", "Name", "Code", "Currency", "ColourKey", "Icon", "Notes",
			"HasTerms", "ArchivedAt", "CreatedAt"})
}

// TestAProjectWithoutItsCommercialTerms.
//
// The same four fields for both readers, which is why ForClient delegates: a
// rate and a rounding rule are how the work is billed, and a budget is somebody's
// estimate of the engagement.
func TestAProjectWithoutItsCommercialTerms(t *testing.T) {
	removed := map[string]string{
		"RateMinor":     "how the work is billed",
		"RoundingRule":  "how the time is billed",
		"BudgetSeconds": "somebody's estimate of the engagement",
		"BudgetMinor":   "as BudgetSeconds, in money",
	}
	kept := []string{"ID", "CustomerID", "Name", "Code", "ColourKey", "Icon",
		"BillableDefault", "ArchivedAt", "CreatedAt", "CustomerName", "Currency"}

	before := filled(t, Project{})
	assertProjection(t, "Project.WithoutMoney", before, before.WithoutMoney(), removed, kept)
	assertProjection(t, "Project.ForClient", before, before.ForClient(), removed, kept)
}

// TestAnAssignmentWithoutItsRate.
//
// The most specific of the three rates, overriding the project's and the
// customer's - so it is the one a member working on it would read off a screen.
// Nothing narrowed assignments at all until it was given this.
func TestAnAssignmentWithoutItsRate(t *testing.T) {
	before := filled(t, Assignment{})
	assertProjection(t, "Assignment.WithoutMoney", before, before.WithoutMoney(),
		map[string]string{
			"RateMinor": "the most specific of the three rates",
		},
		[]string{"ID", "ProjectID", "Name", "Code", "ColourKey", "Icon",
			"BillableDefault", "SortOrder", "Favourite", "ArchivedAt",
			"CreatedAt", "ProjectName", "CustomerID", "CustomerName", "Currency"})
}

// TestAnEntryForAClient.
func TestAnEntryForAClient(t *testing.T) {
	before := filled(t, TimeEntry{})
	assertProjection(t, "TimeEntry.ForClient", before, before.ForClient(),
		map[string]string{
			"Note":                "written by staff for staff, mid-working-day",
			"RateMinor":           "a client's bill is an invoice, not a portal column",
			"AmountMinor":         "as RateMinor",
			"Currency":            "as RateMinor",
			"RoundingRuleApplied": "as RateMinor",
			"EnteredBy":           "internal workflow",
			"EnteredByName":       "internal workflow",
			"DecidedBy":           "a decision on a proxy proposal is internal",
			"DecidedAt":           "as DecidedBy",
			"DecisionNote":        "as DecidedBy",
			"Tags":                "internal labels, and nothing stops one being a candid opinion",
			"AttachmentCount":     "a client cannot fetch an attachment",
		},
		[]string{"ID", "UserID", "AssignmentID", "StartedAt", "EndedAt",
			"DurationSeconds", "Billable", "Kind", "Status", "TimeZone",
			"BillableSeconds", "Flagged", "CreatedAt", "UpdatedAt",
			"AssignmentName", "ProjectID", "ProjectName", "CustomerID",
			"CustomerName", "ColourKey", "Icon", "UserName"})
}

// TestNarrowingAListingNarrowsEveryRow.
//
// Each listing helper exists because a caller that narrowed one record would be
// called in a loop somebody eventually writes wrongly - so the loop is written
// once, here, and tested on more than one row.
func TestNarrowingAListingNarrowsEveryRow(t *testing.T) {
	customers := []Customer{filled(t, Customer{}), filled(t, Customer{})}
	for i, got := range CustomersWithoutMoney(append([]Customer{}, customers...)) {
		if got.RateMinor != 0 {
			t.Errorf("customer %d kept its rate", i)
		}
	}
	for i, got := range ProjectCustomersForClient(append([]Customer{}, customers...)) {
		if got.RateMinor != 0 || got.Notes != "" {
			t.Errorf("customer %d kept its rate or notes for a client", i)
		}
	}

	projects := []Project{filled(t, Project{}), filled(t, Project{})}
	for i, got := range ProjectsWithoutMoney(append([]Project{}, projects...)) {
		if got.RateMinor != 0 || got.BudgetSeconds != 0 {
			t.Errorf("project %d kept money", i)
		}
	}
	for i, got := range ProjectProjectsForClient(append([]Project{}, projects...)) {
		if got.RateMinor != 0 || got.BudgetMinor != 0 {
			t.Errorf("project %d kept money for a client", i)
		}
	}

	assignments := []Assignment{filled(t, Assignment{}), filled(t, Assignment{})}
	for i, got := range AssignmentsWithoutMoney(append([]Assignment{}, assignments...)) {
		if got.RateMinor != 0 {
			t.Errorf("assignment %d kept its rate", i)
		}
	}

	entries := []TimeEntry{filled(t, TimeEntry{}), filled(t, TimeEntry{})}
	for i, got := range ProjectEntriesForClient(append([]TimeEntry{}, entries...)) {
		if got.Note != "" || got.AmountMinor != 0 || got.Tags != nil {
			t.Errorf("entry %d kept what a client may not see", i)
		}
	}

	// An empty listing is an empty listing rather than a panic, which is the
	// case a loop written against len-1 gets wrong.
	if got := CustomersWithoutMoney(nil); got != nil {
		t.Errorf("narrowing nothing gave %v", got)
	}
}
