package web

import (
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/i18n"
	"github.com/rom/timetracker/internal/service"
)

// pageData is what every full page render receives. The common fields (the
// acting user, the running timers, the theme) appear on every screen, so they are
// assembled once rather than by each handler.
type pageData struct {
	Title  string
	Active string // which navigation item to highlight
	User   domain.User
	Now    time.Time

	// Printer renders every user-visible string in the request's language.
	// Templates reach it as {{.T "key"}}, so no template needs to know how the
	// language was chosen.
	Printer *i18n.Printer
	// Lang is the BCP 47 tag for the document's lang attribute. A screen reader
	// picks its voice from this, so claiming Swedish while rendering English is
	// actively harmful rather than merely untidy.
	Lang      string
	Languages []i18n.Language

	// Help is the context-sensitive help for the current screen.
	Help       []helpSection
	HelpScreen string
	HasHelp    bool
	// Guide is the task-oriented user guide: how to perform an action, as
	// opposed to what the screen in front of you is. Empty except on /guide.
	Guide      []guideSection
	GuideTopic string
	// Running is drawn in the header on every screen, so a timer left going is
	// impossible to miss.
	Running     []domain.TimeEntry
	Themes      []string
	Assignments []domain.Assignment
	Recent      []domain.Assignment
	// Routines are the recurring templates due on the day being viewed, and the
	// one being edited on the management screen.
	Routines    []domain.Routine
	RoutinesDue []service.DueRoutine
	EditRoutine *domain.Routine
	Error       string

	// Screen-specific payloads.
	Day       *service.DayView
	Week      *service.WeekView
	Entries   []domain.TimeEntry
	Totals    domain.Totals
	Customers []domain.Customer
	Projects  []domain.Project
	Tags      []domain.Tag
	Entry     *domain.TimeEntry
	Filter    entryFilterForm
	// Zone is the acting user's IANA zone, for templates that render a stored
	// instant as a wall clock time.
	Zone *time.Location
	// Expenses, attachments and the proxy inbox.
	Expenses      []domain.Expense
	ExpenseTotals service.ExpenseTotals
	// NeedsReceipt marks expenses above their customer's evidence threshold with
	// nothing attached. A map so a template can ask about a row without the
	// template calling into the service.
	NeedsReceipt map[int64]bool
	Inbox        *service.Inbox
	Proposed     []domain.TimeEntry
	Attachments  []domain.Attachment

	// Dated contract terms: one scope's history, and the revision being edited.
	Terms     *service.TermsView
	EditTerms *domain.ContractTerms

	// Editing the catalogue. Non-nil when an edit form is being rendered.
	EditCustomer   *domain.Customer
	EditProject    *domain.Project
	EditAssignment *domain.Assignment

	// Weekly submit and approve.
	PeriodView *service.PeriodView
	// Overtime is where a customer's threshold has been passed without the time
	// being marked as overtime. Prompts, not findings.
	Overtime  []service.OvertimeNotice
	Approvals []domain.TimesheetPeriod
	Approved  []domain.TimesheetPeriod
	MyPeriods []domain.TimesheetPeriod
	// Approval status per person per week, and the span it covers.
	ApprovalReport *service.ApprovalReport
	ReportWeeks    int

	// Moving time.
	MoveIDs []string

	// CSV import.
	ImportPreview       *service.ImportPreview
	ImportCreateMissing bool

	// Calendar import. CalendarFile carries the uploaded text back to the
	// commit step so the file does not have to be found twice.
	CalendarPreview *service.CalendarPreview
	CalendarFile    string
	CalendarResult  *service.CalendarImportResult

	// Backup and restore.
	Backups        []service.BackupFile
	RestoreResult  *service.RestoreResult
	BackupEnabled  bool
	BackupInterval string
	BackupKeep     int

	// Instance settings, and the rounding presets the forms offer.
	Settings service.Settings
	Rounding []struct{ Key, MessageKey string }

	// Server-mode payloads. Nil or empty in local mode.
	Users   []domain.User
	Members []service.Membership
	Login   *loginData
	// CSRFToken is rendered into every form. Empty in local mode, where there is
	// no session to bind it to.
	CSRFToken string
	// ServerMode drives the parts of the chrome that only make sense with
	// accounts: the sign-out control and the Users screen.
	ServerMode bool
	// InboxCount badges the navigation, because a proposal nobody looks at is
	// unbilled work.
	InboxCount int
}

// T translates a key in the page's language. Templates call it directly, which
// keeps the language selection entirely out of the templates themselves.
func (d pageData) T(key string, args ...any) string {
	if d.Printer == nil {
		return key
	}
	return d.Printer.T(key, args...)
}

// N translates a countable message, choosing singular or plural.
func (d pageData) N(key string, count int, args ...any) string {
	if d.Printer == nil {
		return key
	}
	return d.Printer.N(key, count, args...)
}

// printerFor picks the language for a request.
//
// The order is: the user's stored preference, then the browser's Accept-Language
// header, then English. A stored preference wins because it is an explicit
// choice, and a browser configured by an employer should not override what
// someone selected here.
func printerFor(r *http.Request, user domain.User) *i18n.Printer {
	if user.Language != "" && i18n.Supported(user.Language) {
		return i18n.NewPrinter(user.Language)
	}
	return i18n.NewPrinter(i18n.Negotiate(r.Header.Get("Accept-Language")))
}

// availableThemes is the list offered in the theme switcher. It is defined here
// and in the stylesheet's token blocks; a theme missing from either is caught by
// the contrast test described in docs/TEST.md.
var availableThemes = []string{
	defaultTheme, "dark", "gold", "sand", "spring", "autumn", "terminal", "contrast",
}

// defaultTheme is the one declared on :root, and therefore the one in force
// before the switcher has said anything.
const defaultTheme = "light"

// newPageData assembles the fields every screen needs.
func (s *Server) newPageData(r *http.Request, title, active string) (pageData, error) {
	user, _ := auth.UserFrom(r.Context())

	running, err := s.svc.RunningTimers(r.Context())
	if err != nil {
		return pageData{}, err
	}
	printer := printerFor(r, user)

	// The instance settings drive the chrome - the clock and the date display -
	// so every page needs them. It is one indexed read of a single row.
	settings, err := s.svc.Settings(r.Context())
	if err != nil {
		return pageData{}, err
	}

	// The proxy inbox count is shown as a badge on every screen, because a
	// proposal nobody looks at is unbilled work.
	inboxCount := 0
	if inbox, inboxErr := s.svc.Inbox(r.Context()); inboxErr == nil {
		inboxCount = inbox.Count()
	}

	return pageData{
		Title:      title,
		Active:     active,
		User:       user,
		Now:        s.svc.Now(),
		Running:    running,
		Themes:     availableThemes,
		CSRFToken:  csrfTokenFrom(r),
		ServerMode: s.accounts != nil,
		Printer:    printer,
		Lang:       printer.Code(),
		Languages:  languageOptions(),
		HasHelp:    helpAvailable(active),
		HelpScreen: active,
		Settings:   settings,
		Rounding:   service.RoundingPresets(),
		InboxCount: inboxCount,
		Zone:       userLocation(r),
	}, nil
}

// handleSetLanguage stores the acting user's language.
//
// Like the theme, the browser applies nothing itself: the page reloads in the
// new language, because translating the current DOM in place would need the
// whole catalogue on the client for no benefit.
func (s *Server) handleSetLanguage(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	language := r.FormValue("language")

	// An allow-list, because the value ends up in the document's lang attribute.
	if language != "" && !i18n.Supported(language) {
		http.Error(w, "Unknown language.", http.StatusBadRequest)
		return
	}
	if err := s.svc.SetLanguage(r.Context(), language); err != nil {
		s.fail(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleHealth reports that the process is up.
//
// Deliberately thin: an unauthenticated endpoint should not describe the system
// to a stranger. The hardening summary is the one exception, and it is there
// because an operator needs a way to confirm from outside that the sandbox
// engaged - "I configured it" and "it took effect" are different claims. It
// names mechanisms, never paths or versions.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = fmt.Fprintf(w, `{"status":"ok","hardening":%q}`+"\n", s.hardening)
}

// handleToday renders the day view, defaulting to today.
func (s *Server) handleToday(w http.ResponseWriter, r *http.Request) {
	date := s.dateParam(r, "date")

	data, err := s.newPageData(r, "Today", "today")
	if err != nil {
		s.fail(w, r, err)
		return
	}

	day, err := s.svc.Day(r.Context(), date)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Day = &day
	data.Totals = day.Totals

	// Whether this day's week is locked, so the screen can say so rather than
	// offering controls whose only outcome is an error message.
	period, err := s.svc.Period(r.Context(), date)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.PeriodView = &period

	// What to offer as one-click starts: favourites first, then what this person
	// works on most. Capped at ten, because a row of thirty buttons is one
	// nobody clicks.
	if data.Recent, err = s.svc.QuickStart(r.Context(), 10); err != nil {
		s.fail(w, r, err)
		return
	}
	// The recurring templates due today, and whether each already looks done.
	if data.RoutinesDue, err = s.svc.RoutinesDue(r.Context(), date); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Tags, err = s.svc.Tags(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Assignments, err = s.svc.Assignments(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}

	s.render(w, r, "page_today.html", data)
}

// handleWeek renders the weekly grid.
func (s *Server) handleWeek(w http.ResponseWriter, r *http.Request) {
	date := s.dateParam(r, "date")

	data, err := s.newPageData(r, "Week", "week")
	if err != nil {
		s.fail(w, r, err)
		return
	}

	week, err := s.svc.Week(r.Context(), date)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Week = &week
	data.Totals = week.Totals

	// The week view is where submitting belongs: it is the screen showing
	// exactly what would be submitted.
	period, err := s.svc.Period(r.Context(), date)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.PeriodView = &period

	// Where a customer's overtime threshold has been passed without any of the
	// time being marked as such. A prompt, never a reclassification - the week
	// view is where somebody reviews their hours before submitting, which is
	// the moment this is worth raising.
	if data.Overtime, err = s.svc.OvertimeNotices(r.Context(), date); err != nil {
		s.fail(w, r, err)
		return
	}

	if data.Assignments, err = s.svc.Assignments(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_week.html", data)
}

// entryFilterForm is the Entries screen's filter, kept as a struct so the form
// can be re-rendered with the user's selections intact.
type entryFilterForm struct {
	From         string
	To           string
	CustomerID   int64
	ProjectID    int64
	AssignmentID int64
	BillableOnly bool
	// Tags and Query drive the search box; UseRegexp switches it from substring
	// to regular expression. Kind narrows to work, overtime or travel.
	Tags      string
	Query     string
	UseRegexp bool
	Kind      string
	// SearchMode is which mechanism answered, so the screen can say. A search
	// that quietly used a different one from the one asked for produces results
	// nobody can explain.
	SearchMode string
}

// handleEntries renders the filterable entry list, which is also what every
// export is generated from.
func (s *Server) handleEntries(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "Entries", "entries")
	if err != nil {
		s.fail(w, r, err)
		return
	}

	filter, form := s.entryFilter(r)
	data.Filter = form

	entries, mode, err := s.svc.SearchEntries(r.Context(), filter)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Entries = entries
	form.SearchMode = string(mode)
	data.Filter = form
	data.Totals = s.svc.Totals(data.Entries)

	// The tags that exist, for the filter's suggestions.
	if data.Tags, err = s.svc.Tags(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}

	if data.Customers, err = s.svc.Customers(r.Context(), false); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Assignments, err = s.svc.Assignments(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_entries.html", data)
}

// entryFilter reads the filter from the query string.
//
// It defaults to the last 30 days rather than to everything: an unbounded query
// on a multi-year database is slow and is almost never what someone wanted.
func (s *Server) entryFilter(r *http.Request) (service.EntryFilter, entryFilterForm) {
	now := s.svc.Now()
	form := entryFilterForm{
		From: r.URL.Query().Get("from"),
		To:   r.URL.Query().Get("to"),
	}

	from, err := time.Parse("2006-01-02", form.From)
	if err != nil {
		from = now.AddDate(0, 0, -30)
		form.From = from.Format("2006-01-02")
	}
	to, err := time.Parse("2006-01-02", form.To)
	if err != nil {
		to = now
		form.To = to.Format("2006-01-02")
	}

	form.CustomerID = int64Param(r.URL.Query().Get("customer"))
	form.ProjectID = int64Param(r.URL.Query().Get("project"))
	form.AssignmentID = int64Param(r.URL.Query().Get("assignment"))
	form.BillableOnly = r.URL.Query().Get("billable") == "1"
	form.Tags = r.URL.Query().Get("tags")
	form.Query = r.URL.Query().Get("q")
	form.UseRegexp = r.URL.Query().Get("regexp") == "1"
	form.Kind = r.URL.Query().Get("kind")

	var kinds []domain.EntryKind
	if kind := domain.EntryKind(form.Kind); kind.Valid() {
		kinds = []domain.EntryKind{kind}
	}

	return service.EntryFilter{
		// The end of the range is exclusive, so "to" is inclusive of that whole
		// day - which is what a user typing a date means.
		From:         time.Date(from.Year(), from.Month(), from.Day(), 0, 0, 0, 0, time.UTC),
		To:           time.Date(to.Year(), to.Month(), to.Day(), 0, 0, 0, 0, time.UTC).AddDate(0, 0, 1),
		CustomerID:   form.CustomerID,
		ProjectID:    form.ProjectID,
		AssignmentID: form.AssignmentID,
		BillableOnly: form.BillableOnly,
		Tags:         domain.ParseTagList(form.Tags),
		Query:        form.Query,
		UseRegexp:    form.UseRegexp,
		Kinds:        kinds,
		Limit:        1000,
	}, form
}

// handleStartTimer starts a timer. It does not stop any other: several may run at
// once (docs/adr/0004-concurrent-timers.md).
func (s *Server) handleStartTimer(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	assignmentID := int64Param(r.FormValue("assignment_id"))
	if assignmentID == 0 {
		http.Error(w, "Choose an assignment to start a timer on.", http.StatusBadRequest)
		return
	}

	if _, err := s.svc.StartTimer(r.Context(), assignmentID, r.FormValue("note")); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleStopTimer stops one timer. Stopping is idempotent, so a double-click or
// two open tabs cannot produce a doubled duration.
func (s *Server) handleStopTimer(w http.ResponseWriter, r *http.Request) {
	id := int64Param(r.PathValue("id"))
	if _, err := s.svc.StopTimer(r.Context(), id); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleStopAllTimers stops everything running, for the end of the day.
func (s *Server) handleStopAllTimers(w http.ResponseWriter, r *http.Request) {
	if _, err := s.svc.StopAllTimers(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleCreateEntry records time that has already happened.
func (s *Server) handleCreateEntry(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	input, err := s.entryInputFromForm(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if _, err := s.svc.CreateEntry(r.Context(), input); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleQuickAdd parses a single line into an entry.
//
// An ambiguous line is not guessed at: the user is told what could not be
// resolved, because a wrong guess that silently becomes billable time is worse
// than a second click.
func (s *Server) handleQuickAdd(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	_, parsed, err := s.svc.QuickAdd(r.Context(), r.FormValue("text"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if parsed.Ambiguous {
		http.Error(w, "Could not read that: "+parsed.Reason, http.StatusBadRequest)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleEditEntryForm renders the screen for correcting one entry.
//
// A whole screen rather than an inline field, because the mistake this exists to
// fix - eight minutes typed where eight hours were meant - is only obvious when
// the duration is shown next to the day, the start time and the assignment. An
// inline cell shows the wrong number in the same shape as the right one.
func (s *Server) handleEditEntryForm(w http.ResponseWriter, r *http.Request) {
	entry, err := s.svc.Entry(r.Context(), int64Param(r.PathValue("id")))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	data, err := s.newPageData(r, "", "today")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("entry.edit.title")
	data.Entry = &entry
	if data.Tags, err = s.svc.Tags(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Assignments, err = s.svc.Assignments(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	// The week's state is shown on the form, so somebody who cannot save an
	// edit learns that before typing it rather than after.
	if period, periodErr := s.svc.Period(r.Context(), entry.StartedAt); periodErr == nil {
		data.PeriodView = &period
	}
	s.render(w, r, "page_entry_edit.html", data)
}

// handleUpdateEntry saves an edit.
//
// The redirect goes to the day the entry now belongs to rather than back to
// wherever the form was opened from: an edit that moves an entry to another day
// would otherwise appear to have deleted it.
func (s *Server) handleUpdateEntry(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	input, err := s.entryInputFromForm(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	updated, err := s.svc.UpdateEntry(r.Context(), int64Param(r.PathValue("id")), input)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	target := "/today?date=" + updated.StartedAt.In(userLocation(r)).Format("2006-01-02")
	if isHTMX(r) {
		w.Header().Set("HX-Redirect", target)
		w.WriteHeader(http.StatusNoContent)
		return
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// handleDeleteEntry removes an entry. The prior state survives in the audit
// trail, so the deletion is still explicable afterwards.
func (s *Server) handleDeleteEntry(w http.ResponseWriter, r *http.Request) {
	if err := s.svc.DeleteEntry(r.Context(), int64Param(r.PathValue("id"))); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// entryInputFromForm decodes the manual entry form.
//
// It accepts either a duration or an explicit end time, because both are natural:
// "2h on the migration" and "09:00 to 10:30" are the same statement made two ways.
func (s *Server) entryInputFromForm(r *http.Request) (service.EntryInput, error) {
	loc := userLocation(r)

	kind := domain.EntryKind(r.FormValue("kind"))
	if kind != "" && !kind.Valid() {
		return service.EntryInput{}, domainValidation("unknown kind of time: " + r.FormValue("kind"))
	}

	input := service.EntryInput{
		AssignmentID: int64Param(r.FormValue("assignment_id")),
		Note:         r.FormValue("note"),
		Billable:     r.FormValue("billable") != "",
		Kind:         kind,
		Tags:         domain.ParseTagList(r.FormValue("tags")),
		OnBehalfOf:   int64Param(r.FormValue("on_behalf_of")),
	}

	// The date and start time are entered in the user's local zone; they become
	// an absolute instant here and are stored as UTC.
	date := r.FormValue("date")
	startTime := r.FormValue("start")
	if date == "" {
		date = s.svc.Now().In(loc).Format("2006-01-02")
	}
	if startTime == "" {
		startTime = "09:00"
	}
	started, err := time.ParseInLocation("2006-01-02 15:04", date+" "+startTime, loc)
	if err != nil {
		return service.EntryInput{}, domainValidation("could not read the date and time: " + err.Error())
	}
	input.StartedAt = started

	if endTime := r.FormValue("end"); endTime != "" {
		ended, err := time.ParseInLocation("2006-01-02 15:04", date+" "+endTime, loc)
		if err != nil {
			return service.EntryInput{}, domainValidation("could not read the end time: " + err.Error())
		}
		// An end before the start means the work ran past midnight.
		if !ended.After(started) {
			ended = ended.AddDate(0, 0, 1)
		}
		input.EndedAt = &ended
		return input, nil
	}

	if durationText := r.FormValue("duration"); durationText != "" {
		seconds, err := domain.ParseDuration(durationText)
		if err != nil {
			return service.EntryInput{}, domainValidation(
				"could not read the duration " + strconv.Quote(durationText) +
					": try 1.5, 1h30 or 90m")
		}
		input.DurationSeconds = seconds
	}
	return input, nil
}

// domainValidation wraps a message so the error mapping renders it as a 400 with
// the text shown to the user.
func domainValidation(message string) error {
	return validationError{message: message}
}

type validationError struct{ message string }

func (e validationError) Error() string { return e.message }

// Is makes errors.Is(err, service.ErrValidation) true for these, so they map onto
// a 400 alongside the domain's own validation failures.
func (e validationError) Is(target error) bool { return target == service.ErrValidation }

// refreshOrRedirect answers a mutation.
//
// For an HTMX request it asks the client to refresh the affected regions, so the
// page updates in place. For a plain form post - which is what happens with
// JavaScript disabled - it redirects back, giving the ordinary post/redirect/get
// behaviour that keeps a reload from repeating the action.
func (s *Server) refreshOrRedirect(w http.ResponseWriter, r *http.Request) {
	if isHTMX(r) {
		// HX-Refresh re-renders the current screen. It is one round trip more
		// than a targeted swap, and it is used here because every mutation
		// affects several regions at once (the timer header, the day list and
		// both totals); a partial update that missed one would show stale
		// numbers, which is worse than a redraw.
		w.Header().Set("HX-Refresh", "true")
		w.WriteHeader(http.StatusNoContent)
		return
	}

	target := r.Header.Get("Referer")
	if target == "" {
		target = "/today"
	}
	http.Redirect(w, r, target, http.StatusSeeOther)
}

// userLocation resolves the acting user's IANA zone.
//
// Dates on screen and dates in forms are both local ones; getting this wrong
// puts an entry on the wrong calendar day, which is the kind of error nobody
// spots until an invoice is queried. One function so there is one answer.
func userLocation(r *http.Request) *time.Location {
	user, _ := auth.UserFrom(r.Context())
	if user.TimeZone == "" {
		return time.UTC
	}
	parsed, err := time.LoadLocation(user.TimeZone)
	if err != nil {
		return time.UTC
	}
	return parsed
}

// dateParam reads a ?date=YYYY-MM-DD parameter, defaulting to today in the user's
// zone.
func (s *Server) dateParam(r *http.Request, name string) time.Time {
	loc := userLocation(r)
	if raw := r.URL.Query().Get(name); raw != "" {
		if parsed, err := time.ParseInLocation("2006-01-02", raw, loc); err == nil {
			return parsed
		}
	}
	return s.svc.Now().In(loc)
}

// int64Param parses an identifier from a form field or path segment, returning 0
// for anything unparseable. Handlers treat 0 as "not supplied"; the service layer
// then fails to find it, which is the correct answer either way.
func int64Param(raw string) int64 {
	value, err := strconv.ParseInt(raw, 10, 64)
	if err != nil {
		return 0
	}
	return value
}

// timeParseIn reads a YYYY-MM-DD date in a location.
func timeParseIn(raw string, loc *time.Location) (time.Time, error) {
	return time.ParseInLocation("2006-01-02", raw, loc)
}

// handleMoveEntryBlock adjusts one entry's start and length.
//
// The endpoint the timeline's drag and resize post to, and also what its
// keyboard controls use. It takes a start time and a length rather than a
// pixel offset: the browser converts, so the server never has to know how tall
// an hour is on somebody's screen, and the same request can be made by a form.
func (s *Server) handleMoveEntryBlock(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	entry, err := s.svc.Entry(r.Context(), int64Param(r.PathValue("id")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	loc := userLocation(r)

	// The day comes from the entry unless the drag moved it to another one.
	day := entry.StartedAt.In(loc).Format("2006-01-02")
	if raw := r.FormValue("date"); raw != "" {
		day = raw
	}
	started, err := time.ParseInLocation("2006-01-02 15:04", day+" "+r.FormValue("start"), loc)
	if err != nil {
		s.fail(w, r, domainValidation("could not read the new start time: "+err.Error()))
		return
	}

	seconds := entry.DurationSeconds
	if raw := r.FormValue("duration"); raw != "" {
		if seconds, err = domain.ParseDuration(raw); err != nil {
			s.fail(w, r, domainValidation("could not read the new length: "+err.Error()))
			return
		}
	}

	// Everything else about the entry is left alone. A drag moves time; it does
	// not silently change what the time was for, and reusing the full edit
	// input here would let a stale form field do exactly that.
	_, err = s.svc.UpdateEntry(r.Context(), entry.ID, service.EntryInput{
		AssignmentID:    entry.AssignmentID,
		StartedAt:       started,
		DurationSeconds: seconds,
		Note:            entry.Note,
		Billable:        entry.Billable,
		Kind:            entry.KindOrDefault(),
		Tags:            entry.Tags,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}
