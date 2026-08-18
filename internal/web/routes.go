package web

// routes registers every URL the application serves.
//
// Kept in one function on purpose: the route table is the map of the
// application's surface, and a reader should be able to see all of it at once.
// Go's ServeMux patterns carry the method, so an endpoint that only accepts POST
// says so here rather than inside the handler.
func (s *Server) routes() {
	mux := s.mux

	// Static assets.
	mux.Handle("GET /static/", s.staticHandler())

	// Health, for a reverse proxy or a monitoring check.
	mux.HandleFunc("GET /healthz", s.handleHealth)

	// Authentication. Registered in both modes; every handler returns 404 when
	// there is no account service, so a local instance exposes nothing.
	mux.HandleFunc("GET /login", s.handleLoginForm)
	mux.HandleFunc("POST /login", s.handleLogin)
	mux.HandleFunc("POST /logout", s.handleLogout)
	mux.HandleFunc("GET /auth/oidc/start", s.handleOIDCStart)
	mux.HandleFunc("GET /auth/oidc/callback", s.handleOIDCCallback)

	// Accounts and project membership - the server-mode administration screen.
	mux.HandleFunc("GET /users", s.handleUsers)
	mux.HandleFunc("POST /users", s.handleCreateUser)
	mux.HandleFunc("POST /users/{id}", s.handleUpdateUser)
	mux.HandleFunc("POST /users/{id}/password", s.handleSetPassword)
	mux.HandleFunc("POST /members", s.handleAddMember)
	mux.HandleFunc("POST /members/remove", s.handleRemoveMember)

	// Screens.
	mux.HandleFunc("GET /{$}", s.handleToday)   // {$} matches only "/"
	mux.HandleFunc("GET /today", s.handleToday) // with an optional ?date=
	mux.HandleFunc("GET /week", s.handleWeek)
	mux.HandleFunc("GET /entries", s.handleEntries)
	mux.HandleFunc("GET /admin", s.handleAdmin)

	// Timers. All state-changing, so all POST: a GET that starts a timer would
	// fire on a prefetch or a bookmark.
	mux.HandleFunc("POST /timers/start", s.handleStartTimer)
	mux.HandleFunc("POST /timers/{id}/stop", s.handleStopTimer)
	mux.HandleFunc("POST /timers/stop-all", s.handleStopAllTimers)

	// Time entries.
	mux.HandleFunc("POST /entries", s.handleCreateEntry)
	mux.HandleFunc("POST /entries/quick", s.handleQuickAdd)
	mux.HandleFunc("GET /entries/{id}/edit", s.handleEditEntryForm)
	mux.HandleFunc("POST /entries/{id}", s.handleUpdateEntry)
	mux.HandleFunc("POST /entries/{id}/delete", s.handleDeleteEntry)

	// Catalogue administration.
	mux.HandleFunc("POST /customers", s.handleCreateCustomer)
	mux.HandleFunc("POST /customers/{id}/archive", s.handleArchiveCustomer)
	mux.HandleFunc("POST /projects", s.handleCreateProject)
	mux.HandleFunc("POST /projects/{id}/archive", s.handleArchiveProject)
	mux.HandleFunc("POST /assignments", s.handleCreateAssignment)
	mux.HandleFunc("POST /assignments/{id}/archive", s.handleArchiveAssignment)

	// Preferences. The theme is stored per user rather than only in the browser,
	// so it follows a person between devices in server mode.
	mux.HandleFunc("POST /preferences/theme", s.handleSetTheme)
	mux.HandleFunc("POST /preferences/language", s.handleSetLanguage)

	// Context-sensitive help. A plain link, so it works without JavaScript; with
	// script it is swapped into a panel instead of navigating.
	mux.HandleFunc("GET /help/{screen}", s.handleHelp)

	// Expenses.
	mux.HandleFunc("GET /expenses", s.handleExpenses)
	mux.HandleFunc("POST /expenses", s.handleCreateExpense)
	mux.HandleFunc("POST /expenses/{id}", s.handleUpdateExpense)
	mux.HandleFunc("POST /expenses/{id}/delete", s.handleDeleteExpense)

	// Attachments. The upload route is the only one that accepts multipart,
	// because that is how a browser sends a file; everything else uses
	// URL-encoded bodies so both submission paths parse identically.
	mux.HandleFunc("POST /attachments/{owner}/{id}", s.handleUpload)
	mux.HandleFunc("GET /attachments/{id}", s.handleDownloadAttachment)
	mux.HandleFunc("POST /attachments/{id}/delete", s.handleDeleteAttachment)

	// The proxy inbox: time and costs awaiting the acting user's decision.
	mux.HandleFunc("GET /inbox", s.handleInbox)
	mux.HandleFunc("POST /inbox/entries/{id}/accept", s.handleAcceptEntry)
	mux.HandleFunc("POST /inbox/entries/{id}/reject", s.handleRejectEntry)
	mux.HandleFunc("POST /inbox/expenses/{id}/accept", s.handleAcceptExpense)
	mux.HandleFunc("POST /inbox/expenses/{id}/reject", s.handleRejectExpense)

	// Editing the catalogue.
	mux.HandleFunc("GET /customers/{id}/edit", s.handleEditCustomer)
	mux.HandleFunc("POST /customers/{id}", s.handleUpdateCustomer)
	// Contract terms beyond the base rate: overtime, travel, reimbursement.
	// Dated, and attached to a customer or a project.
	mux.HandleFunc("GET /terms/{scope}/{id}", s.handleContractTerms)
	mux.HandleFunc("POST /terms/{scope}/{id}", s.handleSaveContractTerms)
	mux.HandleFunc("POST /terms/{scope}/{id}/delete", s.handleDeleteContractTerms)
	mux.HandleFunc("GET /projects/{id}/edit", s.handleEditProject)
	mux.HandleFunc("POST /projects/{id}", s.handleUpdateProject)
	mux.HandleFunc("GET /assignments/{id}/edit", s.handleEditAssignment)
	mux.HandleFunc("POST /assignments/{id}", s.handleUpdateAssignment)
	mux.HandleFunc("POST /assignments/{id}/favourite", s.handleToggleFavourite)

	// Weekly submit and approve.
	mux.HandleFunc("POST /week/submit", s.handleSubmitWeek)
	mux.HandleFunc("POST /week/withdraw", s.handleWithdrawWeek)
	// Tags, and the search index they feed.
	mux.HandleFunc("GET /tags", s.handleTags)
	mux.HandleFunc("POST /tags/{id}", s.handleUpdateTag)
	mux.HandleFunc("POST /tags/{id}/delete", s.handleDeleteTag)
	mux.HandleFunc("POST /search/reindex", s.handleReindexSearch)

	// The user guide: how to do things, as opposed to what a screen is.
	mux.HandleFunc("GET /guide", s.handleGuide)
	mux.HandleFunc("GET /guide/{topic}", s.handleGuide)
	mux.HandleFunc("GET /approvals", s.handleApprovals)
	mux.HandleFunc("GET /approvals/report", s.handleApprovalReport)
	mux.HandleFunc("POST /approvals/approve", s.handleApproveWeek)
	mux.HandleFunc("POST /approvals/reject", s.handleRejectWeek)
	mux.HandleFunc("POST /approvals/reopen", s.handleReopenWeek)

	// Moving time that was recorded against the wrong assignment.
	mux.HandleFunc("GET /move", s.handleMoveForm)
	mux.HandleFunc("POST /move", s.handleMoveEntries)

	// Instance settings.
	mux.HandleFunc("GET /settings", s.handleSettings)
	mux.HandleFunc("POST /settings", s.handleUpdateSettings)

	// Bulk import of hours from CSV.
	mux.HandleFunc("GET /import", s.handleImportForm)
	mux.HandleFunc("POST /import/preview", s.handleImportPreview)
	mux.HandleFunc("POST /import", s.handleImportCommit)

	// Backup and restore.
	mux.HandleFunc("GET /backup", s.handleBackup)
	mux.HandleFunc("GET /backup/download", s.handleDownloadBackup)
	mux.HandleFunc("POST /backup/create", s.handleCreateBackupFile)
	mux.HandleFunc("POST /backup/restore", s.handleRestore)

	// Exports.
	mux.HandleFunc("GET /export/{format}", s.handleExport)
}
