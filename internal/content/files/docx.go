package files

import (
	"archive/zip"
	"bytes"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
)

// DOCXMarkdown returns the text of a Word document from word/document.xml.
// The paragraph styles Heading1 to Heading6 become headings, tables become
// Markdown tables, and other paragraphs become paragraphs.
func DOCXMarkdown(b []byte) (string, error) {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	var doc *zip.File
	for _, f := range zr.File {
		if f.Name == "word/document.xml" {
			doc = f
		}
	}
	if doc == nil {
		return "", fmt.Errorf("%w: the file has no word/document.xml", ErrUnreadable)
	}
	rc, err := doc.Open()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	defer func() { _ = rc.Close() }()
	d := &docx{dec: xml.NewDecoder(&limited{r: rc, n: maxText})}
	blocks, err := d.body()
	if err != nil {
		return "", fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	text := false
	for _, bl := range blocks {
		if !strings.HasPrefix(bl, "#") {
			text = true
		}
	}
	if !text {
		return "", ErrNoText
	}
	return strings.Join(blocks, "\n\n"), nil
}

// limited is a reader that fails after n bytes, so that a compressed
// document cannot expand without bound.
type limited struct {
	r io.Reader
	n int64
}

func (l *limited) Read(p []byte) (int, error) {
	if l.n <= 0 {
		return 0, errors.New("the document is larger than the limit")
	}
	if int64(len(p)) > l.n {
		p = p[:l.n]
	}
	n, err := l.r.Read(p)
	l.n -= int64(n)
	return n, err
}

// docx reads the elements of word/document.xml.
type docx struct {
	dec *xml.Decoder
}

// body returns the blocks of w:body.
func (d *docx) body() ([]string, error) {
	var out []string
	for {
		tok, err := d.dec.Token()
		if errors.Is(err, io.EOF) {
			return out, nil
		}
		if err != nil {
			return nil, err
		}
		se, ok := tok.(xml.StartElement)
		if !ok {
			continue
		}
		switch se.Name.Local {
		case "p":
			level, text, err := d.paragraph()
			if err != nil {
				return nil, err
			}
			switch {
			case strings.TrimSpace(text) == "":
			case level > 0:
				out = append(out, heading(level, text))
			default:
				out = append(out, escape(text))
			}
		case "tbl":
			t, err := d.table()
			if err != nil {
				return nil, err
			}
			if t != "" {
				out = append(out, t)
			}
		}
	}
}

// paragraph reads a w:p up to its end. level is the heading level of its
// style, or 0.
func (d *docx) paragraph() (int, string, error) {
	var b strings.Builder
	level := 0
	depth := 1
	for depth > 0 {
		tok, err := d.dec.Token()
		if err != nil {
			return 0, "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			depth++
			switch t.Name.Local {
			case "pStyle":
				level = headingLevel(attr(t, "val"))
			case "t":
				var s string
				if err := d.dec.DecodeElement(&s, &t); err != nil {
					return 0, "", err
				}
				depth--
				b.WriteString(s)
			case "tab", "br", "cr":
				b.WriteString(" ")
			}
		case xml.EndElement:
			depth--
		}
	}
	return level, b.String(), nil
}

// table reads a w:tbl up to its end as a Markdown table. The first row is
// the header.
func (d *docx) table() (string, error) {
	var rows [][]string
	var row []string
	var cell []string
	depth := 1
	for depth > 0 {
		tok, err := d.dec.Token()
		if err != nil {
			return "", err
		}
		switch t := tok.(type) {
		case xml.StartElement:
			switch t.Name.Local {
			case "tr":
				row = nil
				depth++
			case "tc":
				cell = nil
				depth++
			case "p":
				_, text, err := d.paragraph()
				if err != nil {
					return "", err
				}
				if s := strings.TrimSpace(text); s != "" {
					cell = append(cell, s)
				}
			case "tbl":
				// A nested table becomes the text of its cell.
				inner, err := d.table()
				if err != nil {
					return "", err
				}
				if inner != "" {
					cell = append(cell, strings.ReplaceAll(inner, "\n", " "))
				}
			default:
				depth++
			}
		case xml.EndElement:
			depth--
			switch t.Name.Local {
			case "tc":
				row = append(row, escape(strings.Join(cell, " ")))
			case "tr":
				rows = append(rows, row)
			}
		}
	}
	cols := 0
	empty := true
	for _, r := range rows {
		cols = max(cols, len(r))
		for _, c := range r {
			if c != "" {
				empty = false
			}
		}
	}
	if cols == 0 || empty {
		return "", nil
	}
	var lines []string
	for i, r := range rows {
		for len(r) < cols {
			r = append(r, "")
		}
		lines = append(lines, "| "+strings.Join(r, " | ")+" |")
		if i == 0 {
			lines = append(lines, "|"+strings.Repeat("---|", cols))
		}
	}
	return strings.Join(lines, "\n"), nil
}

// headingLevel is the level of a heading style ID, Heading1 to Heading6,
// or 0.
func headingLevel(style string) int {
	n, ok := strings.CutPrefix(strings.ToLower(strings.ReplaceAll(style, " ", "")), "heading")
	if !ok {
		return 0
	}
	l, err := strconv.Atoi(n)
	if err != nil || l < 1 || l > 6 {
		return 0
	}
	return l
}

func attr(se xml.StartElement, local string) string {
	for _, a := range se.Attr {
		if a.Name.Local == local {
			return a.Value
		}
	}
	return ""
}
