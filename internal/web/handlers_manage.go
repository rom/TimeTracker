package web

import (
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/service"
)

// Editing the catalogue, moving time, favourites, settings, import and backup.

// handleEditCustomer returns the edit form for one customer.
func (s *Server) handleEditCustomer(w http.ResponseWriter, r *http.Request) {
	customer, err := s.svc.Customer(r.Context(), int64Param(r.PathValue("id")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.newPageData(r, "", "admin")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page.EditCustomer = &customer
	s.renderFragment(w, r, "page_admin.html", "customer-edit-form", page)
}

// handleUpdateCustomer saves an edited customer.
func (s *Server) handleUpdateCustomer(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id := int64Param(r.PathValue("id"))

	existing, err := s.svc.Customer(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rate, err := parseRate(r.FormValue("rate"), r.FormValue("currency"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	existing.Name = r.FormValue("name")
	existing.Code = r.FormValue("code")
	existing.Currency = strings.ToUpper(r.FormValue("currency"))
	existing.ColourKey = r.FormValue("colour_key")
	existing.Icon = r.FormValue("icon")
	existing.Notes = r.FormValue("notes")
	existing.RateMinor = rate

	if err := s.svc.UpdateCustomer(r.Context(), existing); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleEditProject returns the edit form for one project.
func (s *Server) handleEditProject(w http.ResponseWriter, r *http.Request) {
	project, err := s.svc.Project(r.Context(), int64Param(r.PathValue("id")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.newPageData(r, "", "admin")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if page.Customers, err = s.svc.Customers(r.Context(), false); err != nil {
		s.fail(w, r, err)
		return
	}
	page.EditProject = &project
	s.renderFragment(w, r, "page_admin.html", "project-edit-form", page)
}

// handleUpdateProject saves an edited project.
//
// The customer may change: a project created under the wrong client is a real
// mistake, and the alternative to fixing it is re-creating the project and
// moving every entry.
func (s *Server) handleUpdateProject(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id := int64Param(r.PathValue("id"))

	existing, err := s.svc.Project(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rate, err := parseRate(r.FormValue("rate"), r.FormValue("currency"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	existing.Name = r.FormValue("name")
	existing.Code = r.FormValue("code")
	existing.ColourKey = r.FormValue("colour_key")
	existing.Icon = r.FormValue("icon")
	existing.BillableDefault = r.FormValue("billable") != ""
	existing.RateMinor = rate
	existing.RoundingRule = r.FormValue("rounding_rule")
	if customerID := int64Param(r.FormValue("customer_id")); customerID != 0 {
		existing.CustomerID = customerID
	}

	if err := s.svc.UpdateProject(r.Context(), existing); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleEditAssignment returns the edit form for one assignment.
func (s *Server) handleEditAssignment(w http.ResponseWriter, r *http.Request) {
	assignment, err := s.svc.Assignment(r.Context(), int64Param(r.PathValue("id")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	page, err := s.newPageData(r, "", "admin")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if page.Projects, err = s.svc.Projects(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	page.EditAssignment = &assignment
	s.renderFragment(w, r, "page_admin.html", "assignment-edit-form", page)
}

// handleUpdateAssignment saves an edited assignment.
func (s *Server) handleUpdateAssignment(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id := int64Param(r.PathValue("id"))

	existing, err := s.svc.Assignment(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rate, err := parseRate(r.FormValue("rate"), r.FormValue("currency"))
	if err != nil {
		s.fail(w, r, err)
		return
	}

	existing.Name = r.FormValue("name")
	existing.Code = r.FormValue("code")
	existing.ColourKey = r.FormValue("colour_key")
	existing.Icon = r.FormValue("icon")
	existing.BillableDefault = r.FormValue("billable") != ""
	existing.Favourite = r.FormValue("favourite") != ""
	existing.RateMinor = rate
	if projectID := int64Param(r.FormValue("project_id")); projectID != 0 {
		existing.ProjectID = projectID
	}

	if err := s.svc.UpdateAssignment(r.Context(), existing); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleToggleFavourite flips an assignment's favourite flag.
//
// One click from the day view and from the admin list, because reshaping the
// working set is something people do constantly and burying it in an edit form
// makes it something they never do.
func (s *Server) handleToggleFavourite(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	id := int64Param(r.PathValue("id"))

	assignment, err := s.svc.Assignment(r.Context(), id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.SetFavourite(r.Context(), id, !assignment.Favourite); err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// ------------------------------------------------------------ moving time ---

// handleMoveForm shows where a selection of entries could go.
func (s *Server) handleMoveForm(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "entries")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("move.title")

	if data.Assignments, err = s.svc.Assignments(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	data.MoveIDs = r.URL.Query()["entry"]
	s.render(w, r, "page_move.html", data)
}

// handleMoveEntries performs the move.
func (s *Server) handleMoveEntries(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	var ids []int64
	for _, raw := range r.Form["entry"] {
		if id := int64Param(raw); id != 0 {
			ids = append(ids, id)
		}
	}

	result, err := s.svc.MoveEntries(r.Context(), ids, int64Param(r.FormValue("assignment_id")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if result.Skipped > 0 {
		s.log.InfoContext(r.Context(), "some entries were not moved",
			"moved", result.Moved, "skipped", result.Skipped)
	}
	http.Redirect(w, r, "/entries", http.StatusSeeOther)
}

// --------------------------------------------------------------- settings ---

// handleSettings renders the instance settings form.
func (s *Server) handleSettings(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "admin")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("settings.title")
	s.render(w, r, "page_settings.html", data)
}

// handleUpdateSettings saves the instance settings.
func (s *Server) handleUpdateSettings(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	weekStart, _ := strconv.Atoi(r.FormValue("week_start"))
	maxTimer, _ := strconv.ParseInt(r.FormValue("max_timer_hours"), 10, 64)

	err := s.svc.UpdateSettings(r.Context(), service.SettingsInput{
		DefaultCurrency: strings.ToUpper(r.FormValue("default_currency")),
		DefaultRounding: r.FormValue("default_rounding"),
		DefaultRate:     r.FormValue("default_rate"),
		WeekStart:       weekStart,
		MaxTimerHours:   maxTimer,
		ShowClock:       r.FormValue("show_clock") != "",
		ShowTimeAndDate: r.FormValue("show_time_and_date") != "",
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// ----------------------------------------------------------- CSV import -----

// handleImportForm renders the CSV import screen.
func (s *Server) handleImportForm(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "entries")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("import.title")
	s.render(w, r, "page_import.html", data)
}

// handleImportPreview parses an uploaded CSV and shows what it would do.
//
// Two passes, because an import that partly succeeds is worse than one that
// fails: the user sees every row and every problem before anything is written.
func (s *Server) handleImportPreview(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxFormMemory); err != nil {
		http.Error(w, "The uploaded file is too large.", http.StatusRequestEntityTooLarge)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "No file was uploaded.", http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }()

	createMissing := r.FormValue("create_missing") != ""

	data, err := s.newPageData(r, "", "entries")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("import.title")

	preview, err := s.svc.ParseTimeCSV(r.Context(), file, createMissing)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.ImportPreview = &preview
	data.ImportCreateMissing = createMissing

	s.render(w, r, "page_import.html", data)
}

// handleImportCommit performs the import.
func (s *Server) handleImportCommit(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, maxUploadBytes)
	if err := r.ParseMultipartForm(maxFormMemory); err != nil {
		http.Error(w, "The uploaded file is too large.", http.StatusRequestEntityTooLarge)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "No file was uploaded.", http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }()

	imported, err := s.svc.ImportTimeCSV(r.Context(), file, r.FormValue("create_missing") != "")
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.log.InfoContext(r.Context(), "imported time entries from CSV", "rows", imported)
	http.Redirect(w, r, "/entries", http.StatusSeeOther)
}

// ------------------------------------------------------ backup and restore --

// handleBackup renders the backup screen.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request) {
	data, err := s.newPageData(r, "", "backup")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("backup.title")

	if data.Customers, err = s.svc.Customers(r.Context(), false); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Projects, err = s.svc.Projects(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Backups, err = s.svc.ListBackupFiles(s.cfg.BackupDir); err != nil {
		s.log.WarnContext(r.Context(), "could not list backups", "error", err.Error())
	}
	data.BackupEnabled = s.cfg.BackupEnabled
	data.BackupInterval = s.cfg.BackupInterval
	data.BackupKeep = s.cfg.BackupKeep

	s.render(w, r, "page_backup.html", data)
}

// handleDownloadBackup streams a backup to the browser.
func (s *Server) handleDownloadBackup(w http.ResponseWriter, r *http.Request) {
	opts := s.backupOptions(r)

	filename := "timetracker-backup-" + s.svc.Now().Format("20060102-150405") + ".json"
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", contentDisposition(filename))

	if err := s.svc.WriteBackup(r.Context(), w, opts); err != nil {
		// The status line is already sent, so the download will be truncated.
		// Logging is all that remains - and a truncated backup is exactly why
		// the on-disk path writes to a temporary file and renames.
		s.log.ErrorContext(r.Context(), "backup download failed midway", "error", err.Error())
	}
}

// handleCreateBackupFile writes a backup into the backup directory.
func (s *Server) handleCreateBackupFile(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	file, err := s.svc.WriteBackupFile(r.Context(), s.cfg.BackupDir, s.backupOptions(r))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.log.InfoContext(r.Context(), "backup created", "file", file.Name, "bytes", file.SizeBytes)
	s.refreshOrRedirect(w, r)
}

// handleRestore merges an uploaded backup.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request) {
	// A backup can be large, so the limit here is generous - but still a limit.
	r.Body = http.MaxBytesReader(w, r.Body, 256<<20)
	if err := r.ParseMultipartForm(maxFormMemory); err != nil {
		http.Error(w, "The uploaded file is too large.", http.StatusRequestEntityTooLarge)
		return
	}

	file, _, err := r.FormFile("file")
	if err != nil {
		http.Error(w, "No file was uploaded.", http.StatusBadRequest)
		return
	}
	defer func() { _ = file.Close() }()

	result, err := s.svc.Restore(r.Context(), file)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	s.log.InfoContext(r.Context(), "backup restored",
		"created", result.Total(), "skipped", result.Skipped,
		"problems", len(result.Problems))

	data, dataErr := s.newPageData(r, "", "backup")
	if dataErr != nil {
		s.fail(w, r, dataErr)
		return
	}
	data.Title = data.Printer.T("backup.title")
	data.RestoreResult = &result
	if data.Customers, err = s.svc.Customers(r.Context(), false); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Projects, err = s.svc.Projects(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	data.Backups, _ = s.svc.ListBackupFiles(s.cfg.BackupDir)
	data.BackupEnabled = s.cfg.BackupEnabled
	data.BackupInterval = s.cfg.BackupInterval
	data.BackupKeep = s.cfg.BackupKeep

	s.render(w, r, "page_backup.html", data)
}

// backupOptions reads the scope from a form or query string.
func (s *Server) backupOptions(r *http.Request) service.BackupOptions {
	opts := service.BackupOptions{
		CustomerID: int64Param(r.FormValue("customer_id")),
		ProjectID:  int64Param(r.FormValue("project_id")),
	}
	if from, err := time.Parse("2006-01-02", r.FormValue("from")); err == nil {
		opts.From = from
	}
	if to, err := time.Parse("2006-01-02", r.FormValue("to")); err == nil {
		// Inclusive of the day the user typed.
		opts.To = to.AddDate(0, 0, 1)
	}
	return opts
}

// ------------------------------------------------------- contract terms ----

// Contract terms are dated, and attach to a customer or to a project.
//
// One screen serves both scopes and shows the whole history: what applies now,
// what applied before, and what has been agreed for a date still to come. A
// screen that showed only the current terms would make a renegotiation look
// like an edit, and there would be no way to answer "what were we charging in
// March" except from a backup.

// termsScopeFromPath reads the scope and its record from the URL.
func (s *Server) termsScopeFromPath(r *http.Request) (domain.TermsScope, int64, error) {
	scope := domain.TermsScope(r.PathValue("scope"))
	if !scope.Valid() {
		return "", 0, domainValidation("unknown contract scope: " + r.PathValue("scope"))
	}
	return scope, int64Param(r.PathValue("id")), nil
}

// handleContractTerms lists one customer's or project's dated terms.
func (s *Server) handleContractTerms(w http.ResponseWriter, r *http.Request) {
	scope, id, err := s.termsScopeFromPath(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	data, err := s.newPageData(r, "", "admin")
	if err != nil {
		s.fail(w, r, err)
		return
	}

	view, err := s.svc.ContractTerms(r.Context(), scope, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Terms = &view
	data.Title = data.Printer.T("terms.title", view.ScopeName)

	// The set being edited, when one was asked for. A form with values in it is
	// an edit; an empty one adds a revision.
	if editID := int64Param(r.URL.Query().Get("edit")); editID != 0 {
		for i := range view.Terms {
			if view.Terms[i].ID == editID {
				data.EditTerms = &view.Terms[i]
				break
			}
		}
	}
	s.render(w, r, "page_contract_terms.html", data)
}

// handleSaveContractTerms adds or updates one dated set.
func (s *Server) handleSaveContractTerms(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	scope, id, err := s.termsScopeFromPath(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	// The currency the amounts are typed in is the customer's, so "2.50" means
	// 2.50 of what this account is invoiced in.
	currency, err := s.svc.TermsCurrency(r.Context(), scope, id)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	rules, err := rateRulesFromForm(r, currency)
	if err != nil {
		s.fail(w, r, err)
		return
	}

	terms := domain.ContractTerms{
		ID:            int64Param(r.FormValue("terms_id")),
		Scope:         scope,
		ScopeID:       id,
		EffectiveFrom: strings.TrimSpace(r.FormValue("effective_from")),
		Rules:         rules,
		Note:          r.FormValue("note"),
	}
	if err := s.svc.SaveContractTerms(r.Context(), terms); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, termsPath(scope, id), http.StatusSeeOther)
}

// handleDeleteContractTerms removes one dated set.
func (s *Server) handleDeleteContractTerms(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	scope, id, err := s.termsScopeFromPath(r)
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.DeleteContractTerms(r.Context(), int64Param(r.FormValue("terms_id"))); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, termsPath(scope, id), http.StatusSeeOther)
}

// termsPath is the URL of one scope's terms screen.
func termsPath(scope domain.TermsScope, id int64) string {
	return "/terms/" + string(scope) + "/" + strconv.FormatInt(id, 10)
}
