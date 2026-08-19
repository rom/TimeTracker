package preview

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"fmt"
	"io"
	"strings"
)

// Reading the text out of a Word document.
//
// A .docx is a zip holding XML, and the text lives in word/document.xml as
// runs of <w:t> inside paragraphs of <w:p>. That is all this needs: it is not a
// renderer and does not pretend to be one. Tables, images, headers, styles and
// numbering are all lost, which is why the interface labels the result an
// extract rather than a preview.
//
// Even so it answers the question a preview is asked: is this the right
// document? A filename cannot answer that and a download does not answer it
// quickly.

// docxDocumentPart is where Word keeps the body text.
const docxDocumentPart = "word/document.xml"

// maxDOCXPartBytes bounds the decompressed XML, so a zip bomb in a receipt
// cannot exhaust memory. Well above any real document's body.
const maxDOCXPartBytes = 32 << 20

// renderDOCX extracts the text of a Word document.
func renderDOCX(r io.Reader) (Result, error) {
	data, err := io.ReadAll(io.LimitReader(r, MaxPreviewBytes))
	if err != nil {
		return Result{}, err
	}

	archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		return Result{}, fmt.Errorf("%w: the file is not a readable document: %s",
			ErrNotPreviewable, err)
	}

	var part *zip.File
	for _, file := range archive.File {
		if file.Name == docxDocumentPart {
			part = file
			break
		}
	}
	if part == nil {
		// A zip with no document part: an .xlsx or an .odt renamed, or a
		// document this reader does not understand. Saying so beats an empty
		// preview that looks like an empty document.
		return Result{}, fmt.Errorf("%w: this does not look like a Word document",
			ErrNotPreviewable)
	}

	opened, err := part.Open()
	if err != nil {
		return Result{}, fmt.Errorf("%w: %s", ErrNotPreviewable, err)
	}
	defer func() { _ = opened.Close() }()

	text, truncated, err := docxText(io.LimitReader(opened, maxDOCXPartBytes))
	if err != nil {
		return Result{}, err
	}
	return Result{
		Kind:      KindText,
		Text:      text,
		Truncated: truncated,
		Converted: true,
	}, nil
}

// docxText walks the document XML and gathers its paragraphs.
//
// A streaming token walk rather than unmarshalling into a struct, because the
// shape of a Word document is deep, varies with whatever produced it, and only
// four element names out of it matter here.
func docxText(r io.Reader) (string, bool, error) {
	decoder := xml.NewDecoder(r)
	var out strings.Builder
	var paragraph strings.Builder
	// inText is true between <w:t> and </w:t>. Everything outside a text run is
	// markup indentation, and gathering it would produce a preview made mostly
	// of whitespace.
	inText := false
	truncated := false

	flush := func() {
		line := strings.TrimRight(paragraph.String(), " \t")
		paragraph.Reset()
		if out.Len()+len(line)+1 > MaxTextBytes {
			truncated = true
			return
		}
		out.WriteString(line)
		out.WriteString("\n")
	}

	for !truncated {
		token, err := decoder.Token()
		if err == io.EOF {
			break
		}
		if err != nil {
			// A document that stops mid-way still has whatever came before it,
			// and that is more useful than an error page.
			if out.Len() > 0 || paragraph.Len() > 0 {
				break
			}
			return "", false, fmt.Errorf("%w: the document could not be read: %s",
				ErrNotPreviewable, err)
		}

		switch element := token.(type) {
		case xml.CharData:
			if inText {
				paragraph.Write(element)
			}

		case xml.StartElement:
			if !isWordElement(element.Name) {
				continue
			}
			switch element.Name.Local {
			case "t":
				inText = true
			case "tab":
				paragraph.WriteString("\t")
			case "br", "cr":
				paragraph.WriteString("\n")
			}

		case xml.EndElement:
			if !isWordElement(element.Name) {
				continue
			}
			switch element.Name.Local {
			case "t":
				inText = false
			case "p":
				flush()
			}
		}
	}
	if paragraph.Len() > 0 {
		flush()
	}

	return sanitiseText(strings.TrimRight(out.String(), "\n")), truncated, nil
}

// isWordElement reports whether a name belongs to the WordprocessingML
// namespace.
//
// Checked rather than assumed: a document.xml embeds several other vocabularies
// - drawings, maths, custom XML - and more than one of them has an element
// called "t". Matching on the local name alone would pull their contents into
// the text.
func isWordElement(name xml.Name) bool {
	return name.Space == "" ||
		strings.Contains(name.Space, "wordprocessingml")
}
