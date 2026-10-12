package web

import (
	"errors"
	"log/slog"
	"net/http"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/service"
)

// fail maps an error from the service layer onto an HTTP response.
//
// The mapping is deliberately narrow. A forbidden action on a resource the actor
// may not know exists has already been converted to "not found" by the service
// layer, so that probing identifiers cannot be used to discover records; here we
// only need to distinguish the user's mistake from ours.
func (s *Server) fail(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, errBadForm):
		http.Error(w, "The submitted form could not be read.", http.StatusBadRequest)
	case errors.Is(err, service.ErrValidation), errors.Is(err, service.ErrInvalidQuery):
		// The user's input was wrong, and the message describes what to fix.
		// A malformed regular expression belongs here rather than in the
		// internal bucket: it is the searcher's typo, and only they can fix it.
		http.Error(w, err.Error(), http.StatusBadRequest)
	case errors.Is(err, service.ErrNotFound):
		http.Error(w, "Not found.", http.StatusNotFound)
	case errors.Is(err, service.ErrForbidden):
		http.Error(w, "Not permitted.", http.StatusForbidden)
	case errors.Is(err, service.ErrUnauthorized):
		http.Error(w, "Not authenticated.", http.StatusUnauthorized)
	case errors.Is(err, service.ErrConflict):
		http.Error(w, err.Error(), http.StatusConflict)
	default:
		s.serverError(w, r, err)
	}
}

// serverError logs the detail and tells the user nothing about it.
//
// Internal errors name file paths, SQL and package structure. The user gets an
// apology and a request id they can quote; the operator gets the rest in the log.
func (s *Server) serverError(w http.ResponseWriter, r *http.Request, err error) {
	s.log.ErrorContext(r.Context(), "request failed",
		slog.String("error", err.Error()),
		slog.String("path", r.URL.Path))
	http.Error(w, "Something went wrong. The details are in the server log.",
		http.StatusInternalServerError)
}

// refuseClient stops an administrative screen short for a client user.
//
// The data behind these screens is narrowed for a client like everything else,
// so this is not what keeps the rates off their screen - the projection is. What
// it stops is a screen full of controls the role cannot use: the catalogue
// offers forms to create and archive customers, and offering somebody a button
// whose only possible outcome is a refusal is a worse answer than saying no.
//
// It returns true when the request has been answered and the handler should
// stop.
func (s *Server) refuseClient(w http.ResponseWriter, r *http.Request) bool {
	user, ok := auth.UserFrom(r.Context())
	if !ok || user.Role != domain.RoleClient {
		return false
	}
	http.Error(w, "Not permitted.", http.StatusForbidden)
	return true
}
