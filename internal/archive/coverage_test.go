package archive

import (
	"archive/zip"
	"bytes"
	"errors"
	"strings"
	"testing"
	"time"
)

// Reading archives back, and the failures a damaged or wrongly-keyed one
// produces.
//
// The format's job is that a backup written today opens in five years, in this
// program and in whatever archiver somebody has to hand. Most of that is
// asserted elsewhere by writing and reading; what is here is the rest of the
// reader's surface and the ways a read can fail.

// written is an archive holding the given entries.
func written(t *testing.T, password string, entries map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := NewWriter(&buf, password)
	// A map iterates in no order; the names here are chosen so order does not
	// matter to any assertion that uses them.
	for name, body := range entries {
		if err := w.Add(name, time.Date(2026, 3, 16, 9, 0, 0, 0, time.UTC),
			strings.NewReader(body)); err != nil {
			t.Fatalf("add %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

// TestAnArchiveListsWhatIsInIt.
//
// The reader's inventory, which is how a restore finds the document and the
// attachments without guessing at names.
func TestAnArchiveListsWhatIsInIt(t *testing.T) {
	data := written(t, "", map[string]string{
		"backup.json":         `{"format_version":1}`,
		"attachments/one.txt": "the first receipt",
		"attachments/two.txt": "the second receipt",
		"README.txt":          "what this file is",
	})

	reader, err := NewReader(bytes.NewReader(data), int64(len(data)), "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	names := reader.Names()
	if len(names) != 4 {
		t.Fatalf("the archive lists %v, want four entries", names)
	}
	found := map[string]bool{}
	for _, name := range names {
		found[name] = true
	}
	for _, want := range []string{"backup.json", "attachments/one.txt", "README.txt"} {
		if !found[want] {
			t.Errorf("%q is not listed", want)
		}
	}

	// Reading one by name.
	body, err := reader.ReadFile("attachments/two.txt")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if string(body) != "the second receipt" {
		t.Errorf("the entry reads %q", body)
	}

	// A name that is not there is an error naming it, rather than empty bytes
	// that a caller would read as an empty file.
	_, err = reader.ReadFile("attachments/three.txt")
	if err == nil {
		t.Fatal("reading an entry that is not in the archive succeeded")
	}
	if !strings.Contains(err.Error(), "three.txt") {
		t.Errorf("the error reads %q, which does not name what was missing", err)
	}

	// Each visits every entry once.
	visited := map[string]int{}
	if err := reader.Each(func(name string, data []byte) error {
		visited[name]++
		return nil
	}); err != nil {
		t.Fatalf("each: %v", err)
	}
	if len(visited) != 4 {
		t.Errorf("Each visited %d entries, want 4", len(visited))
	}
	for name, times := range visited {
		if times != 1 {
			t.Errorf("Each visited %q %d times", name, times)
		}
	}

	// And a callback's error stops the walk and is returned, so a restore that
	// cannot go on does not silently finish.
	wanted := errors.New("stop here")
	if err := reader.Each(func(string, []byte) error { return wanted }); !errors.Is(err, wanted) {
		t.Errorf("Each returned %v, want the callback's own error", err)
	}
}

// TestAnEncryptedArchiveSaysSoAndNeedsItsPassword.
//
// Encryption is per-entry in the zip format, so "is this archive encrypted" is
// a question about its entries. A restore asks before it tries, because
// "wrong password" and "corrupt file" are different things to tell somebody.
func TestAnEncryptedArchiveSaysSoAndNeedsItsPassword(t *testing.T) {
	const password = "a-long-enough-passphrase"
	data := written(t, password, map[string]string{
		"backup.json":         `{"format_version":1}`,
		"attachments/one.txt": "the first receipt",
	})

	plain := written(t, "", map[string]string{"backup.json": `{"format_version":1}`})
	plainReader, err := NewReader(bytes.NewReader(plain), int64(len(plain)), "")
	if err != nil {
		t.Fatalf("open the plain archive: %v", err)
	}
	if plainReader.Encrypted() {
		t.Error("a plain archive reports itself as encrypted")
	}

	withKey, err := NewReader(bytes.NewReader(data), int64(len(data)), password)
	if err != nil {
		t.Fatalf("open with the password: %v", err)
	}
	if !withKey.Encrypted() {
		t.Error("an encrypted archive does not report itself as encrypted")
	}
	body, err := withKey.ReadFile("attachments/one.txt")
	if err != nil {
		t.Fatalf("read with the password: %v", err)
	}
	if string(body) != "the first receipt" {
		t.Errorf("the decrypted entry reads %q", body)
	}

	// The wrong password, and none at all. Both refuse rather than returning
	// plausible rubbish, which is what the authentication tag is for.
	for name, attempt := range map[string]string{
		"the wrong password": "not-the-passphrase",
		"no password":        "",
	} {
		t.Run(name, func(t *testing.T) {
			reader, err := NewReader(bytes.NewReader(data), int64(len(data)), attempt)
			if err != nil {
				return // refusing at open is an acceptable answer
			}
			// It can still say it is encrypted without being able to read it:
			// that is what lets a restore ask for the password.
			if !reader.Encrypted() {
				t.Error("the archive no longer reports itself as encrypted")
			}
			if _, err := reader.ReadFile("attachments/one.txt"); err == nil {
				t.Errorf("an entry was read with %s", name)
			}
		})
	}

	// A writer declares which it is, so the README it writes can say so.
	if !NewWriter(nil, password).Encrypted() {
		t.Error("a writer given a password does not report itself as encrypting")
	}
	if NewWriter(nil, "").Encrypted() {
		t.Error("a writer given no password reports itself as encrypting")
	}
}

// TestWhatIsNotAnArchiveAtAll.
//
// Somebody picks the wrong file. The failure has to come from opening it
// rather than from the first read, so a restore can say "this is not an
// archive" before it has half-applied anything.
func TestWhatIsNotAnArchiveAtAll(t *testing.T) {
	cases := map[string]string{
		"empty":            "",
		"a sentence":       "this is not an archive",
		"a truncated zip":  "PK\x03\x04 and then nothing",
		"only a signature": "PK\x05\x06",
	}
	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := NewReader(strings.NewReader(content),
				int64(len(content)), ""); err == nil {
				t.Errorf("%s was opened as an archive", name)
			}
		})
	}
}

// TestADirectoryEntryIsNotAFile.
//
// Some archivers write explicit directory entries. Handing one to a restore as
// a file would produce an empty attachment under a folder's name, so the walk
// skips them.
func TestADirectoryEntryIsNotAFile(t *testing.T) {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	if _, err := zw.Create("attachments/"); err != nil {
		t.Fatalf("create a directory entry: %v", err)
	}
	entry, err := zw.Create("attachments/one.txt")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := entry.Write([]byte("a receipt")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reader, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	var files []string
	if err := reader.Each(func(name string, data []byte) error {
		files = append(files, name)
		return nil
	}); err != nil {
		t.Fatalf("each: %v", err)
	}
	if len(files) != 1 || files[0] != "attachments/one.txt" {
		t.Errorf("the walk visited %v, want only the file", files)
	}

	// The directory is still listed, because Names is an inventory of the
	// archive rather than of its files.
	if len(reader.Names()) != 2 {
		t.Errorf("the inventory lists %v, want both entries", reader.Names())
	}
}

// TestATimestampSurvivesAsAZipTimestamp.
//
// The MS-DOS time fields a zip carries have two-second resolution and a 1980
// epoch. An archive whose entries are dated 1980 reads as broken to anybody
// looking at it in a file manager, so the conversion is worth pinning.
func TestATimestampSurvivesAsAZipTimestamp(t *testing.T) {
	when := time.Date(2026, 3, 16, 9, 30, 44, 0, time.UTC)
	data := written(t, "", map[string]string{"backup.json": "{}"})

	reader, err := NewReader(bytes.NewReader(data), int64(len(data)), "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if len(reader.zr.File) == 0 {
		t.Fatal("no entries")
	}
	stamped := reader.zr.File[0].Modified
	if stamped.Year() != 2026 || stamped.Month() != time.March {
		t.Errorf("the entry is dated %s, want the date it was written", stamped)
	}

	// A date before the format's epoch has nothing to map onto; what matters is
	// that it does not panic or produce a date in the far future.
	var buf bytes.Buffer
	w := NewWriter(&buf, "")
	if err := w.Add("old.txt", time.Date(1970, 1, 1, 0, 0, 0, 0, time.UTC),
		strings.NewReader("x")); err != nil {
		t.Fatalf("add an entry dated before the epoch: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	old, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if got := old.zr.File[0].Modified; got.Year() > when.Year()+50 {
		t.Errorf("an entry dated 1970 came back as %s", got)
	}
}

// TestTheEncryptionFieldIsWalkedRatherThanAssumed.
//
// An entry's extra area is a sequence of length-prefixed records, any of which
// may be something this package has never heard of. Assuming the first one is
// the encryption field would make an archive unreadable the moment another
// tool added a timestamp or a Unix-attributes record ahead of it - which is
// common - so the area is walked.
func TestTheEncryptionFieldIsWalkedRatherThanAssumed(t *testing.T) {
	// A record this package does not know, then the real one.
	unknown := extraRecord(0x5455, []byte{1, 2, 3, 4, 5}) // extended timestamp
	actual := aesRecord(strength256, zip.Deflate)

	found, ok := parseAESExtra(append(unknown, actual...))
	if !ok {
		t.Fatal("the encryption field was not found behind a record this package " +
			"does not know; an archiver that adds a timestamp first would make " +
			"every entry unreadable")
	}
	if found != zip.Deflate {
		t.Errorf("the real compression method reads as %d, want %d",
			found, zip.Deflate)
	}

	// And the order does not matter.
	if _, ok := parseAESExtra(append(actual, unknown...)); !ok {
		t.Error("the encryption field was not found when it comes first")
	}

	refused := map[string][]byte{
		"nothing at all":                          nil,
		"a truncated record":                      {0x01, 0x99},
		"a record whose length runs past the end": {0x01, 0x99, 0xff, 0xff, 0x01},
		"only records of other kinds":             unknown,
		"a field too short to hold the fields":    extraRecord(aesExtraID, []byte{1, 2}),
		"another vendor's field":                  aesRecordVendor(0x4142, strength256, zip.Deflate),
		// 128- and 192-bit archives are legal AE-2 that this package does not
		// write. Saying so beats decrypting with the wrong key length and
		// reporting corruption.
		"a 128-bit archive": aesRecord(0x01, zip.Deflate),
		"a 192-bit archive": aesRecord(0x02, zip.Deflate),
	}
	for name, extra := range refused {
		t.Run(name, func(t *testing.T) {
			if _, ok := parseAESExtra(extra); ok {
				t.Errorf("%s was read as an encryption field", name)
			}
		})
	}
}

// extraRecord builds one length-prefixed record of an entry's extra area.
func extraRecord(id uint16, payload []byte) []byte {
	out := make([]byte, 4+len(payload))
	out[0] = byte(id)
	out[1] = byte(id >> 8)
	out[2] = byte(len(payload))
	out[3] = byte(len(payload) >> 8)
	copy(out[4:], payload)
	return out
}

// aesRecord builds an AE-2 encryption field at a given strength, declaring a
// real compression method.
func aesRecord(strength byte, method uint16) []byte {
	return aesRecordVendor(aesVendor, strength, method)
}

func aesRecordVendor(vendor uint16, strength byte, method uint16) []byte {
	payload := []byte{
		2, 0, // AE-2
		byte(vendor), byte(vendor >> 8),
		strength,
		byte(method), byte(method >> 8),
	}
	return extraRecord(aesExtraID, payload)
}

// TestAnEntryLargerThanTheLimitIsRefused.
//
// The per-entry cap is what stops one archive making the process hold an
// arbitrary amount of memory - a restore reads an entry whole, because a zip's
// central directory is at the end and an attachment has to be hashed before it
// is trusted. The limit has to be enforced while reading rather than after.
func TestAnEntryLargerThanTheLimitIsRefused(t *testing.T) {
	const limit = 64

	if _, err := readAll(strings.NewReader(strings.Repeat("x", limit+1)), limit); err == nil {
		t.Error("an entry one byte over the limit was read")
	}

	// Exactly at the limit is allowed: the boundary belongs to the allowed side.
	body, err := readAll(strings.NewReader(strings.Repeat("x", limit)), limit)
	if err != nil {
		t.Fatalf("an entry of exactly the limit was refused: %v", err)
	}
	if len(body) != limit {
		t.Errorf("read %d bytes, want %d", len(body), limit)
	}

	// A reader that fails part way through reports its own error rather than
	// a short read that looks like a small file.
	wanted := errors.New("the disk went away")
	if _, err := readAll(&failingReader{after: 8, err: wanted}, limit); !errors.Is(err, wanted) {
		t.Errorf("readAll returned %v, want the reader's own error", err)
	}
}

// failingReader yields a fixed number of bytes and then fails, like a disk or
// a socket going away part way through an entry.
type failingReader struct {
	after int
	err   error
}

func (f *failingReader) Read(p []byte) (int, error) {
	if f.after <= 0 {
		return 0, f.err
	}
	n := len(p)
	if n > f.after {
		n = f.after
	}
	for i := 0; i < n; i++ {
		p[i] = 'x'
	}
	f.after -= n
	return n, nil
}

// TestAnEncryptedEntryMissingItsEncryptionField.
//
// An entry declaring compression method 99 says "I am encrypted"; the extra
// field says how. One without the other is a file no tool can read, and the
// error has to say that rather than reporting corruption - which would send
// somebody looking for a damaged disk.
func TestAnEncryptedEntryMissingItsEncryptionField(t *testing.T) {
	// Build an archive by hand: method 99, no AES extra field.
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entry, err := zw.CreateRaw(&zip.FileHeader{
		Name:   "backup.json",
		Method: 99,
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := entry.Write([]byte("not really encrypted")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}

	reader, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()),
		"a-long-enough-passphrase")
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	_, err = reader.ReadFile("backup.json")
	if err == nil {
		t.Fatal("an entry claiming encryption with no encryption field was read")
	}
	if !strings.Contains(err.Error(), "does not say how") {
		t.Errorf("the error reads %q; it should say the entry does not say how "+
			"it is encrypted rather than report corruption", err)
	}

	// With no password at all the answer is that one is needed, which is a
	// different thing to tell somebody.
	noKey, err := NewReader(bytes.NewReader(buf.Bytes()), int64(buf.Len()), "")
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, err := noKey.ReadFile("backup.json"); !errors.Is(err, ErrPasswordRequired) {
		t.Errorf("reading an encrypted entry with no password = %v, want a "+
			"password-required error", err)
	}
}
