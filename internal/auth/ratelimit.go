package auth

import (
	"sync"
	"time"
)

// LoginLimiter throttles repeated authentication failures.
//
// It counts failures per key - an account name, and separately a source address
// - and refuses further attempts once a threshold is crossed, for a window that
// grows with the number of failures. Successes clear the counter.
//
// Two keys rather than one, because they defend against different attacks:
// per-account stops someone grinding a single user's password, per-address stops
// someone spraying one common password across many accounts. Limiting only by
// account leaves the spray attack untouched.
//
// This is in-process state. It does not survive a restart and does not
// coordinate across instances - which is honest for a single-node application
// (docs/ARCHITECTURE.md §11), and is documented as a limitation in
// docs/SECURITY.md rather than pretended away.
type LoginLimiter struct {
	mu       sync.Mutex
	attempts map[string]*attemptRecord
	// now is injected so the behaviour can be tested without sleeping.
	now func() time.Time
}

type attemptRecord struct {
	failures  int
	lockUntil time.Time
	lastSeen  time.Time
}

// Threshold and backoff. The first few failures are free, because people
// mistype passwords; after that the delay grows quickly.
const (
	loginFreeAttempts = 5
	loginMaxLockout   = 15 * time.Minute
	// Records untouched for this long are discarded, so the map cannot grow
	// without bound from one-off failures across many addresses.
	loginRecordTTL = time.Hour
)

// NewLoginLimiter builds a limiter. A nil clock defaults to time.Now.
func NewLoginLimiter(now func() time.Time) *LoginLimiter {
	if now == nil {
		now = time.Now
	}
	return &LoginLimiter{attempts: map[string]*attemptRecord{}, now: now}
}

// Allowed reports whether an attempt may proceed, and how long to wait if not.
func (l *LoginLimiter) Allowed(keys ...string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	l.sweepLocked(now)

	for _, key := range keys {
		record, ok := l.attempts[key]
		if !ok {
			continue
		}
		if now.Before(record.lockUntil) {
			return false, record.lockUntil.Sub(now)
		}
	}
	return true, 0
}

// Failed records an unsuccessful attempt against every supplied key.
func (l *LoginLimiter) Failed(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	for _, key := range keys {
		record, ok := l.attempts[key]
		if !ok {
			record = &attemptRecord{}
			l.attempts[key] = record
		}
		record.failures++
		record.lastSeen = now

		// The lockout engages on the last free attempt being used up, not one
		// failure later: loginFreeAttempts is how many failures are tolerated in
		// total, not how many precede the first delay.
		if record.failures >= loginFreeAttempts {
			// Exponential backoff from one second, capped: 1s, 2s, 4s ... 15m.
			// The cap matters - an uncapped lockout lets an attacker deny a real
			// user access indefinitely by failing on their behalf.
			exponent := record.failures - loginFreeAttempts
			delay := time.Second << uint(min(exponent, 20))
			if delay > loginMaxLockout {
				delay = loginMaxLockout
			}
			record.lockUntil = now.Add(delay)
		}
	}
}

// Succeeded clears the counters for the supplied keys.
func (l *LoginLimiter) Succeeded(keys ...string) {
	l.mu.Lock()
	defer l.mu.Unlock()

	for _, key := range keys {
		delete(l.attempts, key)
	}
}

// sweepLocked discards records nobody has touched recently. Called on the read
// path rather than from a goroutine: the map only grows when someone is failing
// to log in, so there is nothing to collect when the system is quiet.
func (l *LoginLimiter) sweepLocked(now time.Time) {
	for key, record := range l.attempts {
		if now.Sub(record.lastSeen) > loginRecordTTL && now.After(record.lockUntil) {
			delete(l.attempts, key)
		}
	}
}
