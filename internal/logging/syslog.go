package logging

import (
	"context"
	"crypto/tls"
	"fmt"
	"log/slog"
	"net"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// rsyslog forwarding, in RFC 5424 format.
//
// The design constraint that shapes everything here: a syslog collector that
// goes away must never block a user's request or fail their write. Delivery is
// therefore best-effort through a bounded queue, and the in-database audit trail
// remains the record of truth. Dropped messages are counted rather than hidden,
// so an operator can see that they happened.
//
// See docs/adr/0010-audit-log-and-rsyslog.md.

// SyslogConfig describes where and how to forward.
type SyslogConfig struct {
	// Network is "unix", "unixgram", "tcp", "tcp+tls" or "udp". Empty disables
	// forwarding entirely.
	Network string
	// Address is the socket path for unix, or host:port otherwise.
	Address string
	// Facility is the RFC 5424 facility number; 1 (user) by default, and 10
	// (authpriv) is a common choice for an application handling logins.
	Facility int
	// AppName appears in every message, so an operator can filter by it.
	AppName string
	// TLSConfig is used when Network is "tcp+tls". Sending an audit trail
	// unencrypted across a network would defeat the point of having one.
	TLSConfig *tls.Config
}

// QueueSize bounds the buffer between the application and the collector.
//
// It exists to absorb a brief stall, not an outage: once full, messages are
// dropped rather than queued indefinitely, because unbounded buffering under a
// sustained outage ends with the process out of memory.
const QueueSize = 1024

// SyslogHandler is an slog.Handler that forwards records to syslog.
//
// It wraps another handler rather than replacing it: stderr logging continues
// regardless, so an operator watching the console still sees everything even
// while the collector is unreachable.
type SyslogHandler struct {
	inner  slog.Handler
	cfg    SyslogConfig
	queue  chan string
	closed chan struct{}
	once   sync.Once

	// dropped counts messages the queue could not accept. Exposed rather than
	// silently discarded: "we lost some audit events" is information an operator
	// needs.
	dropped atomic.Uint64

	hostname string
	pid      int
}

// NewSyslogHandler wraps inner with syslog forwarding.
//
// It does not fail when the collector is unreachable at start-up. A logging
// destination being down is not a reason to refuse to run a timesheet - the
// forwarder retries in the background, and the audit trail in the database is
// unaffected either way.
func NewSyslogHandler(inner slog.Handler, cfg SyslogConfig) *SyslogHandler {
	if cfg.Facility == 0 {
		cfg.Facility = 10 // authpriv: this stream carries authentication events
	}
	if cfg.AppName == "" {
		cfg.AppName = "timetracker"
	}

	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "-"
	}

	h := &SyslogHandler{
		inner:    inner,
		cfg:      cfg,
		queue:    make(chan string, QueueSize),
		closed:   make(chan struct{}),
		hostname: hostname,
		pid:      os.Getpid(),
	}
	go h.forward()
	return h
}

// Enabled reports whether a level is handled, delegating to the wrapped handler.
func (h *SyslogHandler) Enabled(ctx context.Context, level slog.Level) bool {
	return h.inner.Enabled(ctx, level)
}

// Handle writes the record to the wrapped handler and queues it for syslog.
//
// The queue send is non-blocking. This is the whole point: if the forwarder is
// stalled on an unreachable collector, the user's request continues and the
// message is counted as dropped.
func (h *SyslogHandler) Handle(ctx context.Context, record slog.Record) error {
	// stderr first, so the local log is complete even if forwarding fails.
	if err := h.inner.Handle(ctx, record); err != nil {
		return err
	}

	select {
	case h.queue <- h.format(record):
	default:
		h.dropped.Add(1)
	}
	return nil
}

// WithAttrs and WithGroup delegate, so slog's structured API works normally.
func (h *SyslogHandler) WithAttrs(attrs []slog.Attr) slog.Handler {
	return &SyslogHandler{
		inner: h.inner.WithAttrs(attrs), cfg: h.cfg, queue: h.queue, closed: h.closed,
		hostname: h.hostname, pid: h.pid,
	}
}

// WithGroup delegates to the wrapped handler; see WithAttrs above.
func (h *SyslogHandler) WithGroup(name string) slog.Handler {
	return &SyslogHandler{
		inner: h.inner.WithGroup(name), cfg: h.cfg, queue: h.queue, closed: h.closed,
		hostname: h.hostname, pid: h.pid,
	}
}

// Dropped returns how many messages the queue could not accept. The health
// endpoint reports it, so a silent gap in the collector's view is visible from
// the application side.
func (h *SyslogHandler) Dropped() uint64 { return h.dropped.Load() }

// Close stops the forwarder.
func (h *SyslogHandler) Close() {
	h.once.Do(func() { close(h.closed) })
}

// format renders one record as an RFC 5424 message.
//
//	<PRI>1 TIMESTAMP HOSTNAME APP-NAME PROCID MSGID STRUCTURED-DATA MSG
//
// RFC 5424 rather than the older BSD format because it carries a real timestamp
// with a time zone and a structured-data section, both of which matter for an
// audit stream that may be read months later in a different country.
func (h *SyslogHandler) format(record slog.Record) string {
	priority := h.cfg.Facility*8 + severityOf(record.Level)

	// The message id distinguishes an audit event from ordinary operational
	// noise, so a collector can route the two differently.
	msgID := "app"
	structured := &strings.Builder{}
	structured.WriteString(`[tt@0 `)

	first := true
	record.Attrs(func(a slog.Attr) bool {
		// Redaction runs here as well as in the stderr handler. A message going
		// off the host is exactly the wrong place to discover that a secret was
		// only filtered on one path.
		a = redact(nil, a)
		if a.Key == "action" {
			msgID = "audit"
		}
		if !first {
			structured.WriteByte(' ')
		}
		first = false
		fmt.Fprintf(structured, `%s=%q`, escapeSDName(a.Key), a.Value.String())
		return true
	})
	structured.WriteByte(']')

	sd := structured.String()
	if sd == "[tt@0 ]" {
		sd = "-" // the RFC's "no structured data" marker
	}

	return fmt.Sprintf("<%d>1 %s %s %s %d %s %s %s",
		priority,
		record.Time.UTC().Format(time.RFC3339),
		h.hostname,
		h.cfg.AppName,
		h.pid,
		msgID,
		sd,
		record.Message)
}

// severityOf maps an slog level onto a syslog severity.
func severityOf(level slog.Level) int {
	switch {
	case level >= slog.LevelError:
		return 3 // error
	case level >= slog.LevelWarn:
		return 4 // warning
	case level >= slog.LevelInfo:
		return 6 // informational
	default:
		return 7 // debug
	}
}

// escapeSDName strips the characters RFC 5424 forbids in a structured-data name,
// so a malformed key cannot produce a message a collector rejects or, worse,
// misparses into separate fields.
func escapeSDName(name string) string {
	name = strings.Map(func(r rune) rune {
		switch r {
		case '=', ' ', ']', '"':
			return '_'
		}
		if r < 33 || r > 126 {
			return '_'
		}
		return r
	}, name)
	if len(name) > 32 {
		name = name[:32]
	}
	if name == "" {
		name = "field"
	}
	return name
}

// forward is the single goroutine that owns the connection.
//
// One goroutine, so there is no lock around the socket and no interleaved
// writes. It reconnects lazily: a failed write drops the connection and the next
// message tries again, with a floor on how often, so an outage does not turn
// into a reconnect storm.
func (h *SyslogHandler) forward() {
	var conn net.Conn
	var lastAttempt time.Time
	const reconnectInterval = 5 * time.Second

	defer func() {
		if conn != nil {
			_ = conn.Close()
		}
	}()

	for {
		select {
		case <-h.closed:
			return
		case message := <-h.queue:
			if conn == nil {
				if time.Since(lastAttempt) < reconnectInterval {
					// Still in the back-off window: drop rather than stall.
					h.dropped.Add(1)
					continue
				}
				lastAttempt = time.Now()
				var err error
				if conn, err = h.dial(); err != nil {
					h.dropped.Add(1)
					continue
				}
			}

			// A write deadline matters: a TCP collector that accepts the
			// connection and then stops reading would otherwise block this
			// goroutine forever and silently stop all forwarding.
			_ = conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
			if _, err := fmt.Fprintf(conn, "%s\n", message); err != nil {
				_ = conn.Close()
				conn = nil
				h.dropped.Add(1)
			}
		}
	}
}

// dial opens the configured transport.
func (h *SyslogHandler) dial() (net.Conn, error) {
	const dialTimeout = 5 * time.Second

	switch h.cfg.Network {
	case "tcp+tls":
		tlsCfg := h.cfg.TLSConfig
		if tlsCfg == nil {
			// A nil config still verifies the server certificate against the
			// system roots; it is not an "insecure" default.
			tlsCfg = &tls.Config{MinVersion: tls.VersionTLS12}
		}
		return tls.DialWithDialer(&net.Dialer{Timeout: dialTimeout}, "tcp", h.cfg.Address, tlsCfg)
	case "":
		return nil, fmt.Errorf("no syslog network configured")
	default:
		return net.DialTimeout(h.cfg.Network, h.cfg.Address, dialTimeout)
	}
}

// DefaultSyslogAddress returns the conventional local socket for a platform, or
// empty where there is none. Windows has no syslog socket, so an operator there
// configures a remote collector explicitly.
func DefaultSyslogAddress() (network, address string) {
	for _, candidate := range []struct{ network, address string }{
		{"unixgram", "/dev/log"},        // Linux
		{"unixgram", "/var/run/syslog"}, // macOS
		{"unix", "/var/run/log"},        // some BSDs
	} {
		if _, err := os.Stat(candidate.address); err == nil {
			return candidate.network, candidate.address
		}
	}
	return "", ""
}
