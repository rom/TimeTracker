package config

import (
	"crypto/tls"
	"fmt"
	"os"
)

// TLS configuration.
//
// The application can terminate TLS itself, which is what a small team running a
// single binary wants, or sit behind a reverse proxy that does it. Both are
// supported; what is not supported is serving a login form over plain HTTP to a
// network, because that puts every password on the wire.

// TLSConfigured reports whether the certificate and key are both set.
func (c Config) TLSConfigured() bool {
	return c.TLSCertFile != "" && c.TLSKeyFile != ""
}

// BuildTLSConfig loads the certificate and returns a hardened tls.Config.
//
// The settings are deliberately opinionated rather than left to Go's defaults:
//
//   - TLS 1.2 is the floor. Everything below it has known weaknesses, and no
//     client that matters still needs 1.0 or 1.1.
//   - The 1.2 cipher suites are restricted to the AEAD constructions with
//     forward secrecy. CBC suites are vulnerable to a family of padding-oracle
//     attacks and RSA key exchange has no forward secrecy, so a stolen key would
//     decrypt yesterday's traffic as well as tomorrow's.
//   - TLS 1.3 suites are not configurable in Go, by design, and are all sound.
func (c Config) BuildTLSConfig() (*tls.Config, error) {
	certificate, err := tls.LoadX509KeyPair(c.TLSCertFile, c.TLSKeyFile)
	if err != nil {
		return nil, fmt.Errorf("load TLS certificate and key: %w", err)
	}

	// The private key must not be readable by other users on the machine. This
	// is checked rather than assumed: a key generated with a careless umask is a
	// common and quiet mistake.
	if err := checkKeyPermissions(c.TLSKeyFile); err != nil {
		return nil, err
	}

	return &tls.Config{
		Certificates: []tls.Certificate{certificate},
		MinVersion:   tls.VersionTLS12,
		CipherSuites: []uint16{
			// TLS 1.3 (listed for documentation; Go selects these itself)
			tls.TLS_AES_128_GCM_SHA256,
			tls.TLS_AES_256_GCM_SHA384,
			tls.TLS_CHACHA20_POLY1305_SHA256,
			// TLS 1.2, forward secrecy and AEAD only
			tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305,
			tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
			tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
			tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305,
		},
		CurvePreferences: []tls.CurveID{tls.X25519, tls.CurveP256},
	}, nil
}

// checkKeyPermissions refuses a private key that others can read.
//
// Not enforced on Windows, where the POSIX permission bits Go reports are
// synthesised and say nothing useful; there the ACL is what matters and is left
// to the operator, documented in scripts/README.md.
func checkKeyPermissions(path string) error {
	info, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("read TLS key %s: %w", path, err)
	}
	if isWindows() {
		return nil
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		return fmt.Errorf(
			"TLS key %s is readable by other users (mode %04o); run: chmod 600 %s",
			path, mode, path)
	}
	return nil
}
