package confluence

import (
	"fmt"
	"regexp"
	"strings"

	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// selfClosing matches the empty ac: and ri: elements of the storage format.
// The HTML parser would otherwise read the following siblings as children.
var selfClosing = regexp.MustCompile(`<((?:ac|ri):[\w-]+)([^<>]*?)\s*/>`)

// ToMarkdown converts Confluence storage format to Markdown. Images,
// attachments and navigation macros are dropped. Links keep only http, https
// and mailto targets.
func ToMarkdown(storage string) (string, error) {
	src := selfClosing.ReplaceAllString(storage, "<$1$2></$1>")
	nodes, err := html.ParseFragment(strings.NewReader(src), &html.Node{Type: html.ElementNode, Data: "body", DataAtom: atom.Body})
	if err != nil {
		return "", err
	}
	var blocks []string
	for _, n := range nodes {
		blocks = append(blocks, block(n)...)
	}
	return strings.Join(blocks, "\n\n"), nil
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

// param returns the value of a macro parameter.
func param(n *html.Node, name string) string {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Data == "ac:parameter" && attr(c, "ac:name") == name {
			return strings.TrimSpace(textOf(c))
		}
	}
	return ""
}

func child(n *html.Node, name string) *html.Node {
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if c.Data == name {
			return c
		}
	}
	return nil
}

// textOf returns the raw text of a node, CDATA included.
func textOf(n *html.Node) string {
	var b strings.Builder
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		switch n.Type {
		case html.TextNode:
			b.WriteString(n.Data)
		case html.CommentNode:
			if s, ok := strings.CutPrefix(n.Data, "[CDATA["); ok {
				b.WriteString(strings.TrimSuffix(s, "]]"))
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return b.String()
}

var dropped = map[string]bool{
	"toc": true, "children": true, "pagetree": true, "attachments": true, "recently-updated": true,
	"contentbylabel": true, "include": true, "excerpt-include": true, "anchor": true, "gallery": true,
	"livesearch": true, "view-file": true, "viewpdf": true, "multimedia": true, "profile": true,
}

var panels = map[string]string{"info": "Info", "note": "Note", "warning": "Warning", "tip": "Tip", "panel": ""}

// blocks converts the children of n.
func blocks(n *html.Node) []string {
	var out []string
	var inline []*html.Node
	flush := func() {
		if t := strings.TrimSpace(inlines(inline, false)); t != "" {
			out = append(out, t)
		}
		inline = nil
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if isBlock(c) {
			flush()
			out = append(out, block(c)...)
			continue
		}
		inline = append(inline, c)
	}
	flush()
	return out
}

func isBlock(n *html.Node) bool {
	if n.Type != html.ElementNode {
		return false
	}
	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6", "p", "ul", "ol", "table", "pre", "blockquote", "hr", "div", "section",
		"ac:layout", "ac:layout-section", "ac:layout-cell", "ac:rich-text-body", "ac:task-list", "ac:image", "img", "script", "style":
		return true
	case "ac:structured-macro":
		name := attr(n, "ac:name")
		return name != "status" && name != "jira"
	}
	return false
}

func block(n *html.Node) []string {
	if n.Type == html.TextNode {
		if t := strings.TrimSpace(collapse(n.Data)); t != "" {
			return []string{escape(t, false)}
		}
		return nil
	}
	if n.Type != html.ElementNode {
		return nil
	}
	switch n.Data {
	case "h1", "h2", "h3", "h4", "h5", "h6":
		if t := strings.TrimSpace(inlines(children(n), false)); t != "" {
			return []string{strings.Repeat("#", int(n.Data[1]-'0')) + " " + t}
		}
		return nil
	case "p":
		if t := strings.TrimSpace(inlines(children(n), false)); t != "" {
			return []string{t}
		}
		return nil
	case "ul", "ol":
		if l := list(n, 0); l != "" {
			return []string{l}
		}
		return nil
	case "ac:task-list":
		var lines []string
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if c.Data != "ac:task" {
				continue
			}
			box := "[ ]"
			if st := child(c, "ac:task-status"); st != nil && strings.TrimSpace(textOf(st)) == "complete" {
				box = "[x]"
			}
			body := ""
			if b := child(c, "ac:task-body"); b != nil {
				body = strings.TrimSpace(inlines(children(b), false))
			}
			lines = append(lines, "- "+box+" "+body)
		}
		return []string{strings.Join(lines, "\n")}
	case "table":
		if t := table(n); t != "" {
			return []string{t}
		}
		return nil
	case "pre":
		return []string{fence("", textOf(n))}
	case "blockquote":
		return []string{quote(blocks(n))}
	case "hr", "ac:image", "img", "script", "style":
		return nil
	case "ac:structured-macro":
		return macro(n)
	}
	return blocks(n)
}

func macro(n *html.Node) []string {
	name := attr(n, "ac:name")
	switch {
	case dropped[name]:
		return nil
	case name == "code" || name == "noformat":
		body := ""
		if b := child(n, "ac:plain-text-body"); b != nil {
			body = textOf(b)
		}
		return []string{fence(param(n, "language"), body)}
	case name == "expand":
		var out []string
		if title := param(n, "title"); title != "" {
			out = append(out, "**"+escape(title, false)+"**")
		}
		if b := child(n, "ac:rich-text-body"); b != nil {
			out = append(out, blocks(b)...)
		}
		return out
	}
	if label, ok := panels[name]; ok {
		head := label
		if title := param(n, "title"); title != "" {
			if head != "" {
				head += ": "
			}
			head += title
		}
		var inner []string
		if head != "" {
			inner = append(inner, "**"+escape(head, false)+"**")
		}
		if b := child(n, "ac:rich-text-body"); b != nil {
			inner = append(inner, blocks(b)...)
		}
		return []string{quote(inner)}
	}
	if b := child(n, "ac:rich-text-body"); b != nil {
		return blocks(b)
	}
	if b := child(n, "ac:plain-text-body"); b != nil {
		if t := strings.TrimSpace(textOf(b)); t != "" {
			return []string{fence("", t)}
		}
	}
	return nil
}

func children(n *html.Node) []*html.Node {
	var out []*html.Node
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		out = append(out, c)
	}
	return out
}

func fence(lang, body string) string {
	body = strings.Trim(body, "\n")
	ticks := "```"
	for strings.Contains(body, ticks) {
		ticks += "`"
	}
	return ticks + strings.TrimSpace(lang) + "\n" + body + "\n" + ticks
}

func quote(bs []string) string {
	var lines []string
	for i, b := range bs {
		if i > 0 {
			lines = append(lines, ">")
		}
		for _, l := range strings.Split(b, "\n") {
			lines = append(lines, strings.TrimRight("> "+l, " "))
		}
	}
	return strings.Join(lines, "\n")
}

func list(n *html.Node, depth int) string {
	var lines []string
	num := 0
	for li := n.FirstChild; li != nil; li = li.NextSibling {
		if li.Data != "li" {
			continue
		}
		num++
		marker := "- "
		if n.Data == "ol" {
			marker = fmt.Sprintf("%d. ", num)
		}
		var text []*html.Node
		var nested []string
		for c := li.FirstChild; c != nil; c = c.NextSibling {
			switch c.Data {
			case "ul", "ol":
				if l := list(c, depth+1); l != "" {
					nested = append(nested, l)
				}
			case "p":
				text = append(text, children(c)...)
				text = append(text, &html.Node{Type: html.TextNode, Data: " "})
			default:
				text = append(text, c)
			}
		}
		indent := strings.Repeat("  ", depth)
		lines = append(lines, indent+marker+strings.TrimSpace(inlines(text, false)))
		lines = append(lines, nested...)
	}
	return strings.Join(lines, "\n")
}

func table(n *html.Node) string {
	var rows [][]string
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Data == "tr" {
			var cells []string
			for c := x.FirstChild; c != nil; c = c.NextSibling {
				if c.Data == "th" || c.Data == "td" {
					cells = append(cells, strings.TrimSpace(inlines(cellNodes(c), true)))
				}
			}
			rows = append(rows, cells)
			return
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	if len(rows) == 0 {
		return ""
	}
	cols := 0
	for _, r := range rows {
		cols = max(cols, len(r))
	}
	if cols == 0 {
		return ""
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
	return strings.Join(lines, "\n")
}

// cellNodes flattens the blocks of a cell into inline nodes with spaces.
func cellNodes(c *html.Node) []*html.Node {
	var out []*html.Node
	for x := c.FirstChild; x != nil; x = x.NextSibling {
		if x.Type == html.ElementNode && (x.Data == "p" || x.Data == "div" || x.Data == "ul" || x.Data == "ol" || x.Data == "li") {
			out = append(out, cellNodes(x)...)
			out = append(out, &html.Node{Type: html.TextNode, Data: " "})
			continue
		}
		out = append(out, x)
	}
	return out
}

var spaces = regexp.MustCompile(`\s+`)

func collapse(s string) string { return spaces.ReplaceAllString(s, " ") }

var mdSpecial = strings.NewReplacer(`\`, `\\`, "*", `\*`, "_", `\_`, "`", "\\`", "[", `\[`, "]", `\]`, "<", `\<`, ">", `\>`)

func escape(s string, cell bool) string {
	s = mdSpecial.Replace(s)
	if cell {
		s = strings.ReplaceAll(s, "|", `\|`)
	}
	return s
}

// inlines converts inline nodes. In a table cell, line breaks become spaces.
func inlines(ns []*html.Node, cell bool) string {
	var b strings.Builder
	for _, n := range ns {
		if n != nil {
			inline(&b, n, cell)
		}
	}
	out := strings.ReplaceAll(b.String(), " ", " ")
	return strings.TrimSpace(spaces.ReplaceAllStringFunc(out, func(s string) string {
		if strings.Contains(s, "\n") && !cell {
			return "\n"
		}
		return " "
	}))
}

func wrap(b *strings.Builder, mark string, n *html.Node, cell bool) {
	inner := strings.TrimSpace(inlines(children(n), cell))
	if inner == "" {
		return
	}
	b.WriteString(mark + inner + mark)
}

func inline(b *strings.Builder, n *html.Node, cell bool) {
	switch n.Type {
	case html.TextNode:
		b.WriteString(escape(collapse(n.Data), cell))
		return
	case html.ElementNode:
	default:
		return
	}
	switch n.Data {
	case "strong", "b":
		wrap(b, "**", n, cell)
	case "em", "i":
		wrap(b, "_", n, cell)
	case "s", "del", "strike":
		wrap(b, "~~", n, cell)
	case "code", "tt":
		if t := strings.TrimSpace(collapse(textOf(n))); t != "" {
			b.WriteString("`" + strings.ReplaceAll(t, "`", "'") + "`")
		}
	case "br":
		if cell {
			b.WriteString(" ")
		} else {
			b.WriteString("\\\n")
		}
	case "a":
		text := strings.TrimSpace(inlines(children(n), cell))
		href := attr(n, "href")
		switch {
		case text == "":
		case strings.HasPrefix(href, "http://") || strings.HasPrefix(href, "https://") || strings.HasPrefix(href, "mailto:"):
			b.WriteString("[" + text + "](" + strings.ReplaceAll(href, " ", "%20") + ")")
		default:
			b.WriteString(text)
		}
	case "ac:link":
		if body := child(n, "ac:link-body"); body != nil {
			b.WriteString(escape(strings.TrimSpace(collapse(textOf(body))), cell))
		} else if body := child(n, "ac:plain-text-link-body"); body != nil {
			b.WriteString(escape(strings.TrimSpace(collapse(textOf(body))), cell))
		} else if pg := child(n, "ri:page"); pg != nil {
			b.WriteString(escape(attr(pg, "ri:content-title"), cell))
		} else if at := child(n, "ri:attachment"); at != nil {
			b.WriteString(escape(attr(at, "ri:filename"), cell))
		}
	case "ac:structured-macro":
		switch attr(n, "ac:name") {
		case "status":
			if t := param(n, "title"); t != "" {
				b.WriteString("**" + escape(t, cell) + "**")
			}
		case "jira":
			b.WriteString(escape(param(n, "key"), cell))
		}
	case "time":
		b.WriteString(escape(attr(n, "datetime"), cell))
	case "ac:image", "img", "ac:emoticon", "ri:url", "ac:parameter", "script", "style":
	default:
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			inline(b, c, cell)
		}
	}
}
