package blob

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The store's accessors and its sweep.
//
// A blob is a receipt behind a billed expense. The hash is both the integrity
// check and the storage path, which is what removes traversal and
// case-collision problems in one move - and which makes the hash validation
// load-bearing rather than defensive: a hash that reached the path builder
// unchecked would be a path.

// TestAHashIsCheckedBeforeItBecomesAPath.
//
// Every accessor validates first. Without that, "../../etc/passwd" is a
// filename, and a 64-character string of anything is a reader's choice of file.
func TestAHashIsCheckedBeforeItBecomesAPath(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	bad := []string{
		"",
		"../../../etc/passwd",
		strings.Repeat("a", 63),
		strings.Repeat("a", 65),
		strings.Repeat("A", 64), // upper case is not the stored form
		strings.Repeat("g", 64), // not hexadecimal
		strings.Repeat("a", 32) + "/" + strings.Repeat("b", 31),
		strings.Repeat("a", 60) + "/../x",
	}

	for _, hash := range bad {
		name := hash
		if name == "" {
			name = "the empty string"
		}
		if len(name) > 20 {
			name = name[:20] + "…"
		}
		t.Run(name, func(t *testing.T) {
			if _, err := store.Size(hash); err == nil {
				t.Errorf("Size(%q) was accepted", hash)
			}
			if err := store.Remove(hash); err == nil {
				t.Errorf("Remove(%q) was accepted", hash)
			}
			if _, err := store.Get(hash); err == nil {
				t.Errorf("Get(%q) was accepted", hash)
			}
		})
	}
}

// TestSizeAndRemove.
//
// Size is what a listing shows, and Remove is what the sweep calls. Removing
// something that is not there is not an error: two sweeps, or a sweep racing
// a delete, must not fail.
func TestSizeAndRemove(t *testing.T) {
	store, err := Open(t.TempDir())
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	const content = "a receipt, of some length"
	hash, _, _, err := store.PutNamed(strings.NewReader(content), "receipt.txt")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	size, err := store.Size(hash)
	if err != nil {
		t.Fatalf("size: %v", err)
	}
	if size != int64(len(content)) {
		t.Errorf("the stored file is %d bytes, want %d", size, len(content))
	}

	if err := store.Remove(hash); err != nil {
		t.Fatalf("remove: %v", err)
	}
	if _, err := store.Size(hash); err == nil {
		t.Error("the removed file still has a size")
	}

	// Again, which is what a second sweep does.
	if err := store.Remove(hash); err != nil {
		t.Errorf("removing twice = %v, want a no-op", err)
	}

	// A well-formed hash that was never stored is not found rather than an
	// error about the hash.
	if _, err := store.Size(strings.Repeat("0", 64)); err == nil {
		t.Error("a hash that was never stored has a size")
	}
}

// TestTheSweepKeepsWhatIsReferenced.
//
// Deletion is a sweep rather than something done on the request path, so
// removing an attachment stays a single fast database write. The sweep is
// therefore the only thing standing between a deleted receipt and a disk that
// fills up - and the only thing standing between a *referenced* receipt and
// its deletion, which is the direction that loses evidence.
func TestTheSweepKeepsWhatIsReferenced(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	keep, _, _, err := store.PutNamed(strings.NewReader("the one still in use"), "keep.txt")
	if err != nil {
		t.Fatalf("put: %v", err)
	}
	drop, _, _, err := store.PutNamed(strings.NewReader("the one nothing points at"), "drop.txt")
	if err != nil {
		t.Fatalf("put: %v", err)
	}

	removed, err := store.Sweep(map[string]bool{keep: true})
	if err != nil {
		t.Fatalf("sweep: %v", err)
	}
	if removed != 1 {
		t.Errorf("the sweep removed %d files, want the 1 nothing references", removed)
	}
	if _, err := store.Size(keep); err != nil {
		t.Errorf("the sweep removed a referenced file: %v", err)
	}
	if _, err := store.Size(drop); err == nil {
		t.Error("the sweep left an unreferenced file behind")
	}

	// A sweep with nothing referenced empties the store, and one over an empty
	// store is a no-op.
	if removed, err := store.Sweep(map[string]bool{}); err != nil {
		t.Fatalf("sweep everything: %v", err)
	} else if removed != 1 {
		t.Errorf("the second sweep removed %d, want the 1 that was left", removed)
	}
	if removed, err := store.Sweep(map[string]bool{}); err != nil {
		t.Fatalf("sweep an empty store: %v", err)
	} else if removed != 0 {
		t.Errorf("sweeping an empty store removed %d files", removed)
	}
}

// TestTheSweepTidiesAbandonedUploads.
//
// A request that died mid-upload leaves a temporary file. Old ones are
// abandoned and are swept; a fresh one belongs to an upload still in progress
// and must not be swept out from under itself - which would turn a slow upload
// on a bad connection into a corrupt one.
func TestTheSweepTidiesAbandonedUploads(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("open: %v", err)
	}

	fresh := filepath.Join(root, ".upload-inprogress")
	old := filepath.Join(root, ".upload-abandoned")
	for _, path := range []string{fresh, old} {
		if err := os.WriteFile(path, []byte("partial"), 0o600); err != nil {
			t.Fatalf("write %s: %v", path, err)
		}
	}
	// Backdate the abandoned one past the threshold.
	stale := time.Now().Add(-staleUploadAge - time.Hour)
	if err := os.Chtimes(old, stale, stale); err != nil {
		t.Fatalf("backdate: %v", err)
	}

	if _, err := store.Sweep(map[string]bool{}); err != nil {
		t.Fatalf("sweep: %v", err)
	}

	if _, err := os.Stat(fresh); err != nil {
		t.Errorf("the sweep deleted an upload still in progress: %v", err)
	}
	if _, err := os.Stat(old); !os.IsNotExist(err) {
		t.Errorf("the sweep left an abandoned upload behind: %v", err)
	}
}

// TestWhichContentTypesMayBeStored.
//
// An allow-list rather than a deny-list, because the interesting types are the
// ones nobody thought of. The charset family matters: text/plain arrives from
// different sources with different parameters, and matching the exact string
// would refuse most plain text files.
func TestWhichContentTypesMayBeStored(t *testing.T) {
	accepted := []string{
		"image/jpeg", "image/png", "application/pdf",
		"text/plain", "text/plain; charset=utf-8", "text/plain;charset=iso-8859-1",
	}
	for _, mime := range accepted {
		t.Run("accepts "+mime, func(t *testing.T) {
			if !Acceptable(mime) {
				t.Errorf("Acceptable(%q) = false", mime)
			}
		})
	}

	refused := []string{
		"", "application/octet-stream", "text/html", "application/javascript",
		"application/x-msdownload", "image/plain", "application/x-sh",
	}
	for _, mime := range refused {
		name := mime
		if name == "" {
			name = "nothing at all"
		}
		t.Run("refuses "+name, func(t *testing.T) {
			if Acceptable(mime) {
				t.Errorf("Acceptable(%q) = true; the list is an allow-list because "+
					"the interesting types are the ones nobody thought of", mime)
			}
		})
	}

	// The match is on the media type, exactly, rather than on how the string
	// begins. A prefix match accepted "text/plainish" and anything else
	// starting that way: nothing sniffs such a type, so it was looseness rather
	// than a hole, and a type accepted because of how its name starts is still
	// the wrong rule for an allow-list whose point is the types nobody thought
	// of.
	for _, refused := range []string{
		"text/plainish", "text/plain-but-not-really", "application/pdfx",
		"image/pngish",
	} {
		if Acceptable(refused) {
			t.Errorf("Acceptable(%q) = true; the match is on the media type and "+
				"not on how the name begins", refused)
		}
	}

	// And the parameters are cut off either way round, so the same type is
	// accepted with a charset and without one. Two of the keys carried a
	// charset, which made the lookup miss for the type sniffed without it:
	// "text/csv" alone was refused outright.
	for _, accepted := range []string{
		"text/plain", "text/plain; charset=utf-8", "text/plain;charset=iso-8859-1",
		"text/csv", "text/csv; charset=utf-8",
		"application/pdf", "application/pdf; qs=0.001",
		// Folded, because a media type is case-insensitive and this one did not
		// come from the sniffer.
		"TEXT/PLAIN", "Application/PDF",
	} {
		if !Acceptable(accepted) {
			t.Errorf("Acceptable(%q) = false; the parameters and the case are "+
				"not what is being decided", accepted)
		}
	}

	// The explicit refusal still refuses, with or without parameters: unknown
	// is not accepted, and that is the entry that says so.
	for _, refused := range []string{
		"application/octet-stream", "application/octet-stream; charset=binary",
	} {
		if Acceptable(refused) {
			t.Errorf("Acceptable(%q) = true; an unknown type is explicitly "+
				"refused", refused)
		}
	}
}

// TestAFileTooLargeIsRefusedAndLeavesNothing.
//
// The limit is what stops one upload filling the disk. It has to be enforced
// while reading rather than after, and the partial file has to go - otherwise
// a refused upload still costs the space it was refused for.
func TestAFileTooLargeIsRefusedAndLeavesNothing(t *testing.T) {
	root := t.TempDir()
	store, err := Open(root)
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	store.MaxBytes = 64

	_, _, _, err = store.PutNamed(strings.NewReader(strings.Repeat("x", 4096)), "big.txt")
	if err == nil {
		t.Fatal("a file over the limit was stored")
	}
	if !errors.Is(err, ErrTooLarge) {
		t.Errorf("the refusal is %v, want it to say the file is too large", err)
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read the store: %v", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Errorf("a refused upload left %q behind", entry.Name())
		}
	}

	// Exactly at the limit is fine: the boundary belongs to the allowed side.
	if _, _, _, err := store.PutNamed(strings.NewReader(strings.Repeat("x", 64)), "exact.txt"); err != nil {
		t.Errorf("a file of exactly the limit was refused: %v", err)
	}
}

// TestOpeningAStoreWhereOneCannotBeMade.
//
// The directory is created on open, because the first upload on a fresh
// installation must not fail for want of a folder. A path that cannot be a
// directory is reported rather than left to fail on the first write.
func TestOpeningAStoreWhereOneCannotBeMade(t *testing.T) {
	root := t.TempDir()

	// A file where the directory should go.
	inTheWay := filepath.Join(root, "not-a-directory")
	if err := os.WriteFile(inTheWay, []byte("a file"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if _, err := Open(inTheWay); err == nil {
		t.Error("a store opened on a path that is a file")
	}

	// A nested path that does not exist yet is created.
	nested := filepath.Join(root, "a", "b", "blobs")
	store, err := Open(nested)
	if err != nil {
		t.Fatalf("open a nested path: %v", err)
	}
	if store.Root() != nested {
		t.Errorf("the store reports its root as %q, want %q", store.Root(), nested)
	}
	info, err := os.Stat(nested)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	// Attachments are receipts and screenshots from somebody's work, no less
	// sensitive than the database beside them.
	if mode := info.Mode().Perm(); mode&0o077 != 0 {
		t.Errorf("the blob directory's mode is %04o, want it reachable only by "+
			"its owner", mode)
	}
}
