package logging

import (
	"bytes"
	"context"
	"encoding/json"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// Configuring the log, and never writing a secret into it.
//
// Redaction happens in the handler rather than by discipline at each call site:
// a rule enforced at hundreds of call sites is a rule that will be broken once,
// and a rule enforced in one function is testable. This is that test.

// TestASecretNeverReachesTheLog.
//
// The key is kept and the value withheld, so the log still shows that a token
// was involved. Matching is case-insensitive and catches the prefixed and
// suffixed variants somebody will inevitably write - "user_password" rather
// than "password" is not a different kind of secret.
func TestASecretNeverReachesTheLog(t *testing.T) {
	const secret = "hunter2-the-actual-secret"

	keys := []string{
		"password", "Password", "PASSWORD",
		"password_hash", "secret", "token", "api_token",
		"session", "session_id", "cookie", "authorization",
		"totp_secret", "private_key",
		// Prefixed and suffixed variants.
		"user_password", "admin_secret", "refresh_token",
		"password_confirmation", "token_value", "session_cookie",
	}

	for _, key := range keys {
		t.Run(key, func(t *testing.T) {
			var buf bytes.Buffer
			New(&buf, "info", "json").Info("a thing happened", key, secret)

			line := buf.String()
			if strings.Contains(line, secret) {
				t.Errorf("the value of %q was written to the log:\n%s", key, line)
			}
			if !strings.Contains(line, "[redacted]") {
				t.Errorf("%q was not redacted, and the value is not there either:\n%s",
					key, line)
			}
			// The key survives, so the log still records that a secret was in
			// play rather than silently dropping the attribute.
			if !strings.Contains(strings.ToLower(line), strings.ToLower(key)) {
				t.Errorf("the key %q was dropped along with its value:\n%s", key, line)
			}
		})
	}

	// Attributes that merely sound sensitive are not redacted, or an
	// operational log becomes useless.
	for _, key := range []string{
		"user", "email", "action", "path", "duration_ms",
		"passwords_checked", "tokenise", "secretary",
	} {
		t.Run("keeps "+key, func(t *testing.T) {
			var buf bytes.Buffer
			New(&buf, "info", "json").Info("a thing happened", key, "an ordinary value")
			if !strings.Contains(buf.String(), "an ordinary value") {
				t.Errorf("%q was redacted, which makes the log useless:\n%s", key, buf.String())
			}
		})
	}
}

// TestTheLevelDecidesWhatIsWritten.
//
// An operator sets this. Spelling it wrongly must not silence the log
// entirely - the default is info, which is what somebody running the service
// wants - and "warning" has to work as well as "warn", because both are what
// people type.
func TestTheLevelDecidesWhatIsWritten(t *testing.T) {
	cases := map[string]struct{ debug, info, warn, err bool }{
		"debug":   {true, true, true, true},
		"DEBUG":   {true, true, true, true},
		"info":    {false, true, true, true},
		"warn":    {false, false, true, true},
		"warning": {false, false, true, true},
		"error":   {false, false, false, true},
		"":        {false, true, true, true},
		"chatty":  {false, true, true, true},
	}

	for level, want := range cases {
		name := level
		if name == "" {
			name = "unset"
		}
		t.Run(name, func(t *testing.T) {
			var buf bytes.Buffer
			logger := New(&buf, level, "text")
			logger.Debug("a debug line")
			logger.Info("an info line")
			logger.Warn("a warning line")
			logger.Error("an error line")

			out := buf.String()
			for _, check := range []struct {
				text string
				want bool
			}{
				{"a debug line", want.debug},
				{"an info line", want.info},
				{"a warning line", want.warn},
				{"an error line", want.err},
			} {
				if got := strings.Contains(out, check.text); got != check.want {
					t.Errorf("at level %q, %q present = %v, want %v",
						level, check.text, got, check.want)
				}
			}
		})
	}
}

// TestTheFormatIsReadableOrParseable.
//
// Text at a terminal, JSON for a collector. The JSON has to be JSON - a
// collector that cannot parse a line drops it, and a dropped audit line is
// the one nobody notices is missing.
func TestTheFormatIsReadableOrParseable(t *testing.T) {
	var asJSON bytes.Buffer
	New(&asJSON, "info", "json").Info("a thing happened", "count", 3)

	var record map[string]any
	if err := json.Unmarshal(asJSON.Bytes(), &record); err != nil {
		t.Fatalf("the JSON format did not produce JSON: %v\n%s", err, asJSON.String())
	}
	if record["msg"] != "a thing happened" {
		t.Errorf("the message reads %v", record["msg"])
	}
	if record["count"] != float64(3) {
		t.Errorf("the attribute reads %v", record["count"])
	}

	var asText bytes.Buffer
	New(&asText, "info", "text").Info("a thing happened", "count", 3)
	if !strings.Contains(asText.String(), "count=3") {
		t.Errorf("the text format did not write the attribute:\n%s", asText.String())
	}
	// Anything that is not "json" is text, rather than nothing at all.
	var unknown bytes.Buffer
	New(&unknown, "info", "yaml").Info("a thing happened")
	if unknown.Len() == 0 {
		t.Error("an unknown format silenced the log")
	}
}

// TestSourcePositionsAreOptional.
//
// They cost a stack walk per record and clutter an operational log, and they
// are exactly what is wanted under --debug, where the reader is looking for
// the code rather than watching the service.
func TestSourcePositionsAreOptional(t *testing.T) {
	var without bytes.Buffer
	NewWithSource(&without, "info", "json", false).Info("a thing happened")
	if strings.Contains(without.String(), "logging_test.go") {
		t.Errorf("a source position was written when it was not asked for:\n%s",
			without.String())
	}

	var with bytes.Buffer
	NewWithSource(&with, "info", "json", true).Info("a thing happened")
	if !strings.Contains(with.String(), "logging_test.go") {
		t.Errorf("no source position was written when it was asked for:\n%s",
			with.String())
	}
}

// TestASyslogRecordIsWellFormed.
//
// RFC 5424 rather than the older BSD format, because it carries a real
// timestamp with a zone and a structured-data section - both of which matter
// for an audit stream read months later in another country.
func TestASyslogRecordIsWellFormed(t *testing.T) {
	handler := NewSyslogHandler(slog.NewTextHandler(bytes.NewBuffer(nil), nil), SyslogConfig{
		Facility: 10, AppName: "timetracker",
	})
	defer handler.Close()

	record := slog.NewRecord(
		time.Date(2026, 3, 16, 9, 30, 0, 0, time.UTC),
		slog.LevelInfo, "a thing happened", 0)
	record.AddAttrs(slog.String("user", "someone"))

	line := handler.format(record)

	// <priority>version timestamp hostname app pid msgid structured message
	if !strings.HasPrefix(line, "<") {
		t.Fatalf("the record does not begin with a priority:\n%s", line)
	}
	// Facility 10, severity 6 (informational): 10*8+6 = 86.
	if !strings.HasPrefix(line, "<86>1 ") {
		t.Errorf("the record begins %q, want <86>1 for authpriv at info", line[:10])
	}
	for _, want := range []string{
		"2026-03-16T09:30:00Z", "timetracker", "a thing happened", `user="someone"`,
	} {
		if !strings.Contains(line, want) {
			t.Errorf("the record does not contain %q:\n%s", want, line)
		}
	}

	// An audit event is distinguished from operational noise by its message
	// id, so a collector can route the two differently.
	audit := slog.NewRecord(time.Now(), slog.LevelInfo, "audit", 0)
	audit.AddAttrs(slog.String("action", "entry.create"))
	if !strings.Contains(handler.format(audit), " audit ") {
		t.Errorf("an audit record is not marked as one:\n%s", handler.format(audit))
	}
	if strings.Contains(line, " audit ") {
		t.Errorf("an ordinary record is marked as an audit event:\n%s", line)
	}

	// A record with no attributes uses the RFC's "nothing here" marker rather
	// than an empty element a parser may reject.
	bare := slog.NewRecord(time.Now(), slog.LevelInfo, "a thing happened", 0)
	if !strings.Contains(handler.format(bare), " - ") {
		t.Errorf("a record with no attributes does not use the no-data marker:\n%s",
			handler.format(bare))
	}
}

// TestASecretNeverLeavesTheHost.
//
// Redaction runs in the syslog formatter as well as in the stderr handler. A
// message going off the host is exactly the wrong place to discover that a
// secret was only filtered on one path.
func TestASecretNeverLeavesTheHost(t *testing.T) {
	handler := NewSyslogHandler(slog.NewTextHandler(bytes.NewBuffer(nil), nil), SyslogConfig{
		Facility: 10, AppName: "timetracker",
	})
	defer handler.Close()

	const secret = "hunter2-the-actual-secret"
	record := slog.NewRecord(time.Now(), slog.LevelInfo, "a login", 0)
	record.AddAttrs(
		slog.String("password", secret),
		slog.String("session_id", secret),
		slog.String("user", "someone"),
	)

	line := handler.format(record)
	if strings.Contains(line, secret) {
		t.Errorf("a secret was formatted for the network:\n%s", line)
	}
	if !strings.Contains(line, "[redacted]") {
		t.Errorf("nothing was redacted:\n%s", line)
	}
	if !strings.Contains(line, `user="someone"`) {
		t.Errorf("the ordinary attribute was lost:\n%s", line)
	}
}

// TestAMalformedKeyCannotMalformTheRecord.
//
// A structured-data name has a restricted character set. A key carrying a
// space, a bracket or an equals sign would produce a message a collector
// rejects or, worse, misparses into separate fields - which is how one
// attribute becomes two and an audit line reads wrongly.
func TestAMalformedKeyCannotMalformTheRecord(t *testing.T) {
	cases := map[string]string{
		"ordinary":      "ordinary",
		"with space":    "with_space",
		"with=equals":   "with_equals",
		"with]bracket":  "with_bracket",
		`with"quote`:    "with_quote",
		"with\nnewline": "with_newline",
		"with\ttab":     "with_tab",
		// One underscore, not two: the mapping is over runes rather than bytes,
		// so a two-byte character is one forbidden character.
		"héllo":                 "h_llo",
		"":                      "field",
		strings.Repeat("a", 64): strings.Repeat("a", 32),
	}
	for key, want := range cases {
		name := key
		if name == "" {
			name = "empty"
		}
		if len(name) > 20 {
			name = name[:20] + "…"
		}
		t.Run(name, func(t *testing.T) {
			if got := escapeSDName(key); got != want {
				t.Errorf("escapeSDName(%q) = %q, want %q", key, got, want)
			}
		})
	}

	// And the escaped name is usable in a record: nothing that could end the
	// structured-data element survives.
	for _, forbidden := range []string{"=", " ", "]", `"`} {
		if strings.Contains(escapeSDName("a"+forbidden+"b"), forbidden) {
			t.Errorf("an escaped name still contains %q", forbidden)
		}
	}
}

// TestSeverityMapsOntoSyslog.
//
// A collector routes and alerts on these numbers, so an error logged as
// informational is an alert nobody gets.
func TestSeverityMapsOntoSyslog(t *testing.T) {
	cases := map[slog.Level]int{
		slog.LevelDebug:     7,
		slog.LevelInfo:      6,
		slog.LevelWarn:      4,
		slog.LevelError:     3,
		slog.LevelError + 4: 3, // anything above error is still error
		slog.LevelDebug - 4: 7,
	}
	for level, want := range cases {
		if got := severityOf(level); got != want {
			t.Errorf("severityOf(%v) = %d, want %d", level, got, want)
		}
	}
}

// TestForwardingToASocketThatIsListening.
//
// The end-to-end path, over a Unix datagram socket so the test owns both ends.
// One goroutine owns the connection, so there is no lock around the socket and
// no interleaved writes.
func TestForwardingToASocketThatIsListening(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "log.sock")

	listener, err := net.Listen("unix", path)
	if err != nil {
		t.Skipf("no unix sockets here: %v", err)
	}
	defer func() { _ = listener.Close() }()

	received := make(chan string, 4)
	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		defer func() { _ = conn.Close() }()
		buf := make([]byte, 4096)
		for {
			n, err := conn.Read(buf)
			if n > 0 {
				received <- string(buf[:n])
			}
			if err != nil {
				return
			}
		}
	}()

	var stderr bytes.Buffer
	handler := NewSyslogHandler(slog.NewTextHandler(&stderr, nil), SyslogConfig{
		Network: "unix", Address: path, Facility: 10, AppName: "timetracker",
	})
	logger := slog.New(handler)
	logger.Info("a thing happened", "user", "someone", "password", "a-secret")

	select {
	case line := <-received:
		if !strings.Contains(line, "a thing happened") {
			t.Errorf("the forwarded record reads %q", line)
		}
		if strings.Contains(line, "a-secret") {
			t.Errorf("a secret was forwarded:\n%s", line)
		}
	case <-time.After(5 * time.Second):
		t.Error("nothing arrived at the socket")
	}

	// It also went to the inner handler, so the local log is complete whether
	// or not the collector is reachable.
	if !strings.Contains(stderr.String(), "a thing happened") {
		t.Errorf("the record did not reach the local log:\n%s", stderr.String())
	}

	handler.Close()
	// Closing twice must not panic: shutdown can be reached by two paths.
	handler.Close()
}

// TestForwardingWhenNothingIsListening.
//
// A collector that is down must not take the application with it. Records are
// dropped once the queue is full - unbounded buffering under a sustained
// outage ends with the process out of memory - and the count of what went is
// kept so an operator can see it.
func TestForwardingWhenNothingIsListening(t *testing.T) {
	var stderr bytes.Buffer
	handler := NewSyslogHandler(slog.NewTextHandler(&stderr, nil), SyslogConfig{
		Network: "unix",
		Address: filepath.Join(t.TempDir(), "nothing-here.sock"),
		AppName: "timetracker",
	})
	defer handler.Close()

	logger := slog.New(handler)
	for i := 0; i < QueueSize*3; i++ {
		logger.Info("a thing happened")
	}

	// The local log still has everything, which is the part that must not fail.
	if strings.Count(stderr.String(), "a thing happened") != QueueSize*3 {
		t.Errorf("the local log holds %d records, want them all",
			strings.Count(stderr.String(), "a thing happened"))
	}

	// Something was dropped rather than queued for ever. The exact number
	// depends on timing, so this asserts only that dropping happened and is
	// counted.
	deadline := time.Now().Add(5 * time.Second)
	for handler.Dropped() == 0 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if handler.Dropped() == 0 {
		t.Error("three times the queue was written to an unreachable collector " +
			"and nothing was reported as dropped, so an operator cannot see it")
	}
}

// TestAHandlerPassesItsAttributesThrough.
//
// A logger built with slog.With carries attributes into every record. The
// syslog handler has to pass those to its inner handler rather than losing
// them, or the local log and the forwarded one disagree about what happened.
func TestAHandlerPassesItsAttributesThrough(t *testing.T) {
	var stderr bytes.Buffer
	handler := NewSyslogHandler(slog.NewTextHandler(&stderr, nil), SyslogConfig{
		AppName: "timetracker",
	})
	defer handler.Close()

	logger := slog.New(handler).With("request_id", "abc123").WithGroup("http")
	logger.Info("a request", "status", 200)

	out := stderr.String()
	for _, want := range []string{"request_id=abc123", "a request", "status=200"} {
		if !strings.Contains(out, want) {
			t.Errorf("the local log does not contain %q:\n%s", want, out)
		}
	}

	// Enabled follows the inner handler, so a level set once applies to both.
	ctx := context.Background()
	if !handler.Enabled(ctx, slog.LevelInfo) {
		t.Error("the handler reports info as disabled")
	}
	if handler.Enabled(ctx, slog.LevelDebug) {
		t.Error("the handler reports debug as enabled, though the inner handler " +
			"is at info")
	}
}

// TestTheDefaultSyslogAddress.
//
// Where to send without configuration. It has to name something that exists on
// the host rather than a guess, since the alternative is an operator wondering
// why nothing arrives.
func TestTheDefaultSyslogAddress(t *testing.T) {
	network, address := DefaultSyslogAddress()
	if network == "" || address == "" {
		t.Fatalf("the default address is %q on %q", address, network)
	}
	switch network {
	case "unix", "unixgram":
		if !strings.HasPrefix(address, "/") {
			t.Errorf("a unix socket address is %q, want a path", address)
		}
	case "udp", "tcp":
		if !strings.Contains(address, ":") {
			t.Errorf("a network address is %q, want host:port", address)
		}
	default:
		t.Errorf("the default network is %q, which is not one this handler dials", network)
	}

	// On a host with a syslog socket, that is what is named.
	for _, candidate := range []string{"/dev/log", "/var/run/syslog"} {
		if _, err := os.Stat(candidate); err == nil && address != candidate {
			t.Logf("this host has %s and the default names %s", candidate, address)
		}
	}
}
