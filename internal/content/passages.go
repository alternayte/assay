// Package content stores source items: versions, passages and chunks. It
// splits the normalized Markdown body of an item into passages, groups the
// passages into chunks, and writes a version only when the content changes.
package content

import (
	"crypto/sha256"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/yuin/goldmark"
	"github.com/yuin/goldmark/ast"
	"github.com/yuin/goldmark/extension"
	extast "github.com/yuin/goldmark/extension/ast"
	"github.com/yuin/goldmark/text"
	"golang.org/x/text/unicode/norm"
)

// MaxPassageChars is the size above which a block splits into several
// passages (REQ-SYN-004).
const MaxPassageChars = 1500

// Passage is one block of an item.
type Passage struct {
	Ordinal int
	// Section counts the top-level sections. Passages before the first
	// heading are in section 0.
	Section     int
	Kind        string // paragraph, list, table, code or quote
	HeadingPath []string
	// Text is the Markdown of the block.
	Text string
	// Norm is the plain text of the block: NFC, no inline marks, collapsed
	// whitespace. Hash is its SHA-256. Quotes match against Norm.
	Norm string
	Hash []byte
}

var markdown = goldmark.New(goldmark.WithExtensions(extension.Table, extension.Strikethrough, extension.TaskList))

// Normalize returns the hash input of a text: NFC with collapsed whitespace.
func Normalize(s string) string {
	return strings.Join(strings.Fields(norm.NFC.String(s)), " ")
}

// HashOf is the passage hash of a normalized text.
func HashOf(normalized string) []byte {
	h := sha256.Sum256([]byte(normalized))
	return h[:]
}

type heading struct {
	level int
	text  string
}

// Split returns the passages of a Markdown body. Headings set the heading
// path and are not passages. Thematic breaks are dropped.
func Split(body string) []Passage {
	src := []byte(body)
	doc := markdown.Parser().Parse(text.NewReader(src))
	var stack []heading
	var out []Passage
	sec := 0
	blocks := []ast.Node{}
	for n := doc.FirstChild(); n != nil; n = n.NextSibling() {
		blocks = append(blocks, n)
	}
	for i, n := range blocks {
		if h, ok := n.(*ast.Heading); ok {
			for len(stack) > 0 && stack[len(stack)-1].level >= h.Level {
				stack = stack[:len(stack)-1]
			}
			if t := Normalize(plain(h, src)); t != "" {
				if len(stack) == 0 {
					sec++
				}
				stack = append(stack, heading{h.Level, t})
			}
			continue
		}
		kind := kindOf(n)
		if kind == "" {
			continue
		}
		path := make([]string, len(stack))
		for j, h := range stack {
			path[j] = h.text
		}
		md := blockSource(n, blocks, i, src)
		var normText string
		if kind == "code" {
			normText = Normalize(codeText(n, src))
		} else {
			normText = Normalize(plain(n, src))
		}
		if normText == "" {
			continue
		}
		if utf8.RuneCountInString(md) <= MaxPassageChars {
			out = append(out, Passage{Section: sec, Kind: kind, HeadingPath: path, Text: md, Norm: normText})
			continue
		}
		for _, piece := range splitText(md, MaxPassageChars) {
			pn := Normalize(piece)
			if kind != "code" {
				pn = Normalize(plainOf(piece))
			}
			if pn != "" {
				out = append(out, Passage{Section: sec, Kind: kind, HeadingPath: path, Text: piece, Norm: pn})
			}
		}
	}
	for i := range out {
		out[i].Ordinal = i
		out[i].Hash = HashOf(out[i].Norm)
	}
	return out
}

func kindOf(n ast.Node) string {
	switch n.(type) {
	case *ast.Paragraph, *ast.TextBlock, *ast.HTMLBlock:
		return "paragraph"
	case *ast.List:
		return "list"
	case *extast.Table:
		return "table"
	case *ast.FencedCodeBlock, *ast.CodeBlock:
		return "code"
	case *ast.Blockquote:
		return "quote"
	}
	return ""
}

// blockSource returns the Markdown lines of block i: from its first line to
// the line before the next block.
func blockSource(n ast.Node, blocks []ast.Node, i int, src []byte) string {
	start := firstOffset(n)
	if start < 0 {
		return ""
	}
	start = lineStart(src, start)
	if _, fenced := n.(*ast.FencedCodeBlock); fenced {
		// The first segment is the first content line, or the info string
		// on the fence line. Step back to the fence.
		if !isFence(src[start:]) && start > 0 {
			start = lineStart(src, start-1)
		}
	}
	end := len(src)
	for j := i + 1; j < len(blocks); j++ {
		if o := firstOffset(blocks[j]); o >= 0 {
			end = lineStart(src, o)
			if _, fenced := blocks[j].(*ast.FencedCodeBlock); fenced && !isFence(src[end:]) && end > 0 {
				end = lineStart(src, end-1)
			}
			break
		}
	}
	if end < start {
		end = start
	}
	lines := strings.Split(strings.TrimSpace(string(src[start:end])), "\n")
	for len(lines) > 1 && (thematic.MatchString(lines[len(lines)-1]) || strings.TrimSpace(lines[len(lines)-1]) == "") {
		lines = lines[:len(lines)-1]
	}
	return strings.Join(lines, "\n")
}

var thematic = regexp.MustCompile(`^ {0,3}(?:(?:-\s*){3,}|(?:\*\s*){3,}|(?:_\s*){3,})$`)

func isFence(b []byte) bool {
	s := strings.TrimLeft(string(b), " ")
	return strings.HasPrefix(s, "```") || strings.HasPrefix(s, "~~~")
}

func lineStart(src []byte, off int) int {
	for off > 0 && src[off-1] != '\n' {
		off--
	}
	return off
}

// firstOffset is the smallest source offset of a node or its descendants.
func firstOffset(n ast.Node) int {
	best := -1
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			return ast.WalkContinue, nil
		}
		off := -1
		if t, ok := c.(*ast.Text); ok {
			off = t.Segment.Start
		} else if c.Type() == ast.TypeBlock && c.Lines().Len() > 0 {
			off = c.Lines().At(0).Start
		}
		if fc, ok := c.(*ast.FencedCodeBlock); ok && fc.Info != nil {
			off = fc.Info.Segment.Start
		}
		if off >= 0 && (best < 0 || off < best) {
			best = off
		}
		return ast.WalkContinue, nil
	})
	return best
}

var tag = regexp.MustCompile(`<[^>]*>`)

// plain returns the text of a node without inline marks.
func plain(n ast.Node, src []byte) string {
	var b strings.Builder
	_ = ast.Walk(n, func(c ast.Node, entering bool) (ast.WalkStatus, error) {
		if !entering {
			if c.Type() == ast.TypeBlock || isCell(c) {
				b.WriteByte(' ')
			}
			return ast.WalkContinue, nil
		}
		switch v := c.(type) {
		case *ast.Text:
			b.Write(v.Segment.Value(src))
			if v.SoftLineBreak() || v.HardLineBreak() {
				b.WriteByte(' ')
			}
		case *ast.String:
			b.Write(v.Value)
		case *ast.AutoLink:
			b.Write(v.URL(src))
			return ast.WalkSkipChildren, nil
		case *ast.RawHTML:
			return ast.WalkSkipChildren, nil
		case *ast.HTMLBlock:
			for i := 0; i < v.Lines().Len(); i++ {
				seg := v.Lines().At(i)
				b.WriteString(tag.ReplaceAllString(string(seg.Value(src)), " "))
			}
		case *ast.FencedCodeBlock, *ast.CodeBlock:
			b.WriteString(codeText(c, src))
			return ast.WalkSkipChildren, nil
		case *extast.TaskCheckBox:
			if v.IsChecked {
				b.WriteString("[x] ")
			} else {
				b.WriteString("[ ] ")
			}
		}
		return ast.WalkContinue, nil
	})
	return b.String()
}

func isCell(n ast.Node) bool {
	_, ok := n.(*extast.TableCell)
	return ok
}

func codeText(n ast.Node, src []byte) string {
	var b strings.Builder
	for i := 0; i < n.Lines().Len(); i++ {
		seg := n.Lines().At(i)
		b.Write(seg.Value(src))
	}
	return b.String()
}

// plainOf parses a Markdown fragment and returns its plain text.
func plainOf(md string) string {
	src := []byte(md)
	return plain(markdown.Parser().Parse(text.NewReader(src)), src)
}

// splitText cuts a text into pieces of at most max runes: at line ends
// first, then at sentence ends, then at spaces.
func splitText(s string, max int) []string {
	var out []string
	var cur strings.Builder
	flush := func() {
		if t := strings.TrimSpace(cur.String()); t != "" {
			out = append(out, t)
		}
		cur.Reset()
	}
	for _, line := range strings.SplitAfter(s, "\n") {
		if utf8.RuneCountInString(cur.String())+utf8.RuneCountInString(line) <= max {
			cur.WriteString(line)
			continue
		}
		flush()
		for utf8.RuneCountInString(line) > max {
			cut := cutPoint(line, max)
			out = append(out, strings.TrimSpace(line[:cut]))
			line = line[cut:]
		}
		cur.WriteString(line)
	}
	flush()
	return out
}

// cutPoint returns a byte offset at or before max runes: after the last
// sentence end, else at the last space, else at max runes.
func cutPoint(s string, max int) int {
	limit := len(s)
	n := 0
	for i := range s {
		if n == max {
			limit = i
			break
		}
		n++
	}
	head := s[:limit]
	if i := strings.LastIndex(head, ". "); i > limit/2 {
		return i + 2
	}
	if i := strings.LastIndexByte(head, ' '); i > 0 {
		return i + 1
	}
	return limit
}
