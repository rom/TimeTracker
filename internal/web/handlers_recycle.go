package web

import (
	"net/http"

	"github.com/rom/timetracker/internal/i18n"
)

// The recycle bin's screens.
//
// One listing and one action. The bin is deliberately not a management screen:
// there is nothing to configure, nothing to purge by hand, and no second state
// a record can be in. A thing is either waiting to be put back or it is not.

// handleDeleted renders what the acting user may put back.
func (s *Server) handleDeleted(w http.ResponseWriter, r *http.Request) {
	// A client deletes nothing - the role is read-only by definition - so this
	// screen could only ever be empty for them, and an empty screen is a worse
	// answer than their own listing.
	if s.sentToTheirOwnListing(w, r) {
		return
	}

	data, err := s.newPageData(r, "", "deleted")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	data.Title = data.Printer.T("deleted.title")

	if data.Deleted, err = s.svc.RecentlyDeleted(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_deleted.html", data)
}

// handleRestoreDeleted puts one record back.
func (s *Server) handleRestoreDeleted(w http.ResponseWriter, r *http.Request) {
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	if err := s.svc.RestoreDeleted(r.Context(), int64Param(r.PathValue("id"))); err != nil {
		s.fail(w, r, err)
		return
	}
	http.Redirect(w, r, "/deleted", http.StatusSeeOther)
}

// Confirmation is what the second-ask page needs to render itself.
type Confirmation struct {
	// What is being deleted, in words the person recognises.
	What string
	// Action is posted to with confirmed=1; Cancel is where "no" goes.
	Action string
	Cancel string
}

// needsConfirming renders the second ask and reports that it did.
//
// Called by every destructive handler before it acts. The script sends
// confirmed=1 once the person has answered its dialog, so this page is never
// seen with JavaScript on and is the entire confirmation without it - which
// before this was nothing at all, because hx-confirm disappears with the
// script that implements it.
//
// It is not a security control. The CSRF token is what stops a forged
// deletion; this stops a misplaced click.
func (s *Server) needsConfirming(w http.ResponseWriter, r *http.Request,
	describe func(*i18n.Printer) string, cancel string) bool {

	if r.FormValue("confirmed") == "1" {
		return false
	}

	data, err := s.newPageData(r, "", "")
	if err != nil {
		s.fail(w, r, err)
		return true
	}

	// Described with the page's own printer, so the record is named in the
	// reader's language and their date format rather than the server's.
	what := describe(data.Printer)

	// Nothing to describe means the caller could not read the record: it does
	// not exist, or it is not this person's to delete. Asking "are you sure?"
	// about a record that is about to be refused would answer a question the
	// person has no business asking, and would turn a 404 into a 200. The
	// service is what decides; this only asks about something it has already
	// agreed exists.
	if what == "" {
		return false
	}

	data.Title = data.Printer.T("confirm.title")
	data.Confirm = Confirmation{What: what, Action: r.URL.Path, Cancel: cancel}
	s.render(w, r, "page_confirm.html", data)
	return true
}
