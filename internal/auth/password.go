package auth

import (
	"crypto/rand"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	"golang.org/x/crypto/argon2"
)

// Password hashing uses Argon2id, the memory-hard function recommended by OWASP
// for new applications. The cost of an offline attack against a stolen database
// is what this buys, and memory hardness is what makes GPU and ASIC attacks
// expensive rather than merely slow.
//
// See docs/adr/0006-authentication-model.md.

// ErrInvalidCredentials is returned for every authentication failure - wrong
// password, unknown account, disabled account.
//
// One error for all of them, deliberately: a login endpoint that distinguishes
// "no such user" from "wrong password" is an account enumeration oracle.
var ErrInvalidCredentials = errors.New("invalid credentials")

// errMalformedHash means a stored hash could not be parsed. It is separate from
// a wrong password because it indicates data corruption or a downgrade, not a
// failed login attempt, and should be investigated rather than counted.
var errMalformedHash = errors.New("malformed password hash")

// argon2Params are the cost parameters used for new hashes.
//
// They are stored inside each hash rather than assumed, so they can be raised
// later without invalidating existing passwords: an old hash still verifies with
// its own parameters, and is transparently upgraded on the next successful
// login (see NeedsRehash).
//
// The defaults follow the OWASP recommendation of 19 MiB and two iterations.
// Memory is the parameter to raise first if these need strengthening; time is
// cheaper for an attacker to parallelise.
type argon2Params struct {
	memoryKiB   uint32
	iterations  uint32
	parallelism uint8
	saltLength  uint32
	keyLength   uint32
}

var defaultParams = argon2Params{
	memoryKiB:   19 * 1024, // 19 MiB
	iterations:  2,
	parallelism: 1,
	saltLength:  16,
	keyLength:   32,
}

// HashPassword derives a storable hash from a plaintext password.
//
// The returned string is self-describing, in the standard PHC format:
//
//	$argon2id$v=19$m=19456,t=2,p=1$<salt>$<hash>
//
// so a future version can read a hash produced by this one even if the defaults
// have changed.
func HashPassword(password string) (string, error) {
	return hashPasswordWith(password, defaultParams)
}

func hashPasswordWith(password string, p argon2Params) (string, error) {
	if len(password) < 12 {
		// A minimum length is the only password rule enforced here. Composition
		// rules (a digit, a symbol) push people towards predictable
		// substitutions and are no longer recommended; length is what matters.
		return "", fmt.Errorf("%w: a password must be at least 12 characters", ErrValidationPassword)
	}
	if len(password) > 1024 {
		// An upper bound is a denial-of-service guard: hashing is deliberately
		// expensive, and an unbounded input makes that a weapon.
		return "", fmt.Errorf("%w: a password may be at most 1024 characters", ErrValidationPassword)
	}

	salt := make([]byte, p.saltLength)
	if _, err := rand.Read(salt); err != nil {
		// A failing CSPRNG must never fall back to something weaker.
		return "", fmt.Errorf("generate salt: %w", err)
	}

	key := argon2.IDKey([]byte(password), salt, p.iterations, p.memoryKiB, p.parallelism, p.keyLength)

	return fmt.Sprintf("$argon2id$v=%d$m=%d,t=%d,p=%d$%s$%s",
		argon2.Version, p.memoryKiB, p.iterations, p.parallelism,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(key)), nil
}

// ErrValidationPassword marks a password that fails the policy, as opposed to
// one that is merely wrong.
var ErrValidationPassword = errors.New("password policy")

// VerifyPassword checks a plaintext password against a stored hash.
//
// The comparison is constant-time: a byte-by-byte comparison that returns early
// leaks, through timing, how much of the hash was guessed correctly.
func VerifyPassword(password, encoded string) error {
	params, salt, want, err := decodeHash(encoded)
	if err != nil {
		return err
	}

	got := argon2.IDKey([]byte(password), salt,
		params.iterations, params.memoryKiB, params.parallelism, uint32(len(want)))

	if subtle.ConstantTimeCompare(got, want) != 1 {
		return ErrInvalidCredentials
	}
	return nil
}

// NeedsRehash reports whether a stored hash was produced with weaker parameters
// than the current defaults.
//
// Callers re-hash on the next successful login, which is the only moment the
// plaintext is available. This is how cost parameters get raised across an
// existing user base without asking anyone to change their password.
func NeedsRehash(encoded string) bool {
	params, _, _, err := decodeHash(encoded)
	if err != nil {
		// An unparseable hash cannot be verified against anyway; treating it as
		// needing a rehash is the safe reading.
		return true
	}
	return params.memoryKiB < defaultParams.memoryKiB ||
		params.iterations < defaultParams.iterations ||
		params.parallelism < defaultParams.parallelism
}

// decodeHash parses the PHC-format string back into its parts.
func decodeHash(encoded string) (argon2Params, []byte, []byte, error) {
	parts := strings.Split(encoded, "$")
	// "", "argon2id", "v=19", "m=...,t=...,p=...", salt, hash
	if len(parts) != 6 || parts[1] != "argon2id" {
		return argon2Params{}, nil, nil, errMalformedHash
	}

	var version int
	if _, err := fmt.Sscanf(parts[2], "v=%d", &version); err != nil {
		return argon2Params{}, nil, nil, errMalformedHash
	}
	if version != argon2.Version {
		// A hash from a different Argon2 version cannot be verified by this
		// implementation. Failing loudly beats silently rejecting the user's
		// correct password as wrong.
		return argon2Params{}, nil, nil, fmt.Errorf("%w: unsupported argon2 version %d",
			errMalformedHash, version)
	}

	var p argon2Params
	if _, err := fmt.Sscanf(parts[3], "m=%d,t=%d,p=%d",
		&p.memoryKiB, &p.iterations, &p.parallelism); err != nil {
		return argon2Params{}, nil, nil, errMalformedHash
	}

	salt, err := base64.RawStdEncoding.Strict().DecodeString(parts[4])
	if err != nil {
		return argon2Params{}, nil, nil, errMalformedHash
	}
	key, err := base64.RawStdEncoding.Strict().DecodeString(parts[5])
	if err != nil {
		return argon2Params{}, nil, nil, errMalformedHash
	}

	p.saltLength = uint32(len(salt))
	p.keyLength = uint32(len(key))
	return p, salt, key, nil
}

// DummyVerify performs a hash computation and discards the result.
//
// It is called when a login names an account that does not exist, so that the
// response takes about as long as a real failed verification. Without it, the
// difference between "unknown user" (fast) and "wrong password" (slow, because
// Argon2 ran) is measurable over the network, and the login endpoint becomes an
// account enumeration oracle despite returning an identical message.
func DummyVerify(password string) {
	salt := make([]byte, defaultParams.saltLength)
	_ = argon2.IDKey([]byte(password), salt, defaultParams.iterations,
		defaultParams.memoryKiB, defaultParams.parallelism, defaultParams.keyLength)
}
