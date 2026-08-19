package archive

import (
	"archive/zip"
	"bytes"
	"crypto/aes"
	"errors"
	"strings"
	"testing"
	"time"
)

var testModified = time.Date(2026, 8, 19, 14, 30, 44, 0, time.UTC)

// roundTrip writes an archive and reads it back with the same password.
func roundTrip(t *testing.T, password string, files map[string]string) *Reader {
	t.Helper()

	var buf bytes.Buffer
	w := NewWriter(&buf, password)
	// Sorted, so the archive is byte-stable for a given input and a failure
	// reproduces.
	for _, name := range sortedKeys(files) {
		if err := w.Add(name, testModified, strings.NewReader(files[name])); err != nil {
			t.Fatalf("Add %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()), password)
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	return r
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	for i := range keys {
		for j := i + 1; j < len(keys); j++ {
			if keys[j] < keys[i] {
				keys[i], keys[j] = keys[j], keys[i]
			}
		}
	}
	return keys
}

func TestPlainArchiveRoundTrips(t *testing.T) {
	files := map[string]string{
		"backup.json":         `{"format_version":1}`,
		"attachments/one.png": "\x89PNG\r\n\x1a\n not really a png",
	}
	r := roundTrip(t, "", files)

	if r.Encrypted() {
		t.Error("an archive written without a password must not claim to be encrypted")
	}
	for name, want := range files {
		got, err := r.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s came back as %q, want %q", name, got, want)
		}
	}
}

func TestEncryptedArchiveRoundTrips(t *testing.T) {
	// Long enough to span several AES blocks and end mid-block, which is where
	// a counter-mode implementation with an off-by-one shows itself.
	long := strings.Repeat("The quick brown fox. ", 500)
	files := map[string]string{
		"backup.json": `{"format_version":1,"customers":[]}`,
		"long.txt":    long,
		"empty.txt":   "",
	}
	r := roundTrip(t, "correct horse battery staple", files)

	if !r.Encrypted() {
		t.Fatal("an archive written with a password must report itself encrypted")
	}
	for name, want := range files {
		got, err := r.ReadFile(name)
		if err != nil {
			t.Fatalf("ReadFile %s: %v", name, err)
		}
		if string(got) != want {
			t.Errorf("%s came back with %d bytes, want %d", name, len(got), len(want))
		}
	}
}

// TestEncryptedArchiveHidesItsContents is the property the feature exists for.
// A backup that says "encrypted" and leaves the customer names legible in the
// file is worse than one that never claimed to.
func TestEncryptedArchiveHidesItsContents(t *testing.T) {
	secret := "Ackerman Consulting AB"

	var buf bytes.Buffer
	w := NewWriter(&buf, "a password")
	if err := w.Add("backup.json", testModified,
		strings.NewReader(`{"customer":"`+secret+`"}`)); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	if bytes.Contains(buf.Bytes(), []byte(secret)) {
		t.Error("the plaintext is still in the archive")
	}
	// The entry name is not encrypted by this format and must not be relied on
	// to be. Asserting it is present keeps that fact from being forgotten.
	if !bytes.Contains(buf.Bytes(), []byte("backup.json")) {
		t.Error("entry names are expected to be visible; something else changed")
	}
}

func TestWrongPasswordIsRefused(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, "the right one")
	if err := w.Add("backup.json", testModified, strings.NewReader("{}")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	// Several wrong passwords, because the two-byte verifier lets roughly one
	// in 65536 past it and a single attempt could get unlucky.
	var refused int
	for _, wrong := range []string{"nope", "the wrong one", "", "the right on", "THE RIGHT ONE"} {
		r, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()), wrong)
		if err != nil {
			t.Fatalf("NewReader: %v", err)
		}
		if _, err := r.ReadFile("backup.json"); err != nil {
			refused++
			if wrong == "" && !errors.Is(err, ErrPasswordRequired) {
				t.Errorf("no password at all should say so, got %v", err)
			}
			continue
		}
		t.Errorf("password %q opened the archive", wrong)
	}
	if refused == 0 {
		t.Fatal("no wrong password was refused at all")
	}
}

// TestTamperedArchiveIsRejected is what the authentication code buys over a
// CRC: a modified backup must not decrypt to plausible-looking rubbish.
func TestTamperedArchiveIsRejected(t *testing.T) {
	const password = "a password"
	var buf bytes.Buffer
	w := NewWriter(&buf, password)
	if err := w.Add("backup.json", testModified,
		strings.NewReader(strings.Repeat("payload ", 200))); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	raw := buf.Bytes()
	// Flip a bit in the middle, which is inside the ciphertext rather than in
	// the header or the trailing directory.
	tampered := bytes.Clone(raw)
	tampered[len(tampered)/2] ^= 0x01

	r, err := NewReader(bytes.NewReader(tampered), int64(len(tampered)), password)
	if err != nil {
		// Corrupting the middle can also break the zip structure itself, which
		// is a refusal too - just an earlier one.
		return
	}
	if _, err := r.ReadFile("backup.json"); err == nil {
		t.Error("a tampered archive decrypted without complaint")
	}
}

// TestEncryptedEntriesLookLikeAE2ToOtherTools checks the header fields other
// archivers key on. Getting these wrong produces a file this package can read
// and nothing else can, which is precisely the failure the format was chosen to
// avoid and which no round-trip test would catch.
func TestEncryptedEntriesLookLikeAE2ToOtherTools(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, "a password")
	if err := w.Add("backup.json", testModified, strings.NewReader("{}")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	zr, err := zip.NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("the archive is not a readable zip at all: %v", err)
	}
	if len(zr.File) != 1 {
		t.Fatalf("got %d entries, want 1", len(zr.File))
	}
	file := zr.File[0]

	if file.Method != 99 {
		t.Errorf("method = %d, want 99 (AE-x)", file.Method)
	}
	if file.CRC32 != 0 {
		t.Errorf("CRC32 = %d, want 0: AE-2 stores no CRC", file.CRC32)
	}
	if file.Flags&0x8 != 0 {
		t.Error("an encrypted entry must not use a data descriptor; many readers refuse one")
	}
	// Bit 0 is what tells any other archiver to decrypt before inflating.
	// Without it the archive is structurally perfect and every real tool fails
	// with a decompression error - which is how this was found, and why the
	// check is here rather than left to a round trip that cannot see it.
	if file.Flags&0x1 == 0 {
		t.Error("the encrypted flag (bit 0) is not set; no other tool will decrypt this")
	}
	if file.ReaderVersion == 0 {
		t.Error("ReaderVersion is unset, which some readers report as 'need version 0.0'")
	}
	if !file.Modified.Equal(testModified) {
		t.Errorf("modified = %v, want %v", file.Modified, testModified)
	}

	method, ok := parseAESExtra(file.Extra)
	if !ok {
		t.Fatal("the 0x9901 extra field is missing or malformed")
	}
	if method != zip.Deflate {
		t.Errorf("the extra field names method %d, want %d (deflate)", method, zip.Deflate)
	}
}

// TestPasswordRequiredIsReportedClearly: a person restoring an encrypted backup
// with no password needs to be told that, not handed a decompression error.
func TestPasswordRequiredIsReportedClearly(t *testing.T) {
	var buf bytes.Buffer
	w := NewWriter(&buf, "a password")
	if err := w.Add("backup.json", testModified, strings.NewReader("{}")); err != nil {
		t.Fatalf("Add: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	r, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "")
	if err != nil {
		t.Fatalf("NewReader: %v", err)
	}
	if !r.Encrypted() {
		t.Error("Encrypted must be answerable without the password")
	}
	if _, err := r.ReadFile("backup.json"); !errors.Is(err, ErrPasswordRequired) {
		t.Errorf("error = %v, want ErrPasswordRequired", err)
	}
}

func TestEachVisitsEveryEntry(t *testing.T) {
	files := map[string]string{"a.txt": "one", "b.txt": "two", "c.txt": "three"}
	r := roundTrip(t, "a password", files)

	seen := map[string]string{}
	if err := r.Each(func(name string, data []byte) error {
		seen[name] = string(data)
		return nil
	}); err != nil {
		t.Fatalf("Each: %v", err)
	}
	if len(seen) != len(files) {
		t.Fatalf("saw %d entries, want %d", len(seen), len(files))
	}
	for name, want := range files {
		if seen[name] != want {
			t.Errorf("%s = %q, want %q", name, seen[name], want)
		}
	}
}

// TestCounterModeIsLittleEndian pins the one detail that cannot be caught by a
// round trip: this package decrypting its own output would work equally well
// with the counter the wrong way round, and every other tool would then fail.
func TestCounterModeIsLittleEndian(t *testing.T) {
	// A known vector: the keystream for counter 1 under a fixed key. Derived
	// from the definition rather than copied from an implementation - block one
	// is AES(key, 01 00 00 ... 00) with the counter little-endian.
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}

	stream, err := newAESCTR(key)
	if err != nil {
		t.Fatalf("newAESCTR: %v", err)
	}
	got := make([]byte, 16)
	stream.XOR(got) // XOR against zeroes yields the keystream itself

	want := aesEncryptBlock(t, key, []byte{
		1, 0, 0, 0, 0, 0, 0, 0,
		0, 0, 0, 0, 0, 0, 0, 0,
	})
	if !bytes.Equal(got, want) {
		t.Errorf("first keystream block = %x,\n                     want %x\n"+
			"(a big-endian counter would start from 00..01 instead)", got, want)
	}
}

// aesEncryptBlock is one raw AES block, for the counter-mode vector above.
func aesEncryptBlock(t *testing.T, key, plaintext []byte) []byte {
	t.Helper()
	block, err := aes.NewCipher(key)
	if err != nil {
		t.Fatalf("aes.NewCipher: %v", err)
	}
	out := make([]byte, aes.BlockSize)
	block.Encrypt(out, plaintext)
	return out
}
