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

	// Exports.
	mux.HandleFunc("GET /export/{format}", s.handleExport)
}
