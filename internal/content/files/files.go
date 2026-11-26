// Package files extracts the text of attached files as Markdown: the pages
// of a PDF, the headings, paragraphs and tables of a Word document, and the
// labels of a draw.io diagram. Connectors that read files call it.
package files

import (
	"errors"
	"path"
	"regexp"
	"strings"
)

// The file types that Assay reads.
const (
	PDF    = "pdf"
	DOCX   = "docx"
	DrawIO = "drawio"
)

// Types are the file types that Assay reads, in the order the settings show
// them.
var Types = []string{PDF, DOCX, DrawIO}

// Errors of an extraction. Reason gives the skip reason of each.
var (
	// ErrNoText is a file without text, such as a scanned PDF.
	ErrNoText = errors.New("files: the file has no text")
	// ErrEncrypted is a PDF that needs a password.
	ErrEncrypted = errors.New("files: the file is encrypted")
	// ErrUnreadable is a file that the parser cannot read.
	ErrUnreadable = errors.New("files: the file cannot be read")
)

// Reason is the skip reason of an extraction error: no_text, encrypted or
// unreadable.
func Reason(err error) string {
	switch {
	case errors.Is(err, ErrNoText):
		return "no_text"
	case errors.Is(err, ErrEncrypted):
		return "encrypted"
	default:
		return "unreadable"
	}
}

// TypeOf returns the file type of a file name and a media type, or "" for a
// type that Assay does not read. The draw.io app of Confluence stores
// diagrams with its media type and often no extension.
func TypeOf(name, mediaType string) string {
	mt, _, _ := strings.Cut(strings.ToLower(strings.TrimSpace(mediaType)), ";")
	switch strings.TrimSpace(mt) {
	case "application/pdf":
		return PDF
	case "application/vnd.openxmlformats-officedocument.wordprocessingml.document":
		return DOCX
	case "application/vnd.jgraph.mxfile":
		return DrawIO
	}
	switch strings.ToLower(path.Ext(name)) {
	case ".pdf":
		return PDF
	case ".docx":
		return DOCX
	case ".drawio":
		return DrawIO
	}
	return ""
}

// Markdown returns the text of a file of a type as Markdown.
func Markdown(typ string, b []byte) (string, error) {
	switch typ {
	case PDF:
		return PDFMarkdown(b)
	case DOCX:
		return DOCXMarkdown(b)
	case DrawIO:
		return DrawIOMarkdown(b)
	}
	return "", ErrUnreadable
}

// maxText bounds the uncompressed text that one file gives.
const maxText = 32 << 20

var inlineSpecial = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", `\<`, ">", `\>`, "|", `\|`)

// blockStart matches the start of a line that Markdown reads as a block
// mark: a heading, a list item or a thematic break.
var blockStart = regexp.MustCompile(`^(#|[-+=]|\d+[.)])`)

// escape makes text plain Markdown text, with whitespace collapsed.
func escape(s string) string {
	s = inlineSpecial.Replace(strings.Join(strings.Fields(s), " "))
	m := blockStart.FindString(s)
	switch {
	case m == "":
	case m[0] >= '0' && m[0] <= '9':
		// Only punctuation takes a backslash: 1\. is plain text.
		s = m[:len(m)-1] + `\` + s[len(m)-1:]
	default:
		s = `\` + s
	}
	return s
}

// heading is a Markdown heading of a level from 1 to 6.
func heading(level int, text string) string {
	level = min(max(level, 1), 6)
	return strings.Repeat("#", level) + " " + escape(text)
}
