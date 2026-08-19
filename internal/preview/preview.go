// Package preview decides how an attachment can be shown in a browser, and
// prepares the bytes for it.
//
// It exists because "can this be previewed" is three different questions with
// three different answers:
//
//   - PNG, JPEG, GIF, WEBP and BMP: the browser already knows. Pass them
//     through.
//   - TIFF: no browser but Safari renders it, so it is decoded here and
//     re-encoded as a PNG. A scanned receipt arriving as a TIFF is common
//     enough - it is what a great many office scanners still produce - that
//     "download it and open it in something else" is a poor answer.
//   - DOCX: no browser renders one, and none ever will. What can be produced is
//     the text, which answers "is this the right document" - the question a
//     preview is for. It is labelled as an extract rather than presented as a
//     rendering, because a preview that quietly loses the tables and the
//     letterhead should say so.
//
// SVG is the one that needs care rather than work. It is passed through
// untouched, and everything that makes it safe is in how it is served: inside
// an <img>, where no browser will run script, and behind a response CSP that
// forbids everything anyway. See docs/adr/0031-attachment-previews.md.
package preview

import (
	"bytes"
	"errors"
	"fmt"
	"image/png"
	"io"
	"path/filepath"
	"strings"

	"golang.org/x/image/tiff"
)

// Kind is how a preview should be placed on the page.
//
// The distinction is not cosmetic: each kind gets a different element, and the
// element is what decides whether the content can execute anything.
type Kind string

const (
	// KindNone means there is nothing to show. The download link stands alone.
	KindNone Kind = ""
	// KindImage is rendered with <img>.
	KindImage Kind = "image"
	// KindSVG is also rendered with <img>, and that is the whole security
	// argument: an SVG in an <img> cannot run script, cannot fetch anything and
	// cannot navigate. Rendering the same file in an <object> or an <iframe>
	// would give it all three.
	KindSVG Kind = "svg"
	// KindPDF is rendered with <object>, which hands it to the browser's own
	// viewer.
	KindPDF Kind = "pdf"
	// KindText is rendered as text in the page, escaped like any other string.
	KindText Kind = "text"
)

// ErrNotPreviewable is returned for content this package cannot show.
var ErrNotPreviewable = errors.New("this file cannot be previewed")

// MaxPixels bounds a decoded image.
//
// A TIFF is decoded into memory to be re-encoded, and the file size limit does
// not bound that: compression means a few megabytes on disk can be hundreds in
// memory. Sixteen megapixels is far beyond any scanned receipt and still only
// about 64 MB of RGBA.
const MaxPixels = 16 << 20

// MaxTextBytes bounds an extracted text preview.
//
// A preview answers "is this the right file", which the first few pages settle.
// Sending a whole book into a page nobody will scroll is waste.
const MaxTextBytes = 64 << 10

// Result is a prepared preview.
type Result struct {
	Kind Kind
	// ContentType is what the bytes are, which is not always what was uploaded:
	// a TIFF comes back as a PNG.
	ContentType string
	// Body is the content to serve. Empty for KindText, whose content is in
	// Text instead, because that one is rendered into the page rather than
	// fetched as a separate resource.
	Body []byte
	// Text is the extracted text, for KindText.
	Text string
	// Truncated says the text was cut short, so the page can say so rather than
	// appearing to show a document that simply stops.
	Truncated bool
	// Converted says the bytes were transcoded, so the page can note that what
	// is on screen is not byte-for-byte the stored file.
	Converted bool
}

// Kinds names the previewable kind for a stored file, or KindNone.
//
// It looks at the sniffed type first and the filename only where the type
// cannot distinguish - a DOCX is a zip, and nothing in its first 512 bytes says
// otherwise.
func Kinds(mime, filename string) Kind {
	switch mediaType(mime) {
	case "image/png", "image/jpeg", "image/gif", "image/webp", "image/bmp":
		return KindImage
	case "image/tiff":
		return KindImage
	case "image/svg+xml":
		return KindSVG
	case "application/pdf":
		return KindPDF
	case "text/plain", "text/csv", "text/markdown":
		return KindText
	case "application/zip":
		// Only a DOCX. The other zip containers - xlsx, pptx, odt - would each
		// need their own reader, and a spreadsheet's text is not a preview of
		// a spreadsheet in any useful sense.
		if strings.EqualFold(filepath.Ext(filename), ".docx") {
			return KindText
		}
	}
	return KindNone
}

// Render prepares the preview for one attachment.
func Render(mime, filename string, r io.Reader) (Result, error) {
	kind := Kinds(mime, filename)
	if kind == KindNone {
		return Result{}, ErrNotPreviewable
	}

	switch mediaType(mime) {
	case "image/tiff":
		return renderTIFF(r)

	case "application/zip":
		return renderDOCX(r)

	case "text/plain", "text/csv", "text/markdown":
		return renderText(r)

	default:
		// Passed through byte for byte. The safety of the SVG case is entirely
		// in the headers and the element it is placed in, not in anything done
		// to the bytes: sanitising SVG is a losing game, and not rendering it
		// as a document at all is the winning one.
		body, err := io.ReadAll(io.LimitReader(r, MaxPreviewBytes))
		if err != nil {
			return Result{}, err
		}
		return Result{Kind: kind, ContentType: mediaType(mime), Body: body}, nil
	}
}

// MaxPreviewBytes bounds what is read for a pass-through preview. It matches the
// blob store's own per-file limit, so nothing storable is refused here.
const MaxPreviewBytes = 25 << 20

// renderTIFF decodes a TIFF and re-encodes it as a PNG.
func renderTIFF(r io.Reader) (Result, error) {
	// The whole file, because the decoder needs to seek and the configuration
	// has to be read before the image to bound what is about to be allocated.
	data, err := io.ReadAll(io.LimitReader(r, MaxPreviewBytes))
	if err != nil {
		return Result{}, err
	}

	config, err := tiff.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("%w: the TIFF could not be read: %s", ErrNotPreviewable, err)
	}
	if config.Width <= 0 || config.Height <= 0 ||
		int64(config.Width)*int64(config.Height) > MaxPixels {
		return Result{}, fmt.Errorf(
			"%w: the image is %d by %d, which is too large to convert",
			ErrNotPreviewable, config.Width, config.Height)
	}

	img, err := tiff.Decode(bytes.NewReader(data))
	if err != nil {
		return Result{}, fmt.Errorf("%w: the TIFF could not be decoded: %s", ErrNotPreviewable, err)
	}

	var out bytes.Buffer
	// Default compression: a preview is generated on every view, and spending
	// noticeably longer to save a few percent of a transfer that is already
	// local is the wrong trade.
	encoder := png.Encoder{CompressionLevel: png.DefaultCompression}
	if err := encoder.Encode(&out, img); err != nil {
		return Result{}, fmt.Errorf("%w: %s", ErrNotPreviewable, err)
	}

	return Result{
		Kind:        KindImage,
		ContentType: "image/png",
		Body:        out.Bytes(),
		Converted:   true,
	}, nil
}

// renderText reads a plain-text file, bounded.
func renderText(r io.Reader) (Result, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxTextBytes+1))
	if err != nil {
		return Result{}, err
	}
	truncated := len(data) > MaxTextBytes
	if truncated {
		data = data[:MaxTextBytes]
	}
	return Result{
		Kind:      KindText,
		Text:      sanitiseText(string(data)),
		Truncated: truncated,
	}, nil
}

// sanitiseText removes the control characters that would otherwise be rendered
// as replacement squares or, worse, reorder the text on screen.
//
// The bidirectional overrides are the ones that matter: they can make a line
// display in an order that has nothing to do with the bytes, which is a way of
// making a document look like it says something it does not.
func sanitiseText(s string) string {
	return strings.Map(func(r rune) rune {
		switch {
		case r == '\n' || r == '\t':
			return r
		case r == '\r':
			return -1
		case r < 0x20 || r == 0x7f:
			return -1
		case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069:
			return -1
		}
		return r
	}, s)
}

// mediaType strips any parameters from a content type.
func mediaType(mime string) string {
	base, _, _ := strings.Cut(mime, ";")
	return strings.ToLower(strings.TrimSpace(base))
}
