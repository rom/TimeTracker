package web

import (
	"errors"
	"log/slog"
	"net/http"

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
	case errors.Is(err, service.ErrValidation):
		// The user's input was wrong, and the message describes what to fix.
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
