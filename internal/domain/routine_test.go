package domain

import (
	"testing"
	"time"
)

// Routines: which days one applies to, and the encoding of the weekday list.

// TestRoutineAppliesOn is where Sunday matters. Go counts Sunday as 0 and ISO
// counts it as 7; the stored list is ISO, and getting the conversion wrong
// offers Monday's routines on Sunday.
func TestRoutineAppliesOn(t *testing.T) {
	weekdays := Routine{Weekdays: []int{1, 2, 3, 4, 5}, Active: true}
	sundays := Routine{Weekdays: []int{7}, Active: true}

	// 2026-03-16 is a Monday, so the week runs Monday to Sunday from there.
	monday := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	for offset, name := range []string{"Mon", "Tue", "Wed", "Thu", "Fri", "Sat", "Sun"} {
		day := monday.AddDate(0, 0, offset)
		wantWeekday := offset < 5
		if got := weekdays.AppliesOn(day); got != wantWeekday {
			t.Errorf("%s: weekday routine applies = %v, want %v", name, got, wantWeekday)
		}
		wantSunday := offset == 6
		if got := sundays.AppliesOn(day); got != wantSunday {
			t.Errorf("%s: Sunday routine applies = %v, want %v", name, got, wantSunday)
		}
	}
}

// TestInactiveRoutineNeverApplies: keeping a routine rather than deleting it is
// how somebody pauses the thing that happens every week until a project does.
func TestInactiveRoutineNeverApplies(t *testing.T) {
	paused := Routine{Weekdays: []int{1, 2, 3, 4, 5}, Active: false}
	monday := time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC)
	if paused.AppliesOn(monday) {
		t.Error("an inactive routine was offered")
	}
}

func TestWeekdayEncoding(t *testing.T) {
	if got := FormatWeekdays([]int{1, 3, 5}); got != "1,3,5" {
		t.Errorf("FormatWeekdays = %q", got)
	}
	if got := FormatWeekdays(nil); got != "" {
		t.Errorf("FormatWeekdays(nil) = %q", got)
	}

	// Sorted and de-duplicated on the way back, so "5,1,1" and "1,5" are the
	// same routine rather than two that behave identically and look different.
	got := ParseWeekdays("5,1,1")
	if len(got) != 2 || got[0] != 1 || got[1] != 5 {
		t.Errorf("ParseWeekdays(\"5,1,1\") = %v, want [1 5]", got)
	}
	// Nonsense is dropped rather than failing: this value comes back from the
	// database, and one bad character should not take a screen down.
	if got := ParseWeekdays("1,banana,9,0,3"); len(got) != 2 || got[0] != 1 || got[1] != 3 {
		t.Errorf("ParseWeekdays with junk = %v, want [1 3]", got)
	}
	if got := ParseWeekdays(""); len(got) != 0 {
		t.Errorf("ParseWeekdays(\"\") = %v", got)
	}
}

func TestRoutineValidate(t *testing.T) {
	valid := Routine{
		AssignmentID: 1, Name: "Stand-up", Weekdays: []int{1},
		DurationSeconds: 900, StartTime: "09:15",
	}
	if err := valid.Validate(); err != nil {
		t.Fatalf("a valid routine was rejected: %v", err)
	}

	cases := map[string]func(*Routine){
		"no name":            func(r *Routine) { r.Name = "  " },
		"no assignment":      func(r *Routine) { r.AssignmentID = 0 },
		"no days":            func(r *Routine) { r.Weekdays = nil },
		"a day out of range": func(r *Routine) { r.Weekdays = []int{8} },
		"no length":          func(r *Routine) { r.DurationSeconds = 0 },
		"a negative length":  func(r *Routine) { r.DurationSeconds = -60 },
		"a bad time":         func(r *Routine) { r.StartTime = "half nine" },
		"an unknown kind":    func(r *Routine) { r.Kind = "guesswork" },
	}
	for name, break_ := range cases {
		t.Run(name, func(t *testing.T) {
			candidate := valid
			break_(&candidate)
			if err := candidate.Validate(); err == nil {
				t.Error("accepted")
			}
		})
	}

	// An empty start time is legal: it means "no particular time", and the
	// entry is placed at the start of the working day.
	noTime := valid
	noTime.StartTime = ""
	if err := noTime.Validate(); err != nil {
		t.Errorf("a routine with no start time was rejected: %v", err)
	}
}

func TestWeekdayNames(t *testing.T) {
	names := WeekdayNames()
	if len(names) != 7 {
		t.Fatalf("got %d names, want 7", len(names))
	}
	if names[0].Number != 1 || names[6].Number != 7 {
		t.Errorf("the numbering is not ISO: %+v", names)
	}
}
