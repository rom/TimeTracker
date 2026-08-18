package web

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/domain"
	"github.com/rom/timetracker/internal/i18n"
	"github.com/rom/timetracker/internal/service"
)

// Authentication endpoints. Present only in server mode; the routes are
// registered but every one of them short-circuits when there is no account
// service, so a local instance cannot be talked into a login flow.

// tr returns a printer for a request that may have no identity yet, which is the
// case on every authentication route.
func (s *Server) tr(r *http.Request) *i18n.Printer {
	if user, ok := auth.UserFrom(r.Context()); ok {
		return printerFor(r, user)
	}
	return i18n.NewPrinter(i18n.Negotiate(r.Header.Get("Accept-Language")))
}

// sessionKey carries the resolved session, so the CSRF middleware and the
// sign-out handler can reach it.
type sessionKey struct{}

func withSession(r *http.Request, s auth.Session) *http.Request {
	return r.WithContext(context.WithValue(r.Context(), sessionKey{}, s))
}

func sessionFrom(r *http.Request) (auth.Session, bool) {
	s, ok := r.Context().Value(sessionKey{}).(auth.Session)
	return s, ok
}

// oidcStateCookie holds the per-login state across the redirect to the provider.
//
// A cookie rather than server-side storage: the state is short-lived, tied to
// exactly one browser, and useless to anyone else. It is HttpOnly and SameSite
// so no script and no other site can read it back.
const oidcStateCookie = "tt_oidc"

// handleLoginForm renders the sign-in page.
func (s *Server) handleLoginForm(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		http.Redirect(w, r, "/today", http.StatusSeeOther)
		return
	}
	// Already signed in: send them where they were going.
	if _, err := s.resolveSession(r); err == nil {
		http.Redirect(w, r, "/today", http.StatusSeeOther)
		return
	}

	s.renderLogin(w, r, "", http.StatusOK)
}

// renderLogin draws the login page, optionally with an error.
func (s *Server) renderLogin(w http.ResponseWriter, r *http.Request, message string, status int) {
	// No identity yet, so the language comes entirely from the browser.
	printer := i18n.NewPrinter(i18n.Negotiate(r.Header.Get("Accept-Language")))

	data := pageData{
		Title:     printer.T("login.title"),
		Active:    "login",
		Now:       s.svc.Now(),
		Themes:    availableThemes,
		Printer:   printer,
		Lang:      printer.Code(),
		Languages: languageOptions(),
		Error:     message,
		Login: &loginData{
			OIDCEnabled:  s.oidc != nil,
			OIDCLabel:    s.cfg.OIDCLabel,
			PasswordOnly: s.oidc == nil,
			// Preserved across a failed attempt so the user is not bounced back
			// to Today after fixing their password.
			Next: safeNext(r.URL.Query().Get("next")),
		},
	}

	set, ok := s.templates["page_login.html"]
	if !ok {
		s.serverError(w, r, errors.New("login template missing"))
		return
	}
	var buf strings.Builder
	if err := set.ExecuteTemplate(&buf, "layout.html", data); err != nil {
		s.serverError(w, r, err)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	_, _ = w.Write([]byte(buf.String()))
}

// loginData is the login page's own payload.
type loginData struct {
	OIDCEnabled  bool
	OIDCLabel    string
	PasswordOnly bool
	Next         string
}

// handleLogin authenticates an email and password.
func (s *Server) handleLogin(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		http.NotFound(w, r)
		return
	}
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	result, err := s.accounts.Login(r.Context(), service.LoginRequest{
		Email:     r.FormValue("email"),
		Password:  r.FormValue("password"),
		IP:        s.clientIP(r),
		UserAgent: r.UserAgent(),
	})
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			// One message for every failure - wrong password, unknown account,
			// disabled account, rate limited. Distinguishing them would turn the
			// login form into an account enumeration tool.
			s.renderLogin(w, r, s.tr(r).T("login.failed"), http.StatusUnauthorized)
			return
		}
		s.serverError(w, r, err)
		return
	}

	s.setSessionCookie(w, r, result)
	http.Redirect(w, r, safeNext(r.FormValue("next")), http.StatusSeeOther)
}

// handleLogout revokes the session and clears the cookie.
func (s *Server) handleLogout(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		http.Redirect(w, r, "/today", http.StatusSeeOther)
		return
	}

	if cookie, err := r.Cookie(auth.SessionCookieName); err == nil {
		// Revoked server-side, not merely forgotten by the browser: a cookie the
		// user has stopped sending is still a valid credential if it is still in
		// the sessions table.
		if err := s.accounts.Logout(r.Context(), cookie.Value); err != nil {
			s.serverError(w, r, err)
			return
		}
	}

	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleOIDCStart redirects the browser to the identity provider.
func (s *Server) handleOIDCStart(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil {
		http.NotFound(w, r)
		return
	}

	request, err := auth.NewAuthRequest()
	if err != nil {
		s.serverError(w, r, err)
		return
	}

	// The state, nonce and PKCE verifier must survive the round trip. They are
	// held in a short-lived HttpOnly cookie, never in the URL, so neither the
	// provider's logs nor a referrer header carry them.
	http.SetCookie(w, &http.Cookie{
		Name:     oidcStateCookie,
		Value:    request.State + "." + request.Nonce + "." + request.Verifier,
		Path:     "/",
		HttpOnly: true,
		Secure:   s.cookieSecure(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int((10 * time.Minute).Seconds()),
	})

	http.Redirect(w, r, s.oidc.AuthorizationURL(request), http.StatusSeeOther)
}

// handleOIDCCallback completes the flow.
func (s *Server) handleOIDCCallback(w http.ResponseWriter, r *http.Request) {
	if s.oidc == nil || s.accounts == nil {
		http.NotFound(w, r)
		return
	}

	// The provider reports its own failures here; showing its raw text back to
	// the user would render attacker-influenced content, so only the fact of a
	// failure is surfaced.
	if r.URL.Query().Get("error") != "" {
		s.log.WarnContext(r.Context(), "OIDC provider returned an error",
			"error", r.URL.Query().Get("error"))
		s.renderLogin(w, r, s.tr(r).T("login.denied"), http.StatusUnauthorized)
		return
	}

	cookie, err := r.Cookie(oidcStateCookie)
	if err != nil {
		s.renderLogin(w, r, s.tr(r).T("login.expired"), http.StatusBadRequest)
		return
	}
	// The cookie is single-use: clearing it here stops a replayed callback from
	// being processed twice.
	s.clearCookie(w, oidcStateCookie)

	parts := strings.SplitN(cookie.Value, ".", 3)
	if len(parts) != 3 {
		s.renderLogin(w, r, s.tr(r).T("login.unverified"), http.StatusBadRequest)
		return
	}
	request := auth.AuthRequest{State: parts[0], Nonce: parts[1], Verifier: parts[2]}

	// The state comparison is what ties this callback to a login this browser
	// actually started.
	if r.URL.Query().Get("state") != request.State {
		s.log.WarnContext(r.Context(), "OIDC state mismatch on callback")
		s.renderLogin(w, r, s.tr(r).T("login.unverified"), http.StatusBadRequest)
		return
	}

	code := r.URL.Query().Get("code")
	if code == "" {
		s.renderLogin(w, r, s.tr(r).T("login.ssofailed"), http.StatusBadRequest)
		return
	}

	claims, err := s.oidc.Exchange(r.Context(), code, request)
	if err != nil {
		// The detail goes to the log: it can name the provider's endpoints and
		// the reason a token failed validation, neither of which belongs on a
		// public error page.
		s.log.ErrorContext(r.Context(), "OIDC exchange failed", "error", err.Error())
		s.renderLogin(w, r, s.tr(r).T("login.ssofailed"), http.StatusUnauthorized)
		return
	}

	result, err := s.accounts.LoginWithOIDC(r.Context(), claims,
		s.oidc.MappedRole(claims), s.clientIP(r), r.UserAgent())
	if err != nil {
		if errors.Is(err, auth.ErrInvalidCredentials) {
			s.renderLogin(w, r, s.tr(r).T("login.denied"), http.StatusForbidden)
			return
		}
		s.serverError(w, r, err)
		return
	}

	s.setSessionCookie(w, r, result)
	http.Redirect(w, r, "/today", http.StatusSeeOther)
}

// setSessionCookie writes the session cookie for a fresh login.
func (s *Server) setSessionCookie(w http.ResponseWriter, r *http.Request, result service.LoginResult) {
	http.SetCookie(w, &http.Cookie{
		Name:  auth.SessionCookieName,
		Value: result.CookieValue,
		Path:  "/",
		// HttpOnly: no script may read it, so an injected script cannot steal
		// the session even if one ever got past the CSP.
		HttpOnly: true,
		// Secure whenever the connection is TLS-terminated, so the cookie never
		// travels in clear.
		Secure: s.cookieSecure(r),
		// Lax: sent on top-level navigation to this site, not on a cross-site
		// form post - a second layer under the CSRF token.
		SameSite: http.SameSiteLaxMode,
		// No Domain attribute, making the cookie host-only: it is never sent to
		// a sibling subdomain that might be operated by somebody else.
		Expires: result.ExpiresAt,
	})
}

func (s *Server) clearSessionCookie(w http.ResponseWriter) {
	s.clearCookie(w, auth.SessionCookieName)
}

func (s *Server) clearCookie(w http.ResponseWriter, name string) {
	http.SetCookie(w, &http.Cookie{
		Name: name, Value: "", Path: "/",
		HttpOnly: true, MaxAge: -1, Expires: time.Unix(0, 0),
	})
}

// cookieSecure reports whether to set the Secure attribute.
//
// True whenever the request arrived over TLS, directly or through a trusted
// proxy that says so. On a plain-HTTP loopback development run it is false,
// because a Secure cookie would simply never be sent back and nothing would work.
func (s *Server) cookieSecure(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	if s.isTrustedProxy(hostOnly(r.RemoteAddr)) &&
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https") {
		return true
	}
	return s.cfg.ForceSecureCookies
}

// safeNext sanitises a post-login redirect target.
//
// Only a same-site absolute path is accepted. Without this the login form is an
// open redirect: an attacker sends a victim to /login?next=https://evil.example,
// the victim signs in legitimately, and lands on a convincing fake.
func safeNext(next string) string {
	if next == "" {
		return "/today"
	}
	// Must be a path on this host: no scheme, no host, and not a protocol-
	// relative "//evil.example" which a browser reads as an absolute URL.
	if !strings.HasPrefix(next, "/") || strings.HasPrefix(next, "//") {
		return "/today"
	}
	parsed, err := url.Parse(next)
	if err != nil || parsed.Host != "" || parsed.Scheme != "" {
		return "/today"
	}
	return parsed.RequestURI()
}

func hostOnly(addr string) string {
	if i := strings.LastIndex(addr, ":"); i > 0 {
		return addr[:i]
	}
	return addr
}

// ------------------------------------------------------- account management --

// handleUsers renders the account administration screen.
func (s *Server) handleUsers(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		http.NotFound(w, r)
		return
	}

	data, err := s.newPageData(r, "Users", "users")
	if err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Users, err = s.accounts.Users(r.Context()); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Customers, err = s.svc.Customers(r.Context(), false); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Projects, err = s.svc.Projects(r.Context(), 0, false); err != nil {
		s.fail(w, r, err)
		return
	}
	if data.Members, err = s.accounts.Members(r.Context(), 0); err != nil {
		s.fail(w, r, err)
		return
	}
	s.render(w, r, "page_users.html", data)
}

// handleCreateUser adds an account.
func (s *Server) handleCreateUser(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		http.NotFound(w, r)
		return
	}
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	_, err := s.accounts.CreateUser(r.Context(), service.NewUserInput{
		DisplayName:      r.FormValue("display_name"),
		Email:            r.FormValue("email"),
		Password:         r.FormValue("password"),
		Role:             domain.Role(r.FormValue("role")),
		TimeZone:         r.FormValue("time_zone"),
		ClientCustomerID: int64Param(r.FormValue("client_customer_id")),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleUpdateUser changes a user's role or active state.
func (s *Server) handleUpdateUser(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		http.NotFound(w, r)
		return
	}
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	err := s.accounts.UpdateUser(r.Context(), domain.User{
		ID:               int64Param(r.PathValue("id")),
		DisplayName:      r.FormValue("display_name"),
		Email:            r.FormValue("email"),
		Role:             domain.Role(r.FormValue("role")),
		Active:           r.FormValue("active") != "",
		ClientCustomerID: int64Param(r.FormValue("client_customer_id")),
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleSetPassword changes a password - the acting user's own, or anyone's if
// they are an administrator.
func (s *Server) handleSetPassword(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		http.NotFound(w, r)
		return
	}
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	err := s.accounts.SetPassword(r.Context(),
		int64Param(r.PathValue("id")),
		r.FormValue("current_password"),
		r.FormValue("new_password"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	// Changing a password revokes every session for that account, including this
	// one when it is your own. Sending the user to the login page is honest
	// about what just happened.
	s.clearSessionCookie(w)
	http.Redirect(w, r, "/login", http.StatusSeeOther)
}

// handleAddMember attaches a user to a project.
func (s *Server) handleAddMember(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		http.NotFound(w, r)
		return
	}
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}

	rate, err := parseRate(r.FormValue("rate"), r.FormValue("currency"))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	err = s.accounts.AddMember(r.Context(), service.Membership{
		ProjectID: int64Param(r.FormValue("project_id")),
		UserID:    int64Param(r.FormValue("user_id")),
		RateMinor: rate,
	})
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// handleRemoveMember detaches a user from a project.
func (s *Server) handleRemoveMember(w http.ResponseWriter, r *http.Request) {
	if s.accounts == nil {
		http.NotFound(w, r)
		return
	}
	if err := parseForm(r); err != nil {
		s.fail(w, r, err)
		return
	}
	err := s.accounts.RemoveMember(r.Context(),
		int64Param(r.FormValue("project_id")), int64Param(r.FormValue("user_id")))
	if err != nil {
		s.fail(w, r, err)
		return
	}
	s.refreshOrRedirect(w, r)
}

// resolveSession is the shared lookup used by the identity middleware and by the
// login page's "already signed in" check.
func (s *Server) resolveSession(r *http.Request) (domain.User, error) {
	cookie, err := r.Cookie(auth.SessionCookieName)
	if err != nil {
		return domain.User{}, auth.ErrUnauthenticated
	}
	user, _, err := s.accounts.ResolveSession(r.Context(), cookie.Value)
	return user, err
}
