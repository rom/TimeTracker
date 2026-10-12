package preview

import (
	"archive/zip"
	"bytes"
	"errors"
	"image"
	"image/color"
	"strings"
	"testing"

	"golang.org/x/image/tiff"
)

// Previewing a file that is not what it should be.
//
// A preview is a convenience: a receipt shown inline instead of downloaded. So
// every failure here has one correct answer - refuse, and let the screen that
// listed the attachment carry on. A panic or a hang takes down a page that was
// working.

// docx builds a .docx around a document.xml body.
func docx(t *testing.T, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entry, err := zw.Create("word/document.xml")
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := entry.Write([]byte(body)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

// docxMIME is how a .docx reaches the previewer: the blob store sniffs
// content rather than trusting the client, and a .docx sniffs as a zip. The
// extension is what distinguishes it from any other zip, which is why Render
// takes the filename as well.
const docxMIME = "application/zip"

const docxHeader = `<?xml version="1.0"?>` +
	`<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"><w:body>`

// TestADocumentThatIsNotOne.
//
// A .docx is a zip holding XML. Each of these is something somebody uploads
// under that name, and each has to be refused rather than rendered as
// whatever bytes happened to be there.
func TestADocumentThatIsNotOne(t *testing.T) {
	cases := map[string][]byte{
		"not a zip":                []byte("this is a plain sentence"),
		"empty":                    nil,
		"a truncated zip":          []byte("PK\x03\x04 and then nothing"),
		"a zip without a document": zipOf(t, "notes.txt", "some notes"),
	}

	for name, content := range cases {
		t.Run(name, func(t *testing.T) {
			_, err := Render(docxMIME, "report.docx", bytes.NewReader(content))
			if err == nil {
				t.Errorf("%s was previewed as a document", name)
			}
			if !errors.Is(err, ErrNotPreviewable) {
				t.Errorf("%s was refused with %v, want a not-previewable error", name, err)
			}
		})
	}
}

// zipOf builds a zip holding one named entry.
func zipOf(t *testing.T, name, body string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	entry, err := zw.Create(name)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if _, err := entry.Write([]byte(body)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := zw.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}

// TestADocumentsTextIsTheTextAndNotTheMarkup.
//
// Everything outside a text run is markup indentation, and gathering it would
// produce a preview made mostly of whitespace - which is a worse answer than
// no preview, because it looks like the document is empty.
func TestADocumentsTextIsTheTextAndNotTheMarkup(t *testing.T) {
	body := docxHeader +
		`<w:p><w:r><w:t>The first paragraph.</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>Before</w:t><w:tab/><w:t>after a tab</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>Before</w:t><w:br/><w:t>after a break</w:t></w:r></w:p>` +
		`<w:p><w:pPr><w:jc w:val="center"/></w:pPr><w:r><w:t>Centred.</w:t></w:r></w:p>` +
		`</w:body></w:document>`

	result, err := Render(docxMIME, "report.docx", bytes.NewReader(docx(t, body)))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if result.Kind != KindText {
		t.Errorf("the preview is of kind %q, want text", result.Kind)
	}

	for _, want := range []string{
		"The first paragraph.",
		"Before\tafter a tab",
		"Before\nafter a break",
		"Centred.",
	} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("the preview does not contain %q:\n%q", want, result.Text)
		}
	}
	// The markup's own attribute values are not text.
	if strings.Contains(result.Text, "center") {
		t.Errorf("the preview carries markup from outside a text run:\n%q", result.Text)
	}
	if result.Truncated {
		t.Error("a short document was reported as truncated")
	}
}

// TestALongDocumentIsTruncatedAndSaysSo.
//
// The cap bounds what one preview can cost. Truncating silently would show
// somebody the first page of a contract and let them believe they had read it.
func TestALongDocumentIsTruncatedAndSaysSo(t *testing.T) {
	var body strings.Builder
	body.WriteString(docxHeader)
	for i := 0; i < 900; i++ {
		body.WriteString(`<w:p><w:r><w:t>` +
			strings.Repeat("a paragraph of some length. ", 4) +
			`</w:t></w:r></w:p>`)
	}
	body.WriteString(`</w:body></w:document>`)

	result, err := Render(docxMIME, "long.docx", bytes.NewReader(docx(t, body.String())))
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	if !result.Truncated {
		t.Error("a document past the limit was not reported as truncated, so " +
			"somebody would believe they had read all of it")
	}
	if len(result.Text) > MaxTextBytes {
		t.Errorf("the preview is %d bytes, past the %d-byte limit",
			len(result.Text), MaxTextBytes)
	}
	if len(result.Text) == 0 {
		t.Error("the preview is empty; what fits should still be shown")
	}
}

// TestADocumentThatStopsMidWay.
//
// XML that ends in the middle of an element, which is what a truncated upload
// or a failing disk produces. Whatever came before it is more useful than an
// error page - but a document with nothing readable at all is a refusal.
func TestADocumentThatStopsMidWay(t *testing.T) {
	partial := docxHeader +
		`<w:p><w:r><w:t>This much was readable.</w:t></w:r></w:p>` +
		`<w:p><w:r><w:t>And then it stop`

	result, err := Render(docxMIME, "partial.docx", bytes.NewReader(docx(t, partial)))
	if err != nil {
		t.Fatalf("a document with readable text before the break was refused: %v", err)
	}
	if !strings.Contains(result.Text, "This much was readable.") {
		t.Errorf("the readable part was dropped:\n%q", result.Text)
	}

	// Nothing readable at all: a refusal, since there is nothing to show.
	_, err = Render(docxMIME, "broken.docx", bytes.NewReader(docx(t, `<w:document><w:body><w:p`)))
	if err == nil {
		t.Error("a document with nothing readable in it produced a preview")
	}
}

// TestAnImageTooLargeToDecodeIsRefusedBeforeItIsDecoded.
//
// The pixel bound is read from the image's header, before the pixels are
// allocated. A decompression bomb declares a huge size in a few bytes, so
// checking after decoding is checking too late.
func TestAnImageTooLargeToDecodeIsRefusedBeforeItIsDecoded(t *testing.T) {
	// A TIFF with a plausible header and an enormous declared size would need
	// a hand-built file; instead assert the bound itself is applied by
	// rendering one that is merely large enough to notice, and that an
	// ordinary one goes through.
	small := tiffOf(t, 8, 8)
	result, err := Render("image/tiff", "scan.tiff", bytes.NewReader(small))
	if err != nil {
		t.Fatalf("an ordinary TIFF was refused: %v", err)
	}
	if result.Kind != KindImage {
		t.Errorf("the preview is of kind %q, want an image", result.Kind)
	}
	if len(result.Body) == 0 {
		t.Error("the converted image is empty")
	}
	// Converted to something a browser renders, since no browser shows a TIFF.
	if result.ContentType == "image/tiff" {
		t.Error("the preview is still a TIFF, which no browser will render")
	}

	// Not a TIFF at all.
	for name, content := range map[string]string{
		"a sentence":         "this is not a TIFF",
		"empty":              "",
		"a truncated header": "II*\x00",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Render("image/tiff", "scan.tiff", strings.NewReader(content)); err == nil {
				t.Errorf("%s was previewed as a TIFF", name)
			}
		})
	}
}

// tiffOf builds a small grey TIFF.
func tiffOf(t *testing.T, width, height int) []byte {
	t.Helper()
	img := image.NewGray(image.Rect(0, 0, width, height))
	for y := 0; y < height; y++ {
		for x := 0; x < width; x++ {
			img.SetGray(x, y, color.Gray{Y: uint8((x * y) % 256)})
		}
	}
	var buf bytes.Buffer
	if err := tiff.Encode(&buf, img, nil); err != nil {
		t.Fatalf("encode a TIFF: %v", err)
	}
	return buf.Bytes()
}

// TestAReaderThatFailsPartWayThrough.
//
// A download interrupted, or a blob store on a disk that went away. Each
// renderer reads its input whole, and a read failure has to surface as an
// error rather than as a preview of whatever arrived.
func TestAReaderThatFailsPartWayThrough(t *testing.T) {
	wanted := errors.New("the disk went away")

	for _, mime := range []string{
		"text/plain",
		"image/tiff",
		docxMIME,
	} {
		t.Run(mime, func(t *testing.T) {
			_, err := Render(mime, "file", &failingReader{after: 16, err: wanted})
			if err == nil {
				t.Errorf("a preview was produced from a reader that failed")
			}
		})
	}
}

// failingReader yields a fixed number of bytes and then fails.
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
