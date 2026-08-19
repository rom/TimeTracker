package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// applySettings posts the settings form with the presentation fields set,
// filling in the rest with values the validator accepts.
func applySettings(t *testing.T, srv *Server, overrides url.Values) {
	t.Helper()
	form := url.Values{
		"default_currency": {"SEK"}, "default_rounding": {"none"}, "default_rate": {"0"},
		"week_start": {"1"}, "max_timer_hours": {"12"},
	}
	for key, values := range overrides {
		form[key] = values
	}
	if rec := post(t, srv, "/settings", form); rec.Code >= 400 {
		t.Fatalf("save settings = %d\n%s", rec.Code, rec.Body.String())
	}
}

// TestNavigationPositionIsOneAttribute.
//
// The whole argument for doing it this way is that the markup does not change:
// the same elements in the same source order, laid out differently, so the
// reading order a screen reader follows and the tab order a keyboard follows
// are identical in both arrangements.
func TestNavigationPositionIsOneAttribute(t *testing.T) {
	srv, _ := newTestServer(t)

	applySettings(t, srv, url.Values{"nav_position": {"top"}})
	top := get(t, srv, "/today").Body.String()

	applySettings(t, srv, url.Values{"nav_position": {"left"}})
	left := get(t, srv, "/today").Body.String()

	if !strings.Contains(left, `data-nav="left"`) {
		t.Error("the left arrangement is not declared on the document")
	}
	if !strings.Contains(top, `data-nav="top"`) {
		t.Error("the top arrangement is not declared on the document")
	}

	// The navigation itself must be byte-identical between the two.
	if navOf(t, top) != navOf(t, left) {
		t.Error("the navigation markup differs between the two arrangements; " +
			"moving it must be a matter of layout only")
	}
}

// navOf extracts the main navigation block from a rendered page.
func navOf(t *testing.T, body string) string {
	t.Helper()
	start := strings.Index(body, `<nav class="mainnav"`)
	end := strings.Index(body, "</nav>")
	if start < 0 || end < start {
		t.Fatal("the page has no main navigation")
	}
	return body[start:end]
}

// TestUnknownNavigationPositionDegrades: a stored value nobody recognises must
// not leave the page with no layout at all.
func TestUnknownNavigationPositionDegrades(t *testing.T) {
	srv, _ := newTestServer(t)
	applySettings(t, srv, url.Values{"nav_position": {"diagonal"}})

	body := get(t, srv, "/today").Body.String()
	if !strings.Contains(body, `data-nav="top"`) {
		t.Error("an unrecognised position should fall back to the top bar")
	}
}

// TestClockAndDateFormatsFollowTheSetting.
func TestClockAndDateFormatsFollowTheSetting(t *testing.T) {
	srv, _ := newTestServer(t)

	applySettings(t, srv, url.Values{
		"clock_format": {"24h"}, "date_format": {"iso"},
		"show_clock": {"on"}, "show_time_and_date": {"on"},
	})
	body := get(t, srv, "/today").Body.String()
	if !strings.Contains(body, `data-hour12="0"`) {
		t.Error("the browser-side clock was not told to use a 24-hour clock")
	}

	applySettings(t, srv, url.Values{
		"clock_format": {"12h"}, "date_format": {"mdy"},
		"show_clock": {"on"}, "show_time_and_date": {"on"},
	})
	body = get(t, srv, "/today").Body.String()
	if !strings.Contains(body, `data-hour12="1"`) {
		t.Error("the browser-side clock was not told to use a 12-hour clock; " +
			"it would flip format the moment the script ran")
	}
	// The server's own first render has to agree with what the script will do.
	if strings.Contains(body, `>13:`) || strings.Contains(body, `>14:`) {
		t.Error("the server rendered a 24-hour time under a 12-hour setting")
	}
}

// TestDayWindowIsHonoured, and that an impossible one falls back rather than
// making the day screen unreachable.
func TestDayWindowIsHonoured(t *testing.T) {
	srv, _ := newTestServer(t)

	applySettings(t, srv, url.Values{
		"day_start_hour": {"6"}, "day_end_hour": {"14"}, "day_overflow": {"arrows"}})
	body := get(t, srv, "/today").Body.String()
	if !strings.Contains(body, `data-start-hour="6" data-end-hour="14"`) {
		t.Error("the configured window is not the one drawn")
	}

	// Reversed: an obvious slip with an obvious intent.
	applySettings(t, srv, url.Values{"day_start_hour": {"17"}, "day_end_hour": {"9"}})
	body = get(t, srv, "/today").Body.String()
	if !strings.Contains(body, `data-start-hour="9" data-end-hour="17"`) {
		t.Error("a window typed the wrong way round should be put the right way round")
	}

	// Impossible: falls back to the default rather than refusing.
	applySettings(t, srv, url.Values{"day_start_hour": {"5"}, "day_end_hour": {"5"}})
	if rec := get(t, srv, "/today"); rec.Code != http.StatusOK {
		t.Fatalf("the day screen = %d after an impossible window", rec.Code)
	}
	if !strings.Contains(get(t, srv, "/today").Body.String(), `data-start-hour="8"`) {
		t.Error("an empty window should fall back to the default, not be stored")
	}
}

// TestBackupPasswordIsNeverRenderedBack.
//
// It is stored in the clear because it has to be usable, which is only
// defensible if it never leaves the settings row. A template that could print
// it would undo that in one line.
func TestBackupPasswordIsNeverRenderedBack(t *testing.T) {
	srv, _ := newTestServer(t)
	const secret = "correct-horse-battery-staple"

	applySettings(t, srv, url.Values{"backup_password": {secret}})

	for _, path := range []string{"/settings", "/backup", "/admin", "/today"} {
		if body := get(t, srv, path).Body.String(); strings.Contains(body, secret) {
			t.Errorf("the backup password appears on %s", path)
		}
	}
	// But the screen must say that one is set, or nobody can tell.
	if !strings.Contains(get(t, srv, "/settings").Body.String(), "password is set") {
		t.Error("the settings screen does not say a password is set")
	}
}

// TestBackupPasswordSurvivesAnUnrelatedSave: the field is blank on every render,
// so a blank submission must mean "unchanged" rather than "clear it".
func TestBackupPasswordSurvivesAnUnrelatedSave(t *testing.T) {
	srv, _ := newTestServer(t)

	applySettings(t, srv, url.Values{"backup_password": {"a good long password"}})
	applySettings(t, srv, url.Values{"week_start": {"7"}})

	if !strings.Contains(get(t, srv, "/settings").Body.String(), "password is set") {
		t.Error("saving an unrelated setting silently disabled backup encryption")
	}

	// And the checkbox is the way to actually remove it.
	applySettings(t, srv, url.Values{"clear_backup_password": {"1"}})
	if strings.Contains(get(t, srv, "/settings").Body.String(), "password is set") {
		t.Error("the clear checkbox did not remove the password")
	}
}
