// Package logging configures the application's structured logging.
//
// Two sinks are planned: stderr in both modes, and rsyslog in server mode
// (docs/adr/0010-audit-log-and-rsyslog.md). The rsyslog handler arrives with
// server mode in layer 2; what exists here is the stderr configuration and the
// redaction step every handler passes through.
package logging

import (
	"io"
	"log/slog"
	"strings"
)

// New builds the application logger.
//
// level is one of debug, info, warn, error; format is "text" (readable at a
// terminal) or "json" (for a collector that parses it).
func New(w io.Writer, level, format string) *slog.Logger {
	return NewWithSource(w, level, format, false)
}

// NewWithSource builds the logger, optionally annotating every record with the
// file and line that produced it.
//
// Source positions are off by default because they cost a stack walk per record
// and clutter an operational log. They are exactly what is wanted under --debug,
// where the reader is trying to find the code rather than watch the service.
func NewWithSource(w io.Writer, level, format string, addSource bool) *slog.Logger {
	options := &slog.HandlerOptions{
		Level:       parseLevel(level),
		AddSource:   addSource,
		ReplaceAttr: redact,
	}

	var handler slog.Handler
	if format == "json" {
		handler = slog.NewJSONHandler(w, options)
	} else {
		handler = slog.NewTextHandler(w, options)
	}
	return slog.New(handler)
}

func parseLevel(level string) slog.Level {
	switch strings.ToLower(level) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// sensitiveKeys are attribute names whose values must never be written to a log.
//
// Redaction happens here, in the handler, rather than by discipline at each call
// site. A rule enforced at hundreds of call sites is a rule that will be broken
// once; a rule enforced in one function is testable.
var sensitiveKeys = map[string]bool{
	"password":      true,
	"password_hash": true,
	"secret":        true,
	"token":         true,
	"api_token":     true,
	"session":       true,
	"session_id":    true,
	"cookie":        true,
	"authorization": true,
	"totp_secret":   true,
	"private_key":   true,
}

// redact replaces the value of any sensitive attribute before it is written.
//
// The key is kept, so the log still shows that a token was involved - only its
// value is withheld. Matching is case-insensitive and also catches prefixed and
// suffixed variants such as "user_password".
func redact(groups []string, a slog.Attr) slog.Attr {
	key := strings.ToLower(a.Key)
	if sensitiveKeys[key] {
		return slog.String(a.Key, "[redacted]")
	}
	for sensitive := range sensitiveKeys {
		if strings.HasSuffix(key, "_"+sensitive) || strings.HasPrefix(key, sensitive+"_") {
			return slog.String(a.Key, "[redacted]")
		}
	}
	return a
}
