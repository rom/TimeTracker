// Package hardening applies the process-level restrictions the platform offers.
//
// The principle throughout: the application asks the operating system to take
// away privileges it will never need, *after* it has finished needing them. A
// restriction applied at start-up cannot be undone, so a defect exploited later
// in the process's life runs inside the smaller box.
//
// What is available differs sharply by platform, and this package is honest
// about that rather than pretending otherwise:
//
//	Linux    Landlock (filesystem), plus everything the systemd unit imposes
//	         from outside: seccomp, capability bounding, namespaces, read-only
//	         filesystems. See deploy/systemd/.
//	macOS    the sandbox and hardened runtime are applied from outside the
//	         process, by launchd and by code signing. See deploy/macos/.
//	Windows  restrictions come from the service account, the ACLs on the data
//	         directory and WDAC/AppLocker policy. See deploy/windows/.
//
// Nothing here is a substitute for the deployment configuration; it is the layer
// that still applies when someone runs the binary by hand.
package hardening

import (
	"fmt"
	"log/slog"
)

// Mode selects how hard to try.
type Mode string

const (
	// ModeOff applies nothing.
	ModeOff Mode = "off"
	// ModeAuto applies what the running kernel supports and continues without
	// complaint when it supports nothing.
	ModeAuto Mode = "auto"
	// ModeEnforce refuses to start if the restrictions cannot be applied. For an
	// operator who has decided the sandbox is not optional.
	ModeEnforce Mode = "enforce"
)

// Policy describes what the process still needs access to.
//
// Everything not listed is denied once the policy is applied, so this is the
// complete inventory of the application's filesystem needs - which is a useful
// document in itself.
type Policy struct {
	// DataDir holds the database and attachments: read, write, create, delete.
	DataDir string
	// ReadOnlyPaths are needed but never written: TLS certificates, system
	// libraries, the certificate trust store.
	ReadOnlyPaths []string
	// TempDir is where multipart uploads spill when they exceed the memory
	// limit, so it needs the same access as the data directory.
	TempDir string
}

// Result reports what was actually applied, so it can be logged and shown on the
// health endpoint. An operator should never have to guess whether the sandbox
// engaged.
type Result struct {
	// Applied names the mechanisms that took effect, e.g. "landlock (ABI 4)".
	Applied []string
	// Unavailable names mechanisms that were not available here, with the
	// reason.
	Unavailable []string
}

// Log writes the outcome at start-up.
func (r Result) Log(log *slog.Logger) {
	for _, applied := range r.Applied {
		log.Info("hardening applied", "mechanism", applied)
	}
	for _, unavailable := range r.Unavailable {
		log.Info("hardening unavailable", "detail", unavailable)
	}
	if len(r.Applied) == 0 {
		log.Warn("no in-process hardening is active; " +
			"see deploy/ for the platform's external mechanisms")
	}
}

// Summary renders the result as a short string for the health endpoint.
func (r Result) Summary() string {
	if len(r.Applied) == 0 {
		return "none"
	}
	summary := r.Applied[0]
	for _, applied := range r.Applied[1:] {
		summary += ", " + applied
	}
	return summary
}

// Apply imposes the policy. It is a no-op on platforms with nothing to apply.
//
// It must be called after the data directory exists and after any file the
// process needs has been located, but before the first request is served.
func Apply(mode Mode, policy Policy) (Result, error) {
	switch mode {
	case ModeOff, "":
		return Result{Unavailable: []string{"disabled by configuration"}}, nil
	case ModeAuto, ModeEnforce:
	default:
		return Result{}, fmt.Errorf("unknown hardening mode %q", mode)
	}

	result, err := apply(policy)
	if err != nil {
		if mode == ModeEnforce {
			return result, fmt.Errorf("could not apply hardening: %w", err)
		}
		// In auto mode a kernel without support is an ordinary fact, not a
		// failure: the binary is meant to run on three platforms and many
		// kernel versions.
		result.Unavailable = append(result.Unavailable, err.Error())
		return result, nil
	}
	return result, nil
}
