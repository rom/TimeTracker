# ADR-0007: Pure-Go PDF and DOCX generation

* **Status:** accepted
* **Date:** 2026-08-18
* **Addresses:** ASR-007, ASR-002, ASR-003

## Context

Reports must be exportable as PDF, CSV, DOCX and JSON. CSV and JSON are trivial with
the standard library. PDF and DOCX are not, and the usual solutions are all external
processes: headless Chrome (print-to-PDF from HTML), wkhtmltopdf, LaTeX, or
LibreOffice in headless mode for DOCX.

Every one of those violates ASR-003 (no runtime dependencies) and complicates
ASR-002 (three platforms): the user would have to install a browser or an office
suite, and the app would have to locate it on three operating systems and cope with
version differences.

## Decision

Both formats are generated **in-process, in pure Go**.

* **PDF** — a Go PDF library, driving a small internal layout layer that knows about
  the report structures we actually produce (title block, meta table, grouped entry
  table with repeating headers across page breaks, totals, footer with page numbers).
  Fonts are embedded so output is identical on every platform.
* **DOCX** — written directly as Office Open XML. A `.docx` is a ZIP containing a
  handful of XML parts; we generate them from templates using `archive/zip` and
  `encoding/xml` from the standard library. This is less code than it sounds for the
  document shapes we need (headings, paragraphs, tables, page headers), and it adds
  no dependency at all.
* **CSV** — `encoding/csv`, UTF-8 with a BOM so Excel on Windows detects the encoding
  and does not mangle non-ASCII customer names.
* **JSON** — `encoding/json` against a documented, versioned schema, so exports can
  be consumed by an invoicing system without screen-scraping.

All four formats are produced from a single in-memory `Report` value, so they cannot
disagree with each other about the numbers. Rendering is streamed to the HTTP
response where the format allows.

## Consequences

**Positive**

* No external binaries, so ASR-002 and ASR-003 hold; a user double-clicking the app
  on Windows gets working PDF export with nothing else installed.
* Deterministic output — the same report renders byte-comparably, which makes golden
  tests possible.
* No process spawning means no shell-injection surface and no sandbox escape via a
  browser engine.

**Negative / accepted costs**

* Layout is our problem. Anything a designer would express in CSS — precise
  typography, complex multi-column layouts — has to be built by hand against a
  drawing API. Our reports are tabular, which is the case this suits, but a
  visually elaborate invoice template would be painful.
* The DOCX writer is bespoke code we own and must test against real Word and
  LibreOffice, not just against the specification.
* Non-Latin scripts need appropriate embedded fonts; we ship a font covering Latin,
  and document how to supply another for other scripts.

## Alternatives considered

**Headless Chrome / wkhtmltopdf print-to-PDF** — reuses the HTML and CSS we already
have, giving the best-looking output for the least layout code, and themes would
carry into the PDF. Rejected on ASR-003: a ~150 MB browser dependency, three
platform-specific discovery paths, and a heavyweight subprocess per export.

**LibreOffice headless for DOCX** — robust and standards-correct. Rejected for the
same dependency reason, and it is slow to start per export.

**Typst or LaTeX** — excellent typography. Rejected: another toolchain to install.

**Generate RTF instead of DOCX** — much simpler to emit and Word opens it. Rejected:
users asked for `.docx`, and RTF looks dated in a document sent to a client.

## Related

* ADR-0003 (no cgo), ADR-0014 (exact money in totals)
