package archive

import (
	"archive/zip"
	"bytes"
	"compress/flate"
	"errors"
	"fmt"
	"io"
	"time"
)

// MaxEntryBytes bounds a single entry read out of an archive.
//
// A restore reads a file somebody handed us, so an entry that claims to be a
// terabyte must be refused rather than allocated. The limit is well above the
// blob store's own 25 MiB per-file ceiling and above any plausible backup
// document, and it applies per entry rather than to the archive as a whole.
const MaxEntryBytes = 256 << 20

// ErrPasswordRequired is returned when an archive is encrypted and no password
// was supplied.
var ErrPasswordRequired = errors.New("this archive is encrypted and needs a password")

// Writer builds a zip archive, encrypting every entry when a password is set.
type Writer struct {
	zw       *zip.Writer
	password string
}

// NewWriter starts an archive. An empty password produces an ordinary zip.
func NewWriter(w io.Writer, password string) *Writer {
	return &Writer{zw: zip.NewWriter(w), password: password}
}

// Encrypted reports whether entries are being encrypted.
func (w *Writer) Encrypted() bool { return w.password != "" }

// Add writes one file into the archive.
func (w *Writer) Add(name string, modified time.Time, r io.Reader) error {
	if w.password == "" {
		return w.addPlain(name, modified, r)
	}
	return w.addEncrypted(name, modified, r)
}

// addPlain streams an entry through the zip package's own deflate.
func (w *Writer) addPlain(name string, modified time.Time, r io.Reader) error {
	header := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: modified}
	entry, err := w.zw.CreateHeader(header)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	if _, err := io.Copy(entry, r); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// addEncrypted compresses, encrypts and writes an entry as AE-2.
//
// Unlike the plain path this buffers, and it has to: an encrypted entry's
// header carries the compressed size, and the size is not known until the
// compression has happened. The zip format allows a data descriptor after the
// data instead, but few readers accept one on an encrypted entry, and an
// archive other tools refuse defeats the reason for choosing this format.
func (w *Writer) addEncrypted(name string, modified time.Time, r io.Reader) error {
	plain, err := readAll(r, MaxEntryBytes)
	if err != nil {
		return fmt.Errorf("read %s: %w", name, err)
	}

	var compressed bytes.Buffer
	deflater, err := flate.NewWriter(&compressed, flate.DefaultCompression)
	if err != nil {
		return err
	}
	if _, err := deflater.Write(plain); err != nil {
		return fmt.Errorf("compress %s: %w", name, err)
	}
	if err := deflater.Close(); err != nil {
		return fmt.Errorf("compress %s: %w", name, err)
	}

	body, err := encryptAE2(w.password, compressed.Bytes())
	if err != nil {
		return fmt.Errorf("encrypt %s: %w", name, err)
	}

	date, clock := msdosTime(modified)
	header := &zip.FileHeader{
		Name: name,
		// The declared method is 99; the real one lives in the extra field.
		Method: aesMethod,
		Extra:  aesExtraField(zip.Deflate),
		// AE-2 stores no CRC. The authentication code is what detects damage,
		// and a CRC of the plaintext would leak a checksum of it.
		CRC32:              0,
		CompressedSize64:   uint64(len(body)),
		UncompressedSize64: uint64(len(plain)),
		// CreateRaw hands the header through untouched, so the fields
		// CreateHeader would have filled in have to be set here.
		ReaderVersion:  zipVersion20,
		CreatorVersion: zipVersion20,
		ModifiedDate:   date,
		ModifiedTime:   clock,
		Modified:       modified,
	}
	// Bit 0 is the general-purpose "this entry is encrypted" flag, and it is
	// what every other tool keys on to know it must decrypt before inflating.
	// Without it an archive is structurally perfect, decrypts correctly by
	// hand, and fails in any real archiver with a decompression error - the
	// bytes are handed straight to the inflater. Nothing this package could
	// test against itself would notice.
	//
	// Bit 11 declares the name as UTF-8. Entry names here are content hashes
	// and fixed strings, but declaring it costs nothing and is correct.
	header.Flags |= 0x1 | 0x800

	entry, err := w.zw.CreateRaw(header)
	if err != nil {
		return fmt.Errorf("create %s: %w", name, err)
	}
	if _, err := entry.Write(body); err != nil {
		return fmt.Errorf("write %s: %w", name, err)
	}
	return nil
}

// zipVersion20 is "2.0", the version an ordinary deflated entry needs.
const zipVersion20 = 20

// Close finishes the archive.
func (w *Writer) Close() error { return w.zw.Close() }

// ------------------------------------------------------------------ reading --

// Reader opens an archive, decrypting entries when a password is supplied.
type Reader struct {
	zr       *zip.Reader
	password string
}

// NewReader opens an archive for reading.
func NewReader(r io.ReaderAt, size int64, password string) (*Reader, error) {
	zr, err := zip.NewReader(r, size)
	if err != nil {
		return nil, err
	}
	return &Reader{zr: zr, password: password}, nil
}

// Names lists the entries in the archive, in the order they were written.
func (r *Reader) Names() []string {
	names := make([]string, 0, len(r.zr.File))
	for _, file := range r.zr.File {
		names = append(names, file.Name)
	}
	return names
}

// Encrypted reports whether any entry is encrypted.
func (r *Reader) Encrypted() bool {
	for _, file := range r.zr.File {
		if file.Method == aesMethod {
			return true
		}
	}
	return false
}

// ReadFile returns one entry's contents.
func (r *Reader) ReadFile(name string) ([]byte, error) {
	for _, file := range r.zr.File {
		if file.Name == name {
			return r.read(file)
		}
	}
	return nil, fmt.Errorf("%s is not in the archive", name)
}

// Each calls fn for every entry, in archive order.
//
// A callback rather than a slice of readers, because the alternative invites
// holding every entry of a large archive open at once.
func (r *Reader) Each(fn func(name string, data []byte) error) error {
	for _, file := range r.zr.File {
		if file.FileInfo().IsDir() {
			continue
		}
		data, err := r.read(file)
		if err != nil {
			return err
		}
		if err := fn(file.Name, data); err != nil {
			return err
		}
	}
	return nil
}

// read returns one entry's plaintext, decrypting it if it is encrypted.
func (r *Reader) read(file *zip.File) ([]byte, error) {
	if file.Method != aesMethod {
		// An ordinary entry: the zip package knows how to do this.
		opened, err := file.Open()
		if err != nil {
			return nil, fmt.Errorf("open %s: %w", file.Name, err)
		}
		defer func() { _ = opened.Close() }()
		return readAll(opened, MaxEntryBytes)
	}

	if r.password == "" {
		return nil, ErrPasswordRequired
	}
	realMethod, ok := parseAESExtra(file.Extra)
	if !ok {
		return nil, fmt.Errorf("%s: %w", file.Name,
			errors.New("the entry claims to be encrypted but does not say how"))
	}

	// OpenRaw rather than Open: the zip package has no decompressor for method
	// 99 and would refuse. The bytes come out exactly as they were written.
	raw, err := file.OpenRaw()
	if err != nil {
		return nil, fmt.Errorf("open %s: %w", file.Name, err)
	}
	body, err := readAll(raw, MaxEntryBytes)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", file.Name, err)
	}

	compressed, err := decryptAE2(r.password, body)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", file.Name, err)
	}

	switch realMethod {
	case zip.Store:
		return compressed, nil
	case zip.Deflate:
		inflater := flate.NewReader(bytes.NewReader(compressed))
		defer func() { _ = inflater.Close() }()
		return readAll(inflater, MaxEntryBytes)
	default:
		return nil, fmt.Errorf("%s uses compression method %d, which this version cannot read",
			file.Name, realMethod)
	}
}

// msdosTime converts an instant to the two 16-bit fields a zip header carries.
//
// CreateRaw does not do this for us the way CreateHeader does, and an entry
// with a zero date shows as 1980-01-01 in every archive manager - which looks
// like a bug in the file rather than a detail of how it was written.
func msdosTime(t time.Time) (date, clock uint16) {
	// The MS-DOS epoch is 1980, and there is nothing to be done for anything
	// before it but clamp: the field cannot represent it.
	if t.Year() < 1980 {
		return 1<<5 | 1, 0 // 1980-01-01 00:00:00
	}
	date = uint16(t.Day() + int(t.Month())<<5 + (t.Year()-1980)<<9)
	clock = uint16(t.Second()/2 + t.Minute()<<5 + t.Hour()<<11)
	return date, clock
}
