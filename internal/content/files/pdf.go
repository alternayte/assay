package files

import (
	"bytes"
	"errors"
	"fmt"
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/ledongthuc/pdf"
)

// PDFMarkdown returns the text of a PDF. Each page is a section "Page N",
// and lines with a wider gap than the usual line gap start a new
// paragraph. A PDF without text gives ErrNoText, a PDF that needs a
// password ErrEncrypted, and a PDF that the parser cannot read
// ErrUnreadable.
func PDFMarkdown(b []byte) (md string, err error) {
	// The parser panics on some malformed files.
	defer func() {
		if r := recover(); r != nil {
			md, err = "", fmt.Errorf("%w: %v", ErrUnreadable, r)
		}
	}()
	r, err := pdf.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		if errors.Is(err, pdf.ErrInvalidPassword) || strings.Contains(err.Error(), "encrypt") {
			return "", fmt.Errorf("%w: %w", ErrEncrypted, err)
		}
		return "", fmt.Errorf("%w: %w", ErrUnreadable, err)
	}
	var out []string
	size, text := 0, false
	for n := 1; n <= r.NumPage(); n++ {
		p := r.Page(n)
		if p.V.IsNull() {
			continue
		}
		paras := paragraphs(p.Content().Text)
		out = append(out, heading(1, "Page "+strconv.Itoa(n)))
		for _, para := range paras {
			size += len(para)
			if size > maxText {
				return "", fmt.Errorf("%w: the text is larger than %d bytes", ErrUnreadable, maxText)
			}
			out = append(out, escape(para))
			text = true
		}
	}
	if !text {
		return "", ErrNoText
	}
	return strings.Join(out, "\n\n"), nil
}

// line is one line of text of a page.
type line struct {
	y, size float64
	text    string
}

// lines joins the glyphs of a page into lines, top first. A glyph that
// starts after a gap of more than a fifth of the font size starts a word.
func lines(glyphs []pdf.Text) []line {
	gs := slices.Clone(glyphs)
	slices.SortStableFunc(gs, func(a, b pdf.Text) int {
		if math.Abs(a.Y-b.Y) > max(a.FontSize, b.FontSize, 1)/2 {
			if a.Y > b.Y {
				return -1
			}
			return 1
		}
		switch {
		case a.X < b.X:
			return -1
		case a.X > b.X:
			return 1
		}
		return 0
	})
	var out []line
	var b strings.Builder
	var cur *line
	var prev pdf.Text
	flush := func() {
		if cur != nil {
			if t := strings.Join(strings.Fields(b.String()), " "); t != "" {
				cur.text = t
				out = append(out, *cur)
			}
		}
		cur = nil
		b.Reset()
	}
	for _, g := range gs {
		if cur != nil && math.Abs(g.Y-cur.y) > max(g.FontSize, cur.size, 1)/2 {
			flush()
		}
		if cur == nil {
			cur = &line{y: g.Y, size: g.FontSize}
		} else if g.X > prev.X+prev.W+max(g.FontSize, 1)/5 {
			b.WriteString(" ")
		}
		b.WriteString(g.S)
		prev = g
	}
	flush()
	return out
}

// paragraphs joins the lines of a page into paragraphs. A gap between two
// lines of more than 1.5 times the smallest gap of the page starts a new
// paragraph.
func paragraphs(glyphs []pdf.Text) []string {
	ls := lines(glyphs)
	gap := 0.0
	for i := 1; i < len(ls); i++ {
		if d := ls[i-1].y - ls[i].y; d > 0 && (gap == 0 || d < gap) {
			gap = d
		}
	}
	var out []string
	var cur []string
	for i, l := range ls {
		if i > 0 && gap > 0 && ls[i-1].y-l.y > gap*1.5 {
			out = append(out, strings.Join(cur, " "))
			cur = nil
		}
		cur = append(cur, l.text)
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, " "))
	}
	return out
}
