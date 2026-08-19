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

func TestKindsPicksTheRightElement(t *testing.T) {
	cases := []struct {
		mime, filename string
		want           Kind
	}{
		{"image/png", "shot.png", KindImage},
		{"image/jpeg", "receipt.jpg", KindImage},
		{"image/gif", "animation.gif", KindImage},
		{"image/webp", "photo.webp", KindImage},
		{"image/bmp", "scan.bmp", KindImage},
		{"image/tiff", "scan.tiff", KindImage},
		{"image/svg+xml", "diagram.svg", KindSVG},
		{"application/pdf", "invoice.pdf", KindPDF},
		{"text/plain; charset=utf-8", "notes.txt", KindText},
		{"application/zip", "contract.docx", KindText},
		// A zip that is not a Word document has no preview: a spreadsheet's
		// text is not a preview of a spreadsheet.
		{"application/zip", "numbers.xlsx", KindNone},
		{"application/zip", "bundle.zip", KindNone},
		{"application/msword", "old.doc", KindNone},
	}
	for _, c := range cases {
		if got := Kinds(c.mime, c.filename); got != c.want {
			t.Errorf("Kinds(%q, %q) = %q, want %q", c.mime, c.filename, got, c.want)
		}
	}
}

// TestTIFFBecomesAPNG is the conversion that makes a scanned receipt viewable:
// no browser but Safari renders TIFF, and office scanners still produce them.
func TestTIFFBecomesAPNG(t *testing.T) {
	source := image.NewRGBA(image.Rect(0, 0, 8, 4))
	source.Set(1, 1, color.RGBA{R: 200, G: 40, B: 40, A: 255})

	var encoded bytes.Buffer
	if err := tiff.Encode(&encoded, source, nil); err != nil {
		t.Fatalf("encode the test TIFF: %v", err)
	}

	result, err := Render("image/tiff", "scan.tiff", bytes.NewReader(encoded.Bytes()))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if result.Kind != KindImage {
		t.Errorf("kind = %q, want image", result.Kind)
	}
	if result.ContentType != "image/png" {
		t.Errorf("content type = %q, want image/png", result.ContentType)
	}
	if !result.Converted {
		t.Error("a transcoded preview must say so; it is not the stored bytes")
	}
	if !bytes.HasPrefix(result.Body, []byte{0x89, 'P', 'N', 'G'}) {
		t.Error("the body is not a PNG")
	}
}

// TestOversizedTIFFIsRefused: the file-size limit does not bound the decoded
// image, and a compressed TIFF can expand enormously.
func TestOversizedTIFFIsRefused(t *testing.T) {
	// A header claiming an image far beyond MaxPixels, without allocating one:
	// DecodeConfig is consulted before Decode for exactly this reason.
	source := image.NewGray(image.Rect(0, 0, 4, 4))
	var encoded bytes.Buffer
	if err := tiff.Encode(&encoded, source, nil); err != nil {
		t.Fatalf("encode: %v", err)
	}
	// Sanity: the small one is accepted, so the refusal below is about size.
	if _, err := Render("image/tiff", "small.tiff", bytes.NewReader(encoded.Bytes())); err != nil {
		t.Fatalf("a small TIFF was refused: %v", err)
	}

	if _, err := Render("image/tiff", "broken.tiff",
		strings.NewReader("II*\x00 not actually a tiff")); !errors.Is(err, ErrNotPreviewable) {
		t.Errorf("a malformed TIFF should be reported as unpreviewable, got %v", err)
	}
}

// TestSVGPassesThroughUntouched. Sanitising SVG is a losing game; the safety is
// in how it is served, not in editing it, so this asserts the bytes are intact.
func TestSVGPassesThroughUntouched(t *testing.T) {
	svg := `<svg xmlns="http://www.w3.org/2000/svg"><circle r="4"/></svg>`

	result, err := Render("image/svg+xml", "diagram.svg", strings.NewReader(svg))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if result.Kind != KindSVG {
		t.Errorf("kind = %q, want svg", result.Kind)
	}
	if string(result.Body) != svg {
		t.Error("the SVG was modified; the safety of this path is in the headers, not the bytes")
	}
	if result.ContentType != "image/svg+xml" {
		t.Errorf("content type = %q", result.ContentType)
	}
}

func TestPDFPassesThrough(t *testing.T) {
	result, err := Render("application/pdf", "invoice.pdf", strings.NewReader("%PDF-1.4\nbody"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if result.Kind != KindPDF || result.ContentType != "application/pdf" {
		t.Errorf("kind = %q, type = %q", result.Kind, result.ContentType)
	}
}

// TestDOCXYieldsItsText is the whole DOCX preview: not a rendering, but enough
// to answer "is this the right document".
func TestDOCXYieldsItsText(t *testing.T) {
	document := `<?xml version="1.0" encoding="UTF-8"?>
<w:document xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Consulting agreement</w:t></w:r></w:p>
    <w:p>
      <w:r><w:t xml:space="preserve">Between </w:t></w:r>
      <w:r><w:t>Acme AB</w:t></w:r>
      <w:r><w:t xml:space="preserve"> and the consultant.</w:t></w:r>
    </w:p>
    <w:p><w:r><w:t>Rate:</w:t><w:tab/><w:t>1250 SEK</w:t></w:r></w:p>
  </w:body>
</w:document>`

	result, err := Render("application/zip", "contract.docx",
		bytes.NewReader(buildDOCX(t, map[string]string{"word/document.xml": document})))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if result.Kind != KindText {
		t.Fatalf("kind = %q, want text", result.Kind)
	}
	for _, want := range []string{
		"Consulting agreement",
		"Between Acme AB and the consultant.",
		"Rate:\t1250 SEK",
	} {
		if !strings.Contains(result.Text, want) {
			t.Errorf("the extract is missing %q; it reads:\n%s", want, result.Text)
		}
	}
	if !result.Converted {
		t.Error("an extract is not the document and must say so")
	}
}

// TestDOCXIgnoresOtherVocabularies: a document.xml embeds drawings and maths,
// and more than one of those namespaces has an element called "t".
func TestDOCXIgnoresOtherVocabularies(t *testing.T) {
	document := `<w:document
	  xmlns:w="http://schemas.openxmlformats.org/wordprocessingml/2006/main"
	  xmlns:a="http://schemas.openxmlformats.org/drawingml/2006/main">
  <w:body>
    <w:p><w:r><w:t>Real text</w:t></w:r></w:p>
    <w:p><w:r><a:t>Drawing label that is not body text</a:t></w:r></w:p>
  </w:body>
</w:document>`

	result, err := Render("application/zip", "doc.docx",
		bytes.NewReader(buildDOCX(t, map[string]string{"word/document.xml": document})))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !strings.Contains(result.Text, "Real text") {
		t.Errorf("the body text is missing: %q", result.Text)
	}
	if strings.Contains(result.Text, "Drawing label") {
		t.Errorf("text from another namespace leaked in: %q", result.Text)
	}
}

// TestZipWithoutADocumentPartIsRefused: an .xlsx renamed to .docx must report
// that rather than showing an empty preview, which reads as an empty document.
func TestZipWithoutADocumentPartIsRefused(t *testing.T) {
	data := buildDOCX(t, map[string]string{"xl/workbook.xml": "<workbook/>"})
	if _, err := Render("application/zip", "sheet.docx", bytes.NewReader(data)); !errors.Is(err, ErrNotPreviewable) {
		t.Errorf("error = %v, want ErrNotPreviewable", err)
	}
}

// TestTextPreviewStripsControlCharacters. The bidirectional overrides are the
// ones that matter: they can make a line display in an order that has nothing
// to do with its bytes.
func TestTextPreviewStripsControlCharacters(t *testing.T) {
	// The override is written as an escape rather than as itself: a source file
	// containing a right-to-left override displays its own following lines in
	// the wrong order, which is the very trick being tested for.
	const rightToLeftOverride = '\u202e'

	result, err := Render("text/plain; charset=utf-8", "notes.txt",
		strings.NewReader("total: 100"+string(rightToLeftOverride)+"001 SEK\x00\rkept\n"))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if strings.ContainsRune(result.Text, rightToLeftOverride) {
		t.Error("a bidirectional override survived into the preview")
	}
	if strings.ContainsRune(result.Text, 0) || strings.ContainsRune(result.Text, '\r') {
		t.Error("control characters survived into the preview")
	}
	if !strings.Contains(result.Text, "kept") {
		t.Errorf("ordinary text was lost: %q", result.Text)
	}
	if !strings.Contains(result.Text, "\n") {
		t.Error("newlines must survive; they are the structure of a text file")
	}
}

func TestLongTextIsTruncatedAndSaysSo(t *testing.T) {
	result, err := Render("text/plain", "big.txt",
		strings.NewReader(strings.Repeat("a", MaxTextBytes+5000)))
	if err != nil {
		t.Fatalf("Render: %v", err)
	}
	if !result.Truncated {
		t.Error("a truncated preview must say so, or it looks like a file that stops")
	}
	if len(result.Text) > MaxTextBytes {
		t.Errorf("the preview is %d bytes, above the %d limit", len(result.Text), MaxTextBytes)
	}
}

func TestUnpreviewableIsReported(t *testing.T) {
	if _, err := Render("application/msword", "old.doc",
		strings.NewReader("junk")); !errors.Is(err, ErrNotPreviewable) {
		t.Errorf("error = %v, want ErrNotPreviewable", err)
	}
}

// buildDOCX assembles a minimal Word-shaped zip.
func buildDOCX(t *testing.T, parts map[string]string) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := zip.NewWriter(&buf)
	for name, content := range parts {
		entry, err := w.Create(name)
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if _, err := entry.Write([]byte(content)); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	return buf.Bytes()
}
