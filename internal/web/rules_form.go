package web

import (
	"net/http"
	"strconv"
	"strings"

	"github.com/rom/timetracker/internal/domain"
)

// Decoding a customer's rate rules from a form.
//
// One function, used by both the create and the edit path. Two copies of this
// would eventually disagree, and a rule silently dropped on one of the two
// paths is an under-billed invoice nobody notices until the customer does.

// rateRulesFromForm reads the overtime, travel and reimbursement fields.
//
// Every field is optional and an empty one means "not set", which the domain
// treats as inherit. That is why blank and zero have to stay distinguishable
// here: "0% travel" and "no travel terms agreed" are different contracts.
func rateRulesFromForm(r *http.Request, currency string) (domain.RateRules, error) {
	rules := domain.RateRules{
		TravelBilling:  domain.TravelBilling(r.FormValue("travel_billing")),
		ExpenseBilling: domain.ExpenseBilling(r.FormValue("expense_billing")),
	}

	var err error
	money := func(field string) int64 {
		if err != nil {
			return 0
		}
		var value int64
		if value, err = parseRate(r.FormValue(field), currency); err != nil {
			return 0
		}
		return value
	}
	percent := func(field string) int64 {
		if err != nil {
			return 0
		}
		var value int64
		if value, err = parsePercent(r.FormValue(field)); err != nil {
			return 0
		}
		return value
	}
	hours := func(field string) int64 {
		if err != nil {
			return 0
		}
		var value int64
		if value, err = parseHoursAsSeconds(r.FormValue(field)); err != nil {
			return 0
		}
		return value
	}

	rules.OvertimeRateMinor = money("overtime_rate")
	rules.OvertimeMultiplierPct = percent("overtime_multiplier")
	rules.OvertimeDailyThresholdSeconds = hours("overtime_daily_threshold")
	rules.OvertimeWeeklyThresholdSeconds = hours("overtime_weekly_threshold")

	rules.TravelRateMinor = money("travel_rate")
	rules.TravelMultiplierPct = percent("travel_multiplier")

	rules.ExpenseMarkupPct = percent("expense_markup")
	rules.MileageRateMinor = money("mileage_rate")
	rules.PerDiemMinor = money("per_diem")
	rules.ReceiptRequiredAboveMinor = money("receipt_required_above")

	if err != nil {
		return domain.RateRules{}, err
	}
	// Validated here as well as in the domain so the message names the screen
	// the user is looking at rather than surfacing as a bare store error.
	if err := rules.Validate(); err != nil {
		return domain.RateRules{}, err
	}
	return rules, nil
}

// parsePercent reads a whole-percent field. Empty is zero, meaning "not set".
func parsePercent(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	// A decimal comma reaches here from a Swedish keyboard; a percentage with a
	// fraction is not something these rules express, so it is refused by name
	// rather than silently truncated.
	if strings.ContainsAny(raw, ".,") {
		return 0, domainValidation("a percentage must be a whole number, got " + strconv.Quote(raw))
	}
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0, domainValidation("could not read the percentage " + strconv.Quote(raw))
	}
	if value < 0 {
		return 0, domainValidation("a percentage cannot be negative")
	}
	return value, nil
}

// parseHoursAsSeconds reads a threshold typed in hours ("8", "37.5") as seconds.
//
// Thresholds are entered in hours because that is how a contract states them -
// "over eight hours a day", "over forty hours a week" - and stored in seconds
// because every other duration in this application is.
func parseHoursAsSeconds(raw string) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return 0, nil
	}
	milli, err := domain.ParseQuantityMilli(raw)
	if err != nil {
		return 0, domainValidation("could not read the threshold " + strconv.Quote(raw))
	}
	// Thousandths of an hour to seconds: 3600/1000 with the division last, so
	// 7.5 hours is exactly 27000 rather than something a rounding step touched.
	return milli * 3600 / 1000, nil
}

// formatHours renders a seconds threshold back as the hours a user typed.
func formatHours(seconds int64) string {
	if seconds == 0 {
		return ""
	}
	return domain.FormatQuantity(seconds * 1000 / 3600)
}
