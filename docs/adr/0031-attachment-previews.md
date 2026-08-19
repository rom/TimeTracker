# ADR-0031: Attachments are previewed inline, and SVG is made safe rather than refused

* **Status:** accepted
* **Date:** 2026-08-19
* **Addresses:** ASR-013

## Context

Attachments could be uploaded and counted, and that was all. There was no screen
listing them, no way to reach the download or delete routes that existed, and no
way to see what a file was without saving it and opening it in something else.
For a receipt attached to an expense — the main reason attachments exist — the
question being asked is almost always "is this the right one", and downloading
is a poor way to answer it.

Four of the six formats worth previewing need nothing but the right element.
Two do not:

* **TIFF.** No browser except Safari renders one. Office scanners still produce
  them in quantity.
* **DOCX.** No browser renders one and none ever will.

And one is a security decision rather than a technical one. **SVG was refused
outright at upload**, on the grounds recorded in ADR-0013 and SECURITY.md: it is
script-capable, and a stored SVG served inline is a stored cross-site scripting
vector. Both documents said "refused **or rasterised**", so rasterising was
always contemplated; there is no pure-Go SVG renderer worth the name, so it
never happened.

## Decision

**A preview route serves attachment bytes inline, behind headers that make the
content inert. SVG is accepted and stored, and its safety comes from how it is
served rather than from inspecting it.**

`GET /attachments/{id}/preview` authorises exactly as the download route does —
against the owning record, in the service layer — and then sets:

```
Content-Security-Policy: default-src 'none'; style-src 'unsafe-inline'; sandbox
Content-Type:            <the type determined at upload>
X-Content-Type-Options:  nosniff
Content-Disposition:     inline
```

`sandbox` with no tokens puts the response in a unique origin with scripts,
forms, popups and plugins disabled. `default-src 'none'` stops it fetching
anything. The page then renders an SVG in an `<img>`, where no browser runs
script whatever the file contains.

That is two independent locks, deliberately. Replacing a blanket refusal
deserves more than one: the `<img>` alone is sufficient for the page's own
rendering, and the response policy is what holds if somebody navigates straight
at the URL. Both were verified against a hostile SVG carrying a `<script>` that
would have fired.

The download route is unchanged and still sends `Content-Disposition:
attachment`. Nothing is served as a document.

**TIFF is transcoded to PNG** on the way out, bounded by a pixel count checked
from the header before anything is allocated — the file-size limit does not
bound a decoded image, and a compressed TIFF expands enormously.

**DOCX yields its text**, walked out of `word/document.xml`, and the interface
labels it an extract rather than a preview: the layout, tables and images are
gone, and a preview that quietly loses them should say so.

## Consequences

**Positive**

* Attachments finally have a screen: listed, previewed, downloadable, and
  removable. The delete and download routes are reachable.
* A scanned TIFF receipt is viewable without leaving the application.
* An SVG diagram can be attached at all, which it could not before.

**Negative / accepted costs**

* One route serves stored bytes inline, where previously none did. It is the
  single place this is true and every header on it is load-bearing; a change to
  any of them is a security change.
* A TIFF is decoded and re-encoded on every view. There is no cache beyond the
  `private, max-age=86400` on the response. Scanned receipts are small and the
  work is local; a cache would be a second store to keep consistent.
* A DOCX extract is not a rendering and can mislead somebody who expects one.
  Mitigated by labelling it, not by pretending otherwise.
* Accepting SVG means the store now holds a format that is dangerous if ever
  served as a document. The guard is in one handler rather than spread out,
  which is the strongest arrangement available, but it is a guard rather than an
  impossibility.

## Alternatives considered

**Keep refusing SVG.** Simplest and safest, and it makes the tool worse at
something people reasonably want. The refusal predated any inline serving at
all; once one exists and is locked down, the refusal is caution rather than
protection.

**Sanitise SVG on upload** — strip `<script>`, event handlers, `<foreignObject>`
and external references. A denylist against a format with an enormous surface,
where every miss is a stored XSS. Not rendering it as a document at all is a
property, not a filter.

**Rasterise SVG to PNG**, as SECURITY.md contemplated. No pure-Go renderer
covers enough of the specification to produce a faithful image, and a wrong
picture of a diagram is worse than none. Rendering it in an `<img>` gets the
browser's own renderer with the same guarantee.

**Render PDFs ourselves.** An enormous amount of work to produce something worse
than the viewer already in every browser.

## Related

* ADR-0013 — attachment storage
* ADR-0007 — pure-Go document generation
