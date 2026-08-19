package blob

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pngBytes returns a minimal but genuine PNG, so the type sniffer sees a real
// image rather than something that merely claims to be one.
func pngBytes() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', '\r', '\n', 0x1a, '\n',
		0, 0, 0, 13, 'I', 'H', 'D', 'R',
		0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0,
		0x90, 0x77, 0x53, 0xde,
		0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xae, 0x42, 0x60, 0x82,
	}
}

func newStore(t *testing.T) *Store {
	t.Helper()
	store, err := Open(filepath.Join(t.TempDir(), "blobs"))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	return store
}

// TestPutAndGetRoundTrip.
func TestPutAndGetRoundTrip(t *testing.T) {
	store := newStore(t)
	content := pngBytes()

	hash, size, mime, err := store.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	if size != int64(len(content)) {
		t.Errorf("size = %d, want %d", size, len(content))
	}
	if mime != "image/png" {
		t.Errorf("mime = %q, want image/png", mime)
	}
	if len(hash) != 64 {
		t.Errorf("hash is not a SHA-256: %q", hash)
	}

	reader, err := store.Get(hash)
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	defer func() { _ = reader.Close() }()

	got := make([]byte, len(content))
	if _, err := reader.Read(got); err != nil {
		t.Fatalf("read: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Error("the content came back different from what went in")
	}
}

// TestDeduplication: content addressing means an identical file attached twice
// occupies one file on disk.
func TestDeduplication(t *testing.T) {
	store := newStore(t)
	content := pngBytes()

	first, _, _, err := store.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("first put: %v", err)
	}
	second, _, _, err := store.Put(bytes.NewReader(content))
	if err != nil {
		t.Fatalf("second put: %v", err)
	}
	if first != second {
		t.Fatal("identical content produced different hashes")
	}

	count := 0
	_ = filepath.Walk(store.Root(), func(path string, info os.FileInfo, err error) error {
		if err == nil && !info.IsDir() {
			count++
		}
		return nil
	})
	if count != 1 {
		t.Errorf("identical content was stored %d times", count)
	}
}

// TestSizeLimit: an unbounded upload is a way to fill someone's disk.
func TestSizeLimit(t *testing.T) {
	store := newStore(t)
	store.MaxBytes = 100

	_, _, _, err := store.Put(bytes.NewReader(make([]byte, 500)))
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("an oversized upload was accepted: %v", err)
	}
}

// TestUnacceptableTypesAreRefused checks the allow-list still holds.
func TestUnacceptableTypesAreRefused(t *testing.T) {
	store := newStore(t)

	// An executable, which no sniff should ever call acceptable.
	elf := append([]byte{0x7f, 'E', 'L', 'F', 2, 1, 1}, make([]byte, 200)...)
	if _, _, _, err := store.Put(bytes.NewReader(elf)); !errors.Is(err, ErrUnacceptableType) {
		t.Errorf("an executable was accepted: %v", err)
	}

	// An empty file is not a file.
	if _, _, _, err := store.Put(bytes.NewReader(nil)); err == nil {
		t.Error("an empty upload was accepted")
	}
}

// TestSVGIsStoredAsAnImageAndLabelledAsOne.
//
// SVG was refused outright until previews arrived, on the grounds that it is
// script-capable. It is stored now, and the reasoning that replaced the refusal
// is in docs/adr/0031-attachment-previews.md: an SVG is never served as a
// document. The preview route puts it inside an <img>, where no browser runs
// script, behind a response policy that forbids scripts and every subresource
// anyway, and the download route keeps Content-Disposition: attachment.
//
// What this test guards is the half that lives here: the store must label the
// file image/svg+xml rather than the text/xml or text/plain that Go's sniffer
// would return. Every downstream decision keys on that type, and a hostile SVG
// stored as "text/plain" would slip past all of them.
func TestSVGIsStoredAsAnImageAndLabelledAsOne(t *testing.T) {
	store := newStore(t)

	hostile := []byte(`<?xml version="1.0"?><svg xmlns="http://www.w3.org/2000/svg">` +
		`<script>alert(1)</script></svg>`)
	_, _, mime, err := store.PutNamed(bytes.NewReader(hostile), "diagram.svg")
	if err != nil {
		t.Fatalf("an SVG should be storable now: %v", err)
	}
	if mime != "image/svg+xml" {
		t.Errorf("mime = %q, want image/svg+xml; every later decision keys on this", mime)
	}

	// Without a declaration, and with leading whitespace and a comment.
	bare := []byte("\n  <!-- a drawing -->\n<svg xmlns=\"http://www.w3.org/2000/svg\"/>")
	if _, _, mime, err = store.PutNamed(bytes.NewReader(bare), "bare.svg"); err != nil {
		t.Fatalf("a bare SVG should be storable: %v", err)
	}
	if mime != "image/svg+xml" {
		t.Errorf("mime = %q for an SVG with no XML declaration, want image/svg+xml", mime)
	}
}

// TestFormatsTheStandardSnifferMisses.
//
// http.DetectContentType implements the WHATWG list, which is aimed at what a
// browser will render. Three accepted types are not on it, and every one was
// unreachable until this sniff existed: the file came back as
// application/octet-stream, which the allow-list explicitly refuses. A list
// that accepts a type nothing can produce is a list that lies.
func TestFormatsTheStandardSnifferMisses(t *testing.T) {
	store := newStore(t)

	// A classic TIFF header, both byte orders. The rest is padding: the sniff
	// looks at the signature and the store does not decode anything.
	littleEndian := append([]byte("II\x2a\x00"), make([]byte, 200)...)
	bigEndian := append([]byte("MM\x00\x2a"), make([]byte, 200)...)
	for name, content := range map[string][]byte{
		"scan.tiff": littleEndian,
		"scan.tif":  bigEndian,
	} {
		_, _, mime, err := store.PutNamed(bytes.NewReader(content), name)
		if err != nil {
			t.Errorf("%s was refused: %v", name, err)
			continue
		}
		if mime != "image/tiff" {
			t.Errorf("%s stored as %q, want image/tiff", name, mime)
		}
	}

	// BigTIFF is deliberately not matched: nothing here can decode one, and
	// accepting it would store a file whose preview could only ever fail.
	bigTIFF := append([]byte("II\x2b\x00"), make([]byte, 200)...)
	if _, _, _, err := store.PutNamed(bytes.NewReader(bigTIFF), "huge.tif"); err == nil {
		t.Error("BigTIFF was accepted, but nothing in this application can read one")
	}

	// A pre-2007 Word document: an OLE compound file.
	ole := append([]byte("\xd0\xcf\x11\xe0\xa1\xb1\x1a\xe1"), make([]byte, 200)...)
	_, _, mime, err := store.PutNamed(bytes.NewReader(ole), "old.doc")
	if err != nil {
		t.Errorf("a .doc was refused: %v", err)
	} else if mime != "application/x-ole-storage" {
		t.Errorf(".doc stored as %q", mime)
	}
}

// TestSVGDetectionIsNarrow: the sniff must not promote a text file to an image
// because it happens to mention an SVG tag.
func TestSVGDetectionIsNarrow(t *testing.T) {
	for _, content := range []string{
		"a note that mentions <svg> in passing",
		"<html><body><svg></svg></body></html>",
		"<svgx foo=\"bar\">",
		"<?xml version=\"1.0\"?><rss><item>&lt;svg&gt;</item></rss>",
		"",
	} {
		if isSVG([]byte(content)) {
			t.Errorf("%q was taken for an SVG document", content)
		}
	}
	for _, content := range []string{
		`<svg xmlns="http://www.w3.org/2000/svg"/>`,
		`<?xml version="1.0"?><svg/>`,
		"<svg>",
		"  \n<!DOCTYPE svg><svg width=\"10\">",
	} {
		if !isSVG([]byte(content)) {
			t.Errorf("%q is an SVG document and was not recognised", content)
		}
	}
}

// TestExtensionMustAgreeWithContent is the check behind the claim in
// docs/SECURITY.md: a name that says PNG while the bytes say shell script is
// refused at upload rather than left for every consumer to re-sniff.
func TestExtensionMustAgreeWithContent(t *testing.T) {
	store := newStore(t)

	script := []byte("#!/bin/sh\necho hello\n")
	_, _, _, err := store.PutNamed(bytes.NewReader(script), "innocent.png")
	if !errors.Is(err, ErrTypeMismatch) {
		t.Errorf("a script named .png was accepted: %v", err)
	}

	// The same bytes under an honest name are fine: plain text is acceptable
	// content, and it is served as a download with nosniff.
	if _, _, _, err := store.PutNamed(bytes.NewReader(script), "notes.txt"); err != nil {
		t.Errorf("plain text under a .txt name was refused: %v", err)
	}

	// A genuine PNG under a PNG name.
	if _, _, _, err := store.PutNamed(bytes.NewReader(pngBytes()), "receipt.png"); err != nil {
		t.Errorf("a genuine PNG was refused: %v", err)
	}

	// An unpoliced extension is not evidence of anything.
	if _, _, _, err := store.PutNamed(bytes.NewReader(pngBytes()), "receipt.unknown"); err != nil {
		t.Errorf("an unknown extension was treated as a mismatch: %v", err)
	}
}

// TestHashesAreValidatedBeforeBecomingPaths: the hash comes from the database,
// but validating it here means a corrupted or crafted value can never traverse.
func TestHashesAreValidatedBeforeBecomingPaths(t *testing.T) {
	store := newStore(t)

	for _, hostile := range []string{
		"../../../etc/passwd",
		"..",
		"",
		strings.Repeat("z", 64), // right length, not hex
		"ABCDEF0123456789abcdef0123456789abcdef0123456789abcdef0123456789", // upper case
	} {
		if _, err := store.Get(hostile); err == nil {
			t.Errorf("Get accepted a hostile hash %q", hostile)
		}
		if err := store.Remove(hostile); err == nil {
			t.Errorf("Remove accepted a hostile hash %q", hostile)
		}
	}
}

// TestSweepRemovesOnlyUnreferencedBlobs.
func TestSweep(t *testing.T) {
	store := newStore(t)

	kept, _, _, err := store.Put(bytes.NewReader(pngBytes()))
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	orphan, _, _, err := store.Put(bytes.NewReader([]byte("some plain text content here")))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	removed, err := store.Sweep(map[string]bool{kept: true})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed != 1 {
		t.Errorf("swept %d blobs, want 1", removed)
	}

	if _, err := store.Get(kept); err != nil {
		t.Error("the sweep removed a referenced blob")
	}
	if _, err := store.Get(orphan); err == nil {
		t.Error("the sweep left an unreferenced blob")
	}
}

// TestBlobPermissions: attachments are receipts and screenshots from somebody's
// work, no less sensitive than the database beside them.
func TestBlobPermissions(t *testing.T) {
	store := newStore(t)

	hash, _, _, err := store.Put(bytes.NewReader(pngBytes()))
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	info, err := os.Stat(filepath.Join(store.Root(), hash[0:2], hash[2:4], hash))
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("a blob is readable by other users: mode %04o", mode)
	}

	rootInfo, err := os.Stat(store.Root())
	if err != nil {
		t.Fatalf("stat root: %v", err)
	}
	if mode := rootInfo.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("the blob directory is readable by other users: mode %04o", mode)
	}
}
