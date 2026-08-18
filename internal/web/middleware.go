package web

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"runtime/debug"
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
		next.ServeHTTP(w, r)
	})
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

		user, err := s.identity(r)
		if err != nil {
			if errors.Is(err, auth.ErrUnauthenticated) {
				// Server mode will redirect to the login page here. Local mode
				// cannot reach this branch, because its identity function always
				// resolves.
				http.Error(w, "Not authenticated.", http.StatusUnauthorized)
				return
			}
			s.serverError(w, r, err)
			return
		}

		next.ServeHTTP(w, r.WithContext(auth.WithUser(r.Context(), user)))
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
