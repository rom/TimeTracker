// Package blob stores attachment bytes on disk, addressed by their content.
//
// The path of a file is derived from the SHA-256 of its contents, which buys
// three things at once: an identical file attached twice is stored once, the
// user's filename never becomes a path component (so traversal and
// case-collision problems cannot arise), and integrity verification is free -
// re-hash and compare.
//
// See docs/adr/0013-attachment-storage.md.
package blob

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// ErrTooLarge is returned when an upload exceeds the configured limit.
var ErrTooLarge = errors.New("file is too large")

// ErrUnacceptableType is returned when the content is not an accepted type.
var ErrUnacceptableType = errors.New("file type is not accepted")

// Store holds attachment bytes under a directory.
type Store struct {
	root string
	// MaxBytes bounds a single upload. An unbounded upload is a
	// denial-of-service surface and a way to fill someone's disk.
	MaxBytes int64
}

// DefaultMaxBytes is the per-file limit. Generous enough for a photographed
// receipt or a screenshot, small enough that a hostile client cannot fill a disk
// quickly.
const DefaultMaxBytes = 25 << 20 // 25 MiB

// Open prepares the blob directory.
func Open(root string) (*Store, error) {
	// 0700: attachments are receipts and screenshots from someone's work, no
	// less sensitive than the database beside them.
	if err := os.MkdirAll(root, 0o700); err != nil {
		return nil, fmt.Errorf("create blob directory %s: %w", root, err)
	}
	return &Store{root: root, MaxBytes: DefaultMaxBytes}, nil
}

// Root returns the directory in use, for backups and diagnostics.
func (s *Store) Root() string { return s.root }

// ErrTypeMismatch is returned when the content does not match the extension.
var ErrTypeMismatch = errors.New("the file's contents do not match its name")

// Put stores the contents of r and returns its hash, size and detected type.
//
// The content is written to a temporary file while being hashed, then moved into
// place. Writing first and committing the database row afterwards means a crash
// between the two leaves a harmless orphan file - swept later - rather than a
// database row pointing at nothing.
func (s *Store) Put(r io.Reader) (hash string, size int64, mime string, err error) {
	return s.PutNamed(r, "")
}

// PutNamed stores content and checks it against the name the client gave it.
//
// The name is used for one thing only - confirming that the extension agrees
// with what the bytes actually are. It never becomes part of the storage path.
// A disagreement is refused rather than silently accepted: it is how someone
// notices they attached the wrong file, and it closes the "looks like an image,
// is not" case at the point of upload rather than relying on every downstream
// consumer to re-sniff.
func (s *Store) PutNamed(r io.Reader, filename string) (hash string, size int64, mime string, err error) {
	temp, err := os.CreateTemp(s.root, ".upload-*")
	if err != nil {
		return "", 0, "", fmt.Errorf("create temporary file: %w", err)
	}
	tempName := temp.Name()
	defer func() {
		_ = temp.Close()
		// Removing a file that was already moved fails harmlessly.
		_ = os.Remove(tempName)
	}()

	digest := sha256.New()
	// The first bytes are kept for type sniffing. 512 is what
	// http.DetectContentType examines.
	var head []byte

	// LimitReader with one extra byte, so exceeding the limit is detectable
	// rather than silently truncating the file.
	limited := io.LimitReader(r, s.MaxBytes+1)
	buf := make([]byte, 32*1024)

	for {
		n, readErr := limited.Read(buf)
		if n > 0 {
			chunk := buf[:n]
			if len(head) < 512 {
				need := 512 - len(head)
				if need > n {
					need = n
				}
				head = append(head, chunk[:need]...)
			}
			digest.Write(chunk)
			if _, writeErr := temp.Write(chunk); writeErr != nil {
				return "", 0, "", fmt.Errorf("write upload: %w", writeErr)
			}
			size += int64(n)
			if size > s.MaxBytes {
				return "", 0, "", fmt.Errorf("%w: limit is %d bytes", ErrTooLarge, s.MaxBytes)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return "", 0, "", fmt.Errorf("read upload: %w", readErr)
		}
	}

	if size == 0 {
		return "", 0, "", fmt.Errorf("%w: the file is empty", ErrUnacceptableType)
	}

	// The type is sniffed from the bytes, never taken from the client's claim:
	// a renamed executable would otherwise be served back with whatever type its
	// uploader asserted.
	mime = http.DetectContentType(head)
	if !Acceptable(mime) {
		return "", 0, "", fmt.Errorf("%w: %s", ErrUnacceptableType, mime)
	}
	if err := checkExtensionAgrees(filename, mime); err != nil {
		return "", 0, "", err
	}

	// Force the bytes to disk before recording them anywhere. A blob that is
	// still in the page cache is not a blob that survives the crash it exists to
	// survive.
	if err := temp.Sync(); err != nil {
		return "", 0, "", fmt.Errorf("flush upload: %w", err)
	}
	if err := temp.Close(); err != nil {
		return "", 0, "", fmt.Errorf("close upload: %w", err)
	}

	hash = hex.EncodeToString(digest.Sum(nil))
	target := s.pathFor(hash)

	if err := os.MkdirAll(filepath.Dir(target), 0o700); err != nil {
		return "", 0, "", fmt.Errorf("create blob subdirectory: %w", err)
	}

	// If the file already exists the content is identical by definition, so
	// there is nothing to do: this is deduplication, and it is free.
	if _, statErr := os.Stat(target); statErr == nil {
		return hash, size, mime, nil
	}

	if err := os.Rename(tempName, target); err != nil {
		return "", 0, "", fmt.Errorf("store blob: %w", err)
	}
	if err := os.Chmod(target, 0o600); err != nil {
		return "", 0, "", fmt.Errorf("set blob permissions: %w", err)
	}
	return hash, size, mime, nil
}

// Get opens a stored blob for reading.
func (s *Store) Get(hash string) (io.ReadSeekCloser, error) {
	if !validHash(hash) {
		// The hash comes from the database, but validating it here means a
		// corrupted or crafted value can never become a path.
		return nil, fmt.Errorf("invalid blob hash")
	}
	file, err := os.Open(s.pathFor(hash))
	if err != nil {
		return nil, fmt.Errorf("open blob %s: %w", hash[:8], err)
	}
	return file, nil
}

// Size returns the stored size of a blob.
func (s *Store) Size(hash string) (int64, error) {
	if !validHash(hash) {
		return 0, fmt.Errorf("invalid blob hash")
	}
	info, err := os.Stat(s.pathFor(hash))
	if err != nil {
		return 0, err
	}
	return info.Size(), nil
}

// Remove deletes a blob. Callers must first establish that nothing references
// it, because content addressing means several records can share one file.
func (s *Store) Remove(hash string) error {
	if !validHash(hash) {
		return fmt.Errorf("invalid blob hash")
	}
	if err := os.Remove(s.pathFor(hash)); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove blob: %w", err)
	}
	return nil
}

// Sweep removes blobs that nothing references, and reports how many went.
//
// Deletion is a sweep rather than something done on the request path, so that
// removing an attachment stays a single fast database write.
func (s *Store) Sweep(referenced map[string]bool) (removed int, err error) {
	err = filepath.WalkDir(s.root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		name := entry.Name()
		// Partial uploads from an interrupted request.
		if strings.HasPrefix(name, ".upload-") {
			if info, statErr := entry.Info(); statErr == nil {
				// Only old ones: a fresh temporary file belongs to an upload
				// still in progress.
				if timeSince(info.ModTime()) > staleUploadAge {
					_ = os.Remove(path)
				}
			}
			return nil
		}
		if !validHash(name) {
			return nil
		}
		if !referenced[name] {
			if removeErr := os.Remove(path); removeErr == nil {
				removed++
			}
		}
		return nil
	})
	return removed, err
}

// pathFor spreads blobs across two levels of subdirectory.
//
// A single directory with a hundred thousand files is slow to list on most
// filesystems and unpleasant to work with by hand; two levels of two hex
// characters gives 65,536 buckets, which is ample.
func (s *Store) pathFor(hash string) string {
	return filepath.Join(s.root, hash[0:2], hash[2:4], hash)
}

// validHash checks that a string is a lower-case hex SHA-256, which is what
// makes it safe to use as a path component.
func validHash(hash string) bool {
	if len(hash) != 64 {
		return false
	}
	for _, c := range hash {
		if (c < '0' || c > '9') && (c < 'a' || c > 'f') {
			return false
		}
	}
	return true
}

// acceptedTypes is the allow-list of content types.
//
// An allow-list rather than a deny-list, because the interesting types are the
// ones nobody thought of. Notably absent: image/svg+xml, which is script-capable
// and is therefore treated as hostile - a stored SVG served inline is a stored
// cross-site scripting vector.
var acceptedTypes = map[string]bool{
	"image/jpeg":                true,
	"image/png":                 true,
	"image/gif":                 true,
	"image/webp":                true,
	"image/bmp":                 true,
	"image/tiff":                true,
	"application/pdf":           true,
	"text/plain; charset=utf-8": true,
	"text/csv; charset=utf-8":   true,
	"application/zip":           true, // covers .docx, .xlsx and .odt
	"application/msword":        true,
	"application/vnd.ms-excel":  true,
	"application/octet-stream":  false, // explicitly refused: unknown is not accepted
}

// extensionTypes maps a file extension to the content types that may carry it.
//
// Only the extensions worth policing are listed. An unknown extension is not an
// error - people name files all sorts of things - but a name that claims to be a
// PNG while the bytes are a shell script is worth refusing.
var extensionTypes = map[string][]string{
	".png":  {"image/png"},
	".jpg":  {"image/jpeg"},
	".jpeg": {"image/jpeg"},
	".gif":  {"image/gif"},
	".webp": {"image/webp"},
	".bmp":  {"image/bmp"},
	".tif":  {"image/tiff"},
	".tiff": {"image/tiff"},
	".pdf":  {"application/pdf"},
	// The OOXML and ODF formats are all zip containers, which is what the
	// sniffer sees.
	".docx": {"application/zip"},
	".xlsx": {"application/zip"},
	".pptx": {"application/zip"},
	".odt":  {"application/zip"},
	".ods":  {"application/zip"},
	".zip":  {"application/zip"},
	".txt":  {"text/plain"},
	".csv":  {"text/plain", "text/csv"},
	".md":   {"text/plain"},
	".log":  {"text/plain"},
}

// checkExtensionAgrees refuses content that contradicts its file name.
func checkExtensionAgrees(filename, mime string) error {
	if filename == "" {
		return nil
	}
	extension := strings.ToLower(filepath.Ext(filename))
	expected, policed := extensionTypes[extension]
	if !policed {
		// An extension nobody listed is not evidence of anything.
		return nil
	}

	// The sniffed type carries parameters ("text/plain; charset=utf-8"), so the
	// comparison is on the media type alone.
	base, _, _ := strings.Cut(mime, ";")
	base = strings.TrimSpace(base)

	for _, candidate := range expected {
		if base == candidate {
			return nil
		}
	}
	return fmt.Errorf("%w: %s claims to be %s but its contents are %s",
		ErrTypeMismatch, filename, extension, base)
}

// Acceptable reports whether a sniffed content type may be stored.
func Acceptable(mime string) bool {
	if accepted, known := acceptedTypes[mime]; known {
		return accepted
	}
	// text/plain arrives with various charset parameters; accept the family.
	if strings.HasPrefix(mime, "text/plain") {
		return true
	}
	return false
}

// staleUploadAge is how long a temporary upload file may linger before the sweep
// treats it as abandoned. Long enough that a slow upload in progress is never
// swept out from under itself.
const staleUploadAge = 6 * time.Hour

// timeSince is time.Since, named so the sweep reads clearly.
func timeSince(t time.Time) time.Duration { return time.Since(t) }
