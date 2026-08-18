package web

import (
	"context"
	"crypto/subtle"
	"net/http"

	"github.com/rom/timetracker/internal/auth"
)

// CSRF protection.
//
// A token bound to the session must accompany every state-changing request. The
// attack it prevents is a page on another site causing the user's browser to
// submit a form here using the cookies it already holds; the cookie travels
// automatically, but a token the attacker cannot read does not.
//
// SameSite=Lax on the session cookie is a second, independent layer. Neither is
// relied on alone: SameSite is enforced by the browser and varies with version
// and configuration, while the token check is ours.

// csrfKey is unexported, so no other package can plant a token in the context.
type csrfKey struct{}

// withCSRFToken attaches the session's token to a request, so templates can
// render it into every form.
func withCSRFToken(r *http.Request, token string) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), csrfKey{}, token))
}

// csrfTokenFrom reads the token attached to a request.
func csrfTokenFrom(r *http.Request) string {
	token, _ := r.Context().Value(csrfKey{}).(string)
	return token
}

// requireCSRF rejects an unsafe request without a valid token.
//
// Safe methods are exempt because they must not change state in the first place;
// if a GET handler ever mutates something, that is the bug to fix, not a reason
// to widen this check.
func (s *Server) requireCSRF(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodGet, http.MethodHead, http.MethodOptions:
			next.ServeHTTP(w, r)
			return
		}

		// Local mode has no session and no cookie, so there is no cross-site
		// request to forge: the port is bound to loopback and there is no
		// authenticated identity for another origin to borrow. The check is
		// skipped rather than made to pass with a fake token, which would make
		// the server-mode path untested in local runs.
		session, ok := sessionFrom(r)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}

		presented := r.Header.Get(auth.CSRFHeaderName)
		if presented == "" {
			// Parsing is idempotent, and handlers parse again for their own
			// fields. It must go through parseForm so a multipart submission
			// yields its token rather than an empty string.
			_ = parseForm(r)
			presented = r.PostFormValue(auth.CSRFFieldName)
		}

		// Constant-time comparison: an early-returning comparison leaks, through
		// timing, how much of the token was guessed.
		if presented == "" || subtle.ConstantTimeCompare([]byte(presented), []byte(session.CSRFToken)) != 1 {
			s.log.WarnContext(r.Context(), "CSRF token rejected",
				"path", r.URL.Path, "method", r.Method)
			http.Error(w, "This request could not be verified. Reload the page and try again.",
				http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}
