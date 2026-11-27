package service

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
)

// Reading a calendar as a leave register.
//
// The shape of a national holiday feed: all-day events, no times, published
// once a year by every country that has one. The time import throws every one
// of these away ("all day, so it has no length"), which is correct for a
// timesheet and is why this exists separately.

const holidayFeed = "BEGIN:VCALENDAR\r\nVERSION:2.0\r\n" +
	"BEGIN:VEVENT\r\nUID:midsummer\r\nDTSTART;VALUE=DATE:20260619\r\n" +
	"DTEND;VALUE=DATE:20260620\r\nSUMMARY:Midsommarafton\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:christmas\r\nDTSTART;VALUE=DATE:20261224\r\n" +
	"DTEND;VALUE=DATE:20261226\r\nSUMMARY:Julafton\r\nEND:VEVENT\r\n" +
	"BEGIN:VEVENT\r\nUID:standup\r\nDTSTART:20260619T090000Z\r\n" +
	"DTEND:20260619T091500Z\r\nSUMMARY:Standup\r\nEND:VEVENT\r\n" +
	"END:VCALENDAR\r\n"

func TestAllDayEventsBecomeDaysAwayAndMeetingsDoNot(t *testing.T) {
	f := newFixture(t)

	preview, err := f.svc.ParseCalendarAsAbsence(f.ctx,
		strings.NewReader(holidayFeed), domain.AbsenceHoliday)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if preview.Fatal != "" {
		t.Fatalf("the file was refused: %s", preview.Fatal)
	}

	if preview.Importable != 2 {
		t.Errorf("%d importable, want 2: the two all-day events", preview.Importable)
	}
	// One day plus two: a calendar states the end exclusively, so 24 December
	// to 26 December is Christmas Eve and Christmas Day, not three days.
	if preview.Days != 3 {
		t.Errorf("%d days, want 3: an exclusive end date was read as inclusive", preview.Days)
	}

	for _, row := range preview.Rows {
		switch row.UID {
		case "midsummer":
			if row.StartsOn != "2026-06-19" || row.EndsOn != "2026-06-19" {
				t.Errorf("midsummer is %s–%s, want one day on 2026-06-19",
					row.StartsOn, row.EndsOn)
			}
		case "christmas":
			if row.EndsOn != "2026-12-25" {
				t.Errorf("christmas ends on %s, want 2026-12-25: a calendar's "+
					"end date is the morning after", row.EndsOn)
			}
		case "standup":
			if row.Skip == "" {
				t.Error("a fifteen-minute meeting was offered as a day off")
			}
		}
	}
}

func TestImportingAHolidayFeedRecordsTheDaysAway(t *testing.T) {
	f := newFixture(t)

	result, err := f.svc.ImportCalendarAsAbsence(f.ctx,
		strings.NewReader(holidayFeed), domain.AbsenceHoliday,
		map[string]bool{"midsummer": true, "christmas": true, "standup": true})
	if err != nil {
		t.Fatalf("import: %v", err)
	}
	if result.Imported != 2 {
		t.Errorf("imported %d, want 2", result.Imported)
	}
	// The meeting was ticked and still refused: the preview's judgement stands.
	if len(result.Skipped) != 1 {
		t.Errorf("skipped %v, want the meeting named", result.Skipped)
	}

	days, err := f.svc.absentDaysFor(f.ctx, f.user.ID,
		time.Date(2026, 12, 23, 0, 0, 0, 0, time.UTC),
		time.Date(2026, 12, 27, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("absent days: %v", err)
	}
	for _, day := range []string{"2026-12-24", "2026-12-25"} {
		if _, away := days[day]; !away {
			t.Errorf("%s was not recorded as a day away", day)
		}
	}
	if _, away := days["2026-12-26"]; away {
		t.Error("26 December was recorded, but the calendar's end date is the morning after")
	}
}

// Importing next year's feed over this year's is the normal way this is used,
// and the fresh UIDs in it must not produce a second Christmas.
func TestReimportingAFeedDoesNotRecordTheSameDayTwice(t *testing.T) {
	f := newFixture(t)

	if _, err := f.svc.ImportCalendarAsAbsence(f.ctx,
		strings.NewReader(holidayFeed), domain.AbsenceHoliday,
		map[string]bool{"midsummer": true, "christmas": true}); err != nil {
		t.Fatalf("first import: %v", err)
	}

	preview, err := f.svc.ParseCalendarAsAbsence(f.ctx,
		strings.NewReader(holidayFeed), domain.AbsenceHoliday)
	if err != nil {
		t.Fatalf("second parse: %v", err)
	}
	if preview.Duplicates != 2 {
		t.Errorf("%d duplicates, want 2: the same days are already recorded", preview.Duplicates)
	}
	if preview.Importable != 0 {
		t.Errorf("%d still importable, want 0", preview.Importable)
	}
}

// A public holiday is everybody's day off, so recording one is administration.
// The import must not be a way around that, and must refuse before it writes
// anything rather than halfway down the file.
func TestAMemberCannotImportAFeedAsPublicHolidays(t *testing.T) {
	f := newFixture(t)

	stranger, err := f.db.CreateUser(f.ctx, domain.User{
		DisplayName: "Member", Role: domain.RoleMember,
		TimeZone: "UTC", Theme: "light", Active: true,
	})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}

	office, _ := officeContext(t, f)
	ctx := auth.WithUser(f.ctx, stranger)

	result, err := office.ImportCalendarAsAbsence(ctx,
		strings.NewReader(holidayFeed), domain.AbsencePublic,
		map[string]bool{"midsummer": true, "christmas": true})
	if err != nil && !errors.Is(err, ErrForbidden) && !errors.Is(err, ErrNotFound) {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.Imported != 0 {
		t.Errorf("a member recorded %d public holidays for the whole instance",
			result.Imported)
	}
}

// Ticking a day that is already in the register does not record it twice.
//
// The preview unticks duplicates, but a form can be submitted with anything in
// it, and the import is where the answer has to be true. This was wrong first:
// the loop below checked the parser's refusals and not the duplicate flag, so
// re-importing a feed recorded every holiday a second time.
func TestATickedDuplicateIsStillRefused(t *testing.T) {
	f := newFixture(t)

	first, err := f.svc.ImportCalendarAsAbsence(f.ctx,
		strings.NewReader(holidayFeed), domain.AbsenceHoliday,
		map[string]bool{"christmas": true})
	if err != nil {
		t.Fatalf("first import: %v", err)
	}
	if first.Imported != 1 {
		t.Fatalf("imported %d, want 1", first.Imported)
	}

	again, err := f.svc.ImportCalendarAsAbsence(f.ctx,
		strings.NewReader(holidayFeed), domain.AbsenceHoliday,
		map[string]bool{"christmas": true})
	if err != nil {
		t.Fatalf("second import: %v", err)
	}
	if again.Imported != 0 {
		t.Errorf("imported %d the second time, want 0: Christmas is already "+
			"in the register", again.Imported)
	}
	if len(again.Skipped) != 1 {
		t.Errorf("skipped %v, want Christmas named as already recorded", again.Skipped)
	}
}
