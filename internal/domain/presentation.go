package domain

// How the application presents itself: where the navigation sits, how a clock
// and a date are written, and which hours the day pane covers.
//
// These live in the domain rather than in the web layer because they are
// validated in one place and read in three - the settings form, the page shell
// and the timeline builder - and because an unrecognised value must degrade to
// a known one rather than reaching a template as a mystery string.

// NavPosition is where the main navigation sits.
type NavPosition string

const (
	// NavTop is the horizontal bar across the head of the page.
	NavTop NavPosition = "top"
	// NavLeft is a vertical rail down the side. It fits more items without
	// crowding and keeps them in a fixed order, which is what makes it worth
	// offering once the navigation has ten entries in it.
	NavLeft NavPosition = "left"
)

// NavPositions are the choices, in the order the form offers them.
var NavPositions = []NavPosition{NavTop, NavLeft}

// Valid reports whether this is a position the stylesheet knows how to draw.
func (p NavPosition) Valid() bool {
	return p == NavTop || p == NavLeft
}

// OrDefault degrades an unrecognised value to the top bar rather than to
// nothing: a page whose navigation has no layout at all is unusable, and a
// stored value nobody recognises should not be able to cause that.
func (p NavPosition) OrDefault() NavPosition {
	if p.Valid() {
		return p
	}
	return NavTop
}

// MessageKey is the catalogue key naming this position.
func (p NavPosition) MessageKey() string { return "settings.nav." + string(p.OrDefault()) }

// ClockFormat is whether times are written on a 24- or 12-hour clock.
type ClockFormat string

const (
	// ClockAuto follows the interface language. It is the default because it is
	// what the application did before the setting existed, and an upgrade must
	// not silently reformat every time on every screen.
	ClockAuto ClockFormat = "auto"
	Clock24   ClockFormat = "24h"
	Clock12   ClockFormat = "12h"
)

// ClockFormats are the choices, in the order the form offers them.
var ClockFormats = []ClockFormat{ClockAuto, Clock24, Clock12}

func (c ClockFormat) Valid() bool {
	return c == ClockAuto || c == Clock24 || c == Clock12
}

func (c ClockFormat) OrDefault() ClockFormat {
	if c.Valid() {
		return c
	}
	return ClockAuto
}

func (c ClockFormat) MessageKey() string { return "settings.clock." + string(c.OrDefault()) }

// DateFormat is the order a date's parts are written in.
type DateFormat string

const (
	// DateAuto follows the interface language.
	DateAuto DateFormat = "auto"
	// DateISO is 2026-08-19: the Swedish everyday format, unambiguous
	// everywhere, and what this application used before the setting existed.
	DateISO DateFormat = "iso"
	// DateDMY is 19/08/2026 and DateMDY is 08/19/2026. Both are offered because
	// both are what somebody's accounting department asks for; neither is a
	// default, because 03/04 is genuinely ambiguous between them and a
	// timesheet is not the place to guess.
	DateDMY DateFormat = "dmy"
	DateMDY DateFormat = "mdy"
)

// DateFormats are the choices, in the order the form offers them.
var DateFormats = []DateFormat{DateAuto, DateISO, DateDMY, DateMDY}

func (d DateFormat) Valid() bool {
	switch d {
	case DateAuto, DateISO, DateDMY, DateMDY:
		return true
	default:
		return false
	}
}

func (d DateFormat) OrDefault() DateFormat {
	if d.Valid() {
		return d
	}
	return DateAuto
}

func (d DateFormat) MessageKey() string { return "settings.date." + string(d.OrDefault()) }

// DayOverflow is what the day pane does with time recorded outside its window.
type DayOverflow string

const (
	// DayOverflowExpand grows the pane until everything fits. Right for somebody
	// who works late once a month; wrong for somebody who does it nightly, whose
	// ordinary day then lives in the top third of the pane every day.
	DayOverflowExpand DayOverflow = "expand"
	// DayOverflowArrows keeps the window fixed and marks what falls outside it,
	// so the scale of an ordinary hour never changes.
	DayOverflowArrows DayOverflow = "arrows"
)

// DayOverflows are the choices, in the order the form offers them.
var DayOverflows = []DayOverflow{DayOverflowExpand, DayOverflowArrows}

func (o DayOverflow) Valid() bool {
	return o == DayOverflowExpand || o == DayOverflowArrows
}

func (o DayOverflow) OrDefault() DayOverflow {
	if o.Valid() {
		return o
	}
	return DayOverflowExpand
}

func (o DayOverflow) MessageKey() string { return "settings.overflow." + string(o.OrDefault()) }

// DayWindow is the pane's default range of hours.
type DayWindow struct {
	StartHour int
	EndHour   int
}

// DefaultDayWindow is a working day: eight in the morning to six in the evening.
// It is what the timeline was hard-coded to before this was configurable.
var DefaultDayWindow = DayWindow{StartHour: 8, EndHour: 18}

// NormaliseDayWindow clamps a stored or submitted window to something drawable.
//
// A window must lie inside a day and must not be empty. Rather than refusing -
// which would let one bad row make the day screen unreachable - an impossible
// window falls back to the default, and a merely reversed one is put the right
// way round, because "6 to 18" typed as "18 to 6" is a slip with an obvious
// intent.
func NormaliseDayWindow(start, end int) DayWindow {
	if start > end {
		start, end = end, start
	}
	if start < 0 || end > 24 || start >= end {
		return DefaultDayWindow
	}
	return DayWindow{StartHour: start, EndHour: end}
}
