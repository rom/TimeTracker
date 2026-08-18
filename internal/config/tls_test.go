package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestKeyPermissionsAreChecked covers the guard against the most common quiet
// mistake with a TLS key: generating it with a careless umask.
func TestKeyPermissionsAreChecked(t *testing.T) {
	dir := t.TempDir()
	keyPath := filepath.Join(dir, "server.key")

	if err := os.WriteFile(keyPath, []byte("not a real key"), 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}
	if err := checkKeyPermissions(keyPath); err != nil {
		t.Errorf("a 0600 key was rejected: %v", err)
	}

	if err := os.Chmod(keyPath, 0o644); err != nil {
		t.Fatalf("chmod: %v", err)
	}
	err := checkKeyPermissions(keyPath)
	if err == nil {
		t.Fatal("a world-readable key was accepted")
	}
	// The message must say what to do about it.
	if !strings.Contains(err.Error(), "chmod 600") {
		t.Errorf("the error does not say how to fix it: %v", err)
	}

	// On Windows the reported bits are synthesised and say nothing about the
	// real ACL, so the check is skipped rather than producing a false alarm.
	original := isWindows
	isWindows = func() bool { return true }
	defer func() { isWindows = original }()
	if err := checkKeyPermissions(keyPath); err != nil {
		t.Errorf("the permission check should be skipped on Windows: %v", err)
	}
}

// TestServerRefusesPlainHTTPOnAPublicAddress is the guard that stops a login
// form being served in clear to a network.
func TestServerRefusesPlainHTTPOnAPublicAddress(t *testing.T) {
	base := Config{
		Mode: ModeServer, Addr: "0.0.0.0:8420",
		LogLevel: "info", LogFormat: "text", Hardening: "off",
	}

	if err := base.validate(); err == nil {
		t.Error("binding a public address with no TLS should be refused")
	}

	// Each of the three escapes must work.
	withTLS := base
	withTLS.TLSCertFile, withTLS.TLSKeyFile = "cert.pem", "key.pem"
	if err := withTLS.validate(); err != nil {
		t.Errorf("a configuration with its own certificate was refused: %v", err)
	}

	behindProxy := base
	behindProxy.ForceSecureCookies = true
	if err := behindProxy.validate(); err != nil {
		t.Errorf("a configuration behind a TLS proxy was refused: %v", err)
	}

	acknowledged := base
	acknowledged.AllowInsecure = true
	if err := acknowledged.validate(); err != nil {
		t.Errorf("an explicit --allow-insecure was refused: %v", err)
	}

	// Loopback needs no TLS: nothing else can reach the socket.
	loopback := base
	loopback.Addr = "127.0.0.1:8420"
	if err := loopback.validate(); err != nil {
		t.Errorf("a loopback bind was refused: %v", err)
	}
}

// TestHalfConfiguredTLSIsRejected: falling back to plain HTTP because only one
// of the two files was given would be a silent downgrade.
func TestHalfConfiguredTLSIsRejected(t *testing.T) {
	cfg := Config{
		Mode: ModeLocal, Addr: "127.0.0.1:8420",
		LogLevel: "info", LogFormat: "text", Hardening: "off",
		TLSCertFile: "cert.pem",
	}
	if err := cfg.validate(); err == nil {
		t.Error("a certificate without a key should be refused")
	}

	cfg.TLSCertFile = ""
	cfg.TLSKeyFile = "key.pem"
	if err := cfg.validate(); err == nil {
		t.Error("a key without a certificate should be refused")
	}
}

// TestLocalModeMustStayOnLoopback: local mode has no authentication, so binding
// it to a network would publish a timesheet with no login.
func TestLocalModeMustStayOnLoopback(t *testing.T) {
	cfg := Config{
		Mode: ModeLocal, Addr: "0.0.0.0:8420",
		LogLevel: "info", LogFormat: "text", Hardening: "off",
	}
	if err := cfg.validate(); err == nil {
		t.Error("local mode on a public address should be refused")
	}
}

// TestTLSConfigIsHardened pins the negotiation policy: no TLS 1.0/1.1, and no
// cipher suite without forward secrecy or without AEAD.
func TestTLSConfigIsHardened(t *testing.T) {
	dir := t.TempDir()
	certPath := filepath.Join(dir, "cert.pem")
	keyPath := filepath.Join(dir, "key.pem")

	// A throwaway certificate, generated in-process so the test needs no
	// fixtures and no openssl.
	certPEM, keyPEM := generateTestCertificate(t)
	if err := os.WriteFile(certPath, certPEM, 0o644); err != nil {
		t.Fatalf("write cert: %v", err)
	}
	if err := os.WriteFile(keyPath, keyPEM, 0o600); err != nil {
		t.Fatalf("write key: %v", err)
	}

	cfg := Config{TLSCertFile: certPath, TLSKeyFile: keyPath}
	tlsConfig, err := cfg.BuildTLSConfig()
	if err != nil {
		t.Fatalf("build TLS config: %v", err)
	}

	if tlsConfig.MinVersion < 0x0303 { // tls.VersionTLS12
		t.Errorf("MinVersion allows TLS below 1.2: %#04x", tlsConfig.MinVersion)
	}

	// Every configured 1.2 suite must be ECDHE (forward secrecy) and AEAD.
	// CBC suites carry padding-oracle attacks; RSA key exchange has no forward
	// secrecy, so a stolen key decrypts captured traffic retroactively.
	for _, suite := range tlsConfig.CipherSuites {
		name := cipherName(suite)
		if strings.Contains(name, "_CBC_") {
			t.Errorf("a CBC cipher suite is configured: %s", name)
		}
		if strings.HasPrefix(name, "TLS_RSA_") {
			t.Errorf("a suite without forward secrecy is configured: %s", name)
		}
	}
}
