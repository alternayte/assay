# Confluence attachments

## What it does
A Confluence connection reads the PDF, Word (DOCX) and draw.io attachments of the pages in its scope. Each attachment becomes a child item of its page, with its text as passages. Search, Ask, claims and get_item then cite the text of a PDF spec or the labels of an architecture diagram. This works on Confluence DC and on Confluence Cloud.

## Decisions
- An attachment is an item of the same connection with external ID `att:<attachment id>`, kind `file`, the file name as title, and the attachment version as revision — the visible set, tombstones and the revision skip then work with no new rule.
- The item metadata holds `parent` (the page ID), `parentTitle`, `mediaType` and `fileSize`. The heading path of each passage starts with the page title — no schema column is needed, and a result shows which page holds the file.
- `Changed` runs a second CQL query, `type=attachment` with the same space filter and `lastmodified`, after the pages — one listing finds the changed attachments of all pages, with no request per page.
- An attachment whose container page is not a stored item of the connection is not read — ancestor and label scopes then apply to attachments through their page.
- `ListIDs` also lists attachment IDs. Reconcile tombstones an attachment that is gone, and the tombstone of a page also tombstones its attachments.
- A new setting `attachments` shows in Settings, General: `types` (default `pdf`, `docx`, `drawio`) and `maxKb` (default 10240). An empty type list stops attachment reads — an admin controls cost and risk without a code change.
- The connector checks the file size of the listing before it downloads. A larger file is skipped with `too_large`, and a type that is not in the list is not read.
- The download is a GET to the download link of the attachment on the connection host, through the connector client — the GET-only and same-host rules stay.
- PDF text comes from `github.com/ledongthuc/pdf` (BSD-3-Clause, MIT-compatible). Each PDF page becomes a section `Page N` — a citation then names the page.
- A PDF with no text, such as a scan, is skipped with `no_text`. An encrypted PDF is skipped with `encrypted`. A PDF that the library cannot parse is skipped with `unreadable`, and the sync continues.
- DOCX text comes from `word/document.xml` with `archive/zip` and `encoding/xml`, with no new dependency. `Heading1` to `Heading6` styles become headings, tables become Markdown tables, and other paragraphs become paragraphs.
- A draw.io file is an `mxfile`. Each `diagram` element becomes a section named after its tab. A compressed diagram is decoded with base64, raw inflate (`compress/flate`) and URL-decoding. The labels of `mxCell@value` and `UserObject@label` become list items, with HTML removed — a diagram has no prose, and its labels name the systems in it.
- The draw.io Confluence app stores a diagram as an attachment with media type `application/vnd.jgraph.mxfile` and often no extension. Assay detects draw.io by that media type or the `.drawio` extension.
- Attachment items get acronym entities, as pages do. Decision and glossary labels do not apply to attachments.
- A page body shows an attachment link, a `viewpdf` macro or a `view-file` macro as the text `Attachment: <file name>` — the page passage then tells the reader that the file exists.
- Cloud lists attachments with the same CQL search as pages and downloads through the `downloadLink` of the attachment under `/wiki`.
- The extraction of text is in `internal/content/files` (`pdf.go`, `docx.go`, `drawio.go`), and each function returns Markdown — the repository connectors can use them later with no change.

## Out
- SharePoint, OneDrive and Google Drive sources.
- Gliffy, Visio, PPTX, XLSX and image text (OCR).
- Attachments of Jira issues.

## How I know it works
- The Acme fixture page "Payments architecture" has a PDF, a DOCX and a draw.io attachment. After a sync, search for a sentence of the PDF returns the PDF item with `Page 2` in its heading path.
- Ask "Which systems does the payments diagram show?" cites the draw.io item, and the answer names the labels of the diagram.
- A scanned PDF fixture shows in the run detail as skipped with `no_text`.
- With `maxKb` 1, every attachment shows as skipped with `too_large`, and the fixture server logs no download request.
- Delete the attachment from the fixture and reconcile. The attachment item is a tombstone, and the page stays.
- `go-licenses` (or the licence check of `just ci`) passes with the new PDF dependency.
