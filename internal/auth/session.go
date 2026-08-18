package auth

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"time"
)

// Sessions are server-side records referenced by an opaque cookie.
//
// The alternative - a signed token carrying the identity, such as a JWT - needs
// no storage, but cannot be revoked: a sacked employee keeps access until the
// token expires. Revocation is the property that matters here, so the state is
// worth it. See docs/adr/0006-authentication-model.md.

// Session lifetimes. Both are enforced, because they answer different
// questions: idle expiry catches a browser left open on a shared machine, and
// absolute expiry bounds a session that is kept alive by continued use.
const (
	// SessionIdleTimeout is how long a session survives without a request.
	SessionIdleTimeout = 12 * time.Hour
	// SessionAbsoluteTimeout is the longest a session may live at all.
	SessionAbsoluteTimeout = 30 * 24 * time.Hour
	// SessionCookieName is host-only by omitting the Domain attribute, so the
	// cookie is never sent to a sibling subdomain.
	SessionCookieName = "tt_session"
	// CSRFFieldName is the form field carrying the anti-forgery token.
	CSRFFieldName = "csrf_token"
	// CSRFHeaderName carries the same token on a background request.
	CSRFHeaderName = "X-CSRF-Token"
)

// Session is one authenticated browser.
type Session struct {
	// IDHash is the SHA-256 of the cookie value. Only the hash is stored, so a
	// stolen database yields no usable cookies - the same reasoning that applies
	// to passwords applies to session identifiers, which are equally
	// authenticating.
	IDHash     string
	UserID     int64
	CreatedAt  time.Time
	LastSeenAt time.Time
	ExpiresAt  time.Time
	IP         string
	UserAgent  string
	CSRFToken  string
}

// Expired reports whether the session has passed either lifetime.
func (s Session) Expired(now time.Time) bool {
	return now.After(s.ExpiresAt) || now.Sub(s.LastSeenAt) > SessionIdleTimeout
}

// NewSessionID returns a fresh cookie value and its storage hash.
//
// 256 bits of entropy from the operating system's CSPRNG: a session identifier
// is a bearer credential, and guessing one must be infeasible. A failure to read
// randomness is returned rather than worked around, because every fallback is
// weaker than the thing it replaces.
func NewSessionID() (cookieValue, idHash string, err error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", "", fmt.Errorf("generate session id: %w", err)
	}
	value := base64.RawURLEncoding.EncodeToString(raw)
	return value, HashSessionID(value), nil
}

// HashSessionID maps a cookie value to its storage key.
//
// A plain SHA-256 is right here, unlike for passwords: the input is 256 bits of
// uniform randomness, so there is no dictionary to attack and no reason to make
// the lookup expensive.
func HashSessionID(cookieValue string) string {
	sum := sha256.Sum256([]byte(cookieValue))
	return hex.EncodeToString(sum[:])
}

// NewCSRFToken returns a random anti-forgery token bound to a session.
func NewCSRFToken() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", fmt.Errorf("generate CSRF token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}
