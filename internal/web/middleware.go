package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"runtime/debug"
	"strings"
	"time"

	"github.com/rom/timetracker/internal/auth"
	"github.com/rom/timetracker/internal/service"
)

// withRecovery turns a panic into a 500 and a logged stack trace.
//
// Outermost in the chain, so a panic anywhere still produces a response. A web
// application that dies because one handler dereferenced a nil pointer loses
// every other user's in-flight request too.
func (s *Server) withRecovery(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		defer func() {
			if rec := recover(); rec != nil {
				s.log.Error("panic recovered",
					slog.Any("panic", rec),
					slog.String("path", r.URL.Path),
					slog.String("stack", string(debug.Stack())))
				// The stack goes to the log, never to the user: it names internal
				// paths and package structure.
				http.Error(w, "Something went wrong.", http.StatusInternalServerError)
			}
		}()
		next.ServeHTTP(w, r)
	})
}

// withRequestID assigns an identifier used to correlate the log lines and audit
// rows produced by one request.
func (s *Server) withRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := newRequestID()
		ctx := service.WithRequestMeta(r.Context(), service.RequestMeta{
			RequestID: id,
			IP:        s.clientIP(r),
		})
		w.Header().Set("X-Request-Id", id)
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// newRequestID returns a short random identifier. Randomness rather than a
// counter means ids stay unique across restarts, so a log search over a week
// cannot collide.
func newRequestID() string {
	var buf [8]byte
	if _, err := rand.Read(buf[:]); err != nil {
		// A failing CSPRNG is fatal for sessions but not for a log correlation
		// id, so fall back to a timestamp rather than failing the request.
		return time.Now().UTC().Format("20060102150405.000000")
	}
	return hex.EncodeToString(buf[:])
}

// withLogging records one line per request.
func (s *Server) withLogging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		recorder := &statusRecorder{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(recorder, r)

		// Static assets are logged at debug: they are numerous and uninteresting,
		// and letting them dominate the log makes the interesting lines hard to
		// find.
		level := slog.LevelInfo
		if isStaticPath(r.URL.Path) {
			level = slog.LevelDebug
		}
		s.log.Log(r.Context(), level, "request",
			slog.String("method", r.Method),
			slog.String("path", r.URL.Path),
			slog.Int("status", recorder.status),
			slog.Duration("duration", time.Since(start)))
	})
}

// statusRecorder captures the status code for the log line.
type statusRecorder struct {
	http.ResponseWriter
	status int
}

func (rec *statusRecorder) WriteHeader(status int) {
	rec.status = status
	rec.ResponseWriter.WriteHeader(status)
}

func isStaticPath(path string) bool {
	return len(path) >= 8 && path[:8] == "/static/"
}

// isPublicPath names the only routes reachable without an identity.
//
// The list is deliberately short and explicit rather than pattern-based: every
// entry is a decision to expose something to an unauthenticated caller, and a
// wildcard would let a future route join it by accident.
func isPublicPath(path string) bool {
	switch path {
	case "/login", "/logout", "/auth/oidc/start", "/auth/oidc/callback", "/healthz":
		return true
	default:
		return false
	}
}

// withSecurityHeaders sets the response headers that constrain what a browser
// will do with our pages.
//
// These apply in both modes. Local mode is not a reason to relax them: the
// browser rendering the page is the same browser that has other sites open, and
// a page from elsewhere should not be able to drive this application.
func (s *Server) withSecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// script-src has no 'unsafe-inline': every piece of JavaScript is served
		// as a file from /static, never inlined in a template. That is a
		// deliberate constraint on how the UI is written, and it removes the most
		// common route from an injected string to executed code.
		w.Header().Set("Content-Security-Policy",
			"default-src 'self'; "+
				"script-src 'self'; "+
				"style-src 'self'; "+
				"img-src 'self' data:; "+
				"font-src 'self'; "+
				"connect-src 'self'; "+
				"form-action 'self'; "+
				"base-uri 'none'; "+
				"frame-ancestors 'none'")
		w.Header().Set("X-Content-Type-Options", "nosniff")
		w.Header().Set("Referrer-Policy", "same-origin")

		// HSTS only over HTTPS, and only when the operator asked for it. Sending
		// it over plain HTTP is meaningless, and enabling it by default is
		// hostile: a browser that has seen the header refuses plain HTTP to that
		// host for the whole max-age, which can make a misconfigured deployment
		// unreachable for months with no way to clear it remotely.
		if s.cfg.HSTSMaxAgeSeconds > 0 && s.isHTTPS(r) {
			w.Header().Set("Strict-Transport-Security",
				fmt.Sprintf("max-age=%d", s.cfg.HSTSMaxAgeSeconds))
		}

		next.ServeHTTP(w, r)
	})
}

// isHTTPS reports whether the request reached us over TLS, directly or through
// a proxy we trust to say so.
func (s *Server) isHTTPS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	return s.isTrustedProxy(hostOnly(r.RemoteAddr)) &&
		strings.EqualFold(r.Header.Get("X-Forwarded-Proto"), "https")
}

// withIdentity resolves the acting user and puts them in the request context.
//
// This is the one place an identity enters the system. Service methods read it
// from the context and never accept it as a parameter, so no caller can claim to
// be someone else. See docs/adr/0001-single-binary-two-modes.md.
func (s *Server) withIdentity(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Static assets carry no identity and need none.
		if isStaticPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		// The login routes and the health check are the only unauthenticated
		// surface. Everything else requires an identity before it runs.
		if isPublicPath(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}

		user, err := s.identity(r)
		if err != nil {
			if errors.Is(err, auth.ErrUnauthenticated) {
				// Never data: an unauthenticated request gets the login page and
				// nothing else (ASR-005). The original path travels as ?next= so
				// the user lands where they were going, sanitised by safeNext so
				// it cannot become an open redirect.
				if s.accounts != nil {
					http.Redirect(w, r, "/login?next="+url.QueryEscape(r.URL.RequestURI()),
						http.StatusSeeOther)
					return
				}
				http.Error(w, "Not authenticated.", http.StatusUnauthorized)
				return
			}
			s.serverError(w, r, err)
			return
		}

		request := r.WithContext(auth.WithUser(r.Context(), user))

		// In server mode the session carries the CSRF token the next middleware
		// checks and the templates render into every form.
		if s.accounts != nil {
			if cookie, cookieErr := r.Cookie(auth.SessionCookieName); cookieErr == nil {
				if _, session, sessErr := s.accounts.ResolveSession(r.Context(), cookie.Value); sessErr == nil {
					request = withSession(request, session)
					request = withCSRFToken(request, session.CSRFToken)
				}
			}
		}

		next.ServeHTTP(w, request)
	})
}

// clientIP determines the address to record in the audit trail.
//
// A forwarded header is believed only when the immediate peer is a configured
// trusted proxy. Believing it unconditionally would let anyone write whatever
// they liked into the audit trail, which is worse than having no address at all.
func (s *Server) clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}

	if !s.isTrustedProxy(host) {
		return host
	}
	if forwarded := r.Header.Get("X-Forwarded-For"); forwarded != "" {
		// The left-most entry is the original client; the rest is the chain.
		if comma := indexByte(forwarded, ','); comma > 0 {
			return trimSpace(forwarded[:comma])
		}
		return trimSpace(forwarded)
	}
	return host
}

func (s *Server) isTrustedProxy(host string) bool {
	for _, trusted := range s.cfg.TrustedProxies {
		if trimSpace(trusted) == host {
			return true
		}
	}
	return false
}

// indexByte and trimSpace avoid pulling the strings package in for two uses.
func indexByte(s string, b byte) int {
	for i := 0; i < len(s); i++ {
		if s[i] == b {
			return i
		}
	}
	return -1
}

func trimSpace(s string) string {
	start, end := 0, len(s)
	for start < end && (s[start] == ' ' || s[start] == '\t') {
		start++
	}
	for end > start && (s[end-1] == ' ' || s[end-1] == '\t') {
		end--
	}
	return s[start:end]
}
