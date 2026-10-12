package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// Dated contract terms, through the screen that edits them.
//
// These fields are a contract: the overtime multiplier, what travel is billed
// at, the markup on expenses, the threshold above which a receipt is required.
// A field silently dropped on the way in is an under-billed invoice nobody
// notices until the customer does - which is why the round trip is asserted
// field by field rather than by sampling.

// fullTerms is every rule on the form, filled in with values that are all
// distinct, so one landing in another's column is visible.
func fullTerms() url.Values {
	return url.Values{
		"effective_from":            {"2026-01-01"},
		"overtime_rate":             {"120"},
		"overtime_multiplier":       {"150"},
		"overtime_daily_threshold":  {"8"},
		"overtime_weekly_threshold": {"37.5"},
		"travel_billing":            {"rate"},
		"travel_rate":               {"50"},
		"travel_multiplier":         {"75"},
		"expense_billing":           {"yes"},
		"expense_markup":            {"10"},
		"mileage_rate":              {"2.50"},
		"per_diem":                  {"300"},
		"receipt_required_above":    {"1000"},
		"note":                      {"Signed contract"},
	}
}

// seedCustomer creates one customer to hang terms on and returns its terms URL.
func seedCustomer(t *testing.T, srv *Server) string {
	t.Helper()
	if rec := post(t, srv, "/customers", url.Values{
		"name": {"Acme"}, "currency": {"EUR"}, "rate": {"100"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("create customer = %d: %s", rec.Code, rec.Body.String())
	}
	return "/terms/customer/1"
}

// TestContractTermsRoundTripThroughTheForm.
//
// Saved, then read back out of the form that edits them. Reading the form
// rather than the database is the point: a rule that was stored but does not
// come back into the field is one somebody will overwrite with a blank the next
// time they edit anything on this screen.
func TestContractTermsRoundTripThroughTheForm(t *testing.T) {
	srv, _ := newTestServer(t)
	path := seedCustomer(t, srv)

	if rec := get(t, srv, path); rec.Code != http.StatusOK {
		t.Fatalf("GET %s = %d: %s", path, rec.Code, rec.Body.String())
	}

	if rec := post(t, srv, path, fullTerms()); rec.Code != http.StatusSeeOther {
		t.Fatalf("save terms = %d: %s", rec.Code, rec.Body.String())
	}

	// The revision is listed, with the date it takes effect and why it changed.
	listing := get(t, srv, path).Body.String()
	for _, shown := range []string{"Signed contract", "2026-01-01"} {
		if !strings.Contains(listing, shown) {
			t.Errorf("the terms listing does not show %q", shown)
		}
	}

	// And every field comes back into the edit form. The money fields come back
	// in the customer's currency with its minor unit shown, which is how they
	// are typed on the next edit.
	form := get(t, srv, path+"?edit=1").Body.String()
	for field, want := range map[string]string{
		"overtime_rate":             "120.00",
		"overtime_multiplier":       "150",
		"overtime_daily_threshold":  "8",
		"overtime_weekly_threshold": "37.5",
		"travel_rate":               "50.00",
		"travel_multiplier":         "75",
		"expense_markup":            "10",
		"mileage_rate":              "2.50",
		"per_diem":                  "300.00",
		"receipt_required_above":    "1000.00",
	} {
		if !fieldHasValue(form, field, want) {
			t.Errorf("the edit form's %s does not come back as %q", field, want)
		}
	}
	for field, want := range map[string]string{"travel_billing": "rate", "expense_billing": "yes"} {
		if !selectedOption(form, field, want) {
			t.Errorf("the edit form's %s does not come back with %q chosen", field, want)
		}
	}
}

// fieldHasValue reports whether a form input of that name carries that value.
func fieldHasValue(body, field, want string) bool {
	marker := `name="` + field + `"`
	for _, chunk := range strings.Split(body, marker)[1:] {
		// Only look within the same tag.
		tag := chunk
		if end := strings.Index(tag, ">"); end >= 0 {
			tag = tag[:end]
		}
		if strings.Contains(tag, `value="`+want+`"`) {
			return true
		}
	}
	return false
}

// TestBlankAndZeroAreDifferentContracts.
//
// "0% markup on expenses" and "no expense terms agreed" are not the same thing:
// the first is a rule, the second inherits whatever the customer's terms say.
// If the form collapsed blank to zero, attaching project terms to override one
// rule would silently zero every rule the customer had agreed.
func TestBlankAndZeroAreDifferentContracts(t *testing.T) {
	srv, _ := newTestServer(t)
	customerPath := seedCustomer(t, srv)

	post(t, srv, "/projects", url.Values{
		"customer_id": {"1"}, "name": {"Migration"}, "billable": {"on"},
	})

	// The customer agrees overtime at 150% and a 10% expense markup.
	if rec := post(t, srv, customerPath, url.Values{
		"effective_from":      {"2026-01-01"},
		"overtime_multiplier": {"150"},
		"expense_markup":      {"10"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("save customer terms = %d: %s", rec.Code, rec.Body.String())
	}

	// The project overrides only the markup, to zero. Every other field is
	// blank, which must mean "inherit" rather than "zero".
	if rec := post(t, srv, "/terms/project/1", url.Values{
		"effective_from": {"2026-01-01"},
		"expense_markup": {"0"},
	}); rec.Code != http.StatusSeeOther {
		t.Fatalf("save project terms = %d: %s", rec.Code, rec.Body.String())
	}

	// The project's screen shows what is in force, which is the override plus
	// what it inherited.
	inForce := get(t, srv, "/terms/project/1").Body.String()
	if !strings.Contains(inForce, "150") {
		t.Errorf("the customer's overtime rule was lost when the project overrode "+
			"one unrelated field:\n%s", inForce)
	}
}

// TestTermsAreRefusedRatherThanRounded.
//
// Every one of these is something a person can type. A percentage with a
// fraction is not something these rules express, so it is refused by name
// instead of being silently truncated to a different contract - and the message
// has to name the value, because that is the only way somebody finds their typo
// in a form of a dozen fields.
func TestTermsAreRefusedRatherThanRounded(t *testing.T) {
	srv, _ := newTestServer(t)
	path := seedCustomer(t, srv)

	cases := []struct {
		name  string
		field string
		value string
		says  string
	}{
		{"a fractional percentage", "overtime_multiplier", "150.5", "150.5"},
		{"a decimal comma, from a Swedish keyboard", "overtime_multiplier", "150,5", "150,5"},
		{"a negative percentage", "expense_markup", "-10", "negative"},
		{"words where a percentage goes", "travel_multiplier", "half again", "half again"},
		{"words where an amount goes", "per_diem", "three hundred", "three hundred"},
		{"words where a threshold goes", "overtime_daily_threshold", "all day", "all day"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			form := url.Values{"effective_from": {"2026-01-01"}}
			form.Set(tc.field, tc.value)

			rec := post(t, srv, path, form)
			if rec.Code == http.StatusSeeOther {
				t.Fatalf("%s=%q was accepted", tc.field, tc.value)
			}
			if rec.Code == http.StatusInternalServerError {
				t.Fatalf("%s=%q = 500, want a refusal naming the value", tc.field, tc.value)
			}
			if !strings.Contains(rec.Body.String(), tc.says) {
				t.Errorf("the refusal of %s=%q does not mention %q:\n%s",
					tc.field, tc.value, tc.says, rec.Body.String())
			}
		})
	}

	// Nothing was stored by any of them.
	if listing := get(t, srv, path).Body.String(); strings.Contains(listing, "2026-01-01") {
		t.Error("a refused set of terms was stored anyway")
	}
}

// TestAnUnknownContractScopeIsRefused.
//
// The scope comes out of the URL. Terms attach to a customer or a project and
// to nothing else, so a third word there is a refusal rather than a row with a
// scope nothing will ever read back.
func TestAnUnknownContractScopeIsRefused(t *testing.T) {
	srv, _ := newTestServer(t)
	seedCustomer(t, srv)

	for _, path := range []string{"/terms/employee/1", "/terms/customer/../1", "/terms//1"} {
		t.Run(path, func(t *testing.T) {
			rec := post(t, srv, path, url.Values{"effective_from": {"2026-01-01"}})
			if rec.Code == http.StatusSeeOther {
				t.Errorf("POST %s was accepted", path)
			}
			if rec.Code == http.StatusInternalServerError {
				t.Errorf("POST %s = 500, want a refusal", path)
			}
		})
	}

	// And on the delete route, which reads the scope the same way.
	if rec := post(t, srv, "/terms/employee/1/delete", url.Values{
		"terms_id": {"1"},
	}); rec.Code == http.StatusSeeOther {
		t.Error("deleting terms under an unknown scope was accepted")
	}
}

// TestDeletingARevisionLeavesTheOthers.
//
// Terms are a dated series: deleting last year's renewal must not take this
// year's with it, and must send the editor back to the screen they were on
// rather than to a dead URL.
func TestDeletingARevisionLeavesTheOthers(t *testing.T) {
	srv, _ := newTestServer(t)
	path := seedCustomer(t, srv)

	for _, from := range []string{"2026-01-01", "2026-07-01"} {
		form := url.Values{
			"effective_from":      {from},
			"overtime_multiplier": {"150"},
			"note":                {"Effective " + from},
		}
		if rec := post(t, srv, path, form); rec.Code != http.StatusSeeOther {
			t.Fatalf("save terms from %s = %d: %s", from, rec.Code, rec.Body.String())
		}
	}

	listing := get(t, srv, path).Body.String()
	for _, from := range []string{"2026-01-01", "2026-07-01"} {
		if !strings.Contains(listing, "Effective "+from) {
			t.Fatalf("the revision effective %s is not listed", from)
		}
	}

	rec := post(t, srv, path+"/delete", url.Values{"terms_id": {"1"}})
	if rec.Code != http.StatusSeeOther {
		t.Fatalf("delete = %d: %s", rec.Code, rec.Body.String())
	}
	if target := rec.Header().Get("Location"); target != path {
		t.Errorf("after deleting a revision the editor goes to %q, want %q", target, path)
	}

	after := get(t, srv, path).Body.String()
	if strings.Contains(after, "Effective 2026-01-01") {
		t.Error("the deleted revision is still listed")
	}
	if !strings.Contains(after, "Effective 2026-07-01") {
		t.Error("deleting one revision took another with it")
	}

	// Deleting it again is a stale button rather than a crash.
	if again := post(t, srv, path+"/delete", url.Values{
		"terms_id": {"1"},
	}); again.Code == http.StatusInternalServerError {
		t.Error("deleting an already-deleted revision = 500, want a no-op or a refusal")
	}
}

// selectedOption reports whether a select of that name has that option chosen.
func selectedOption(body, field, want string) bool {
	marker := `name="` + field + `"`
	start := strings.Index(body, marker)
	if start < 0 {
		return false
	}
	rest := body[start:]
	if end := strings.Index(rest, "</select>"); end >= 0 {
		rest = rest[:end]
	}
	for _, option := range strings.Split(rest, "<option")[1:] {
		if strings.Contains(option, `value="`+want+`"`) {
			return strings.Contains(option, "selected")
		}
	}
	return false
}
