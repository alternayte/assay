package web

import (
	"bytes"
	"compress/gzip"
	"encoding/xml"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"strings"

	"github.com/JohannesKaufmann/html-to-markdown/v2/converter"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/base"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/commonmark"
	"github.com/JohannesKaufmann/html-to-markdown/v2/plugin/table"
	"golang.org/x/net/html"
	"golang.org/x/net/html/atom"
)

// dropped are the elements of site HTML that are not page content.
var dropped = map[atom.Atom]bool{
	atom.Nav: true, atom.Header: true, atom.Footer: true, atom.Aside: true, atom.Script: true, atom.Style: true,
	atom.Noscript: true, atom.Template: true, atom.Form: true, atom.Button: true, atom.Input: true, atom.Select: true,
	atom.Textarea: true, atom.Iframe: true, atom.Svg: true, atom.Img: true, atom.Picture: true, atom.Video: true,
	atom.Audio: true, atom.Canvas: true, atom.Object: true, atom.Embed: true,
}

// fromHTML converts a site page to Markdown. It reads <main>, else
// <article>, else <body>, and drops navigation, scripts, forms and media.
// Links keep only http, https and mailto targets, made absolute against
// the page URL. links are the targets of every <a href> of the page,
// navigation included, for the traversal.
func fromHTML(b []byte, page *url.URL) (title, body string, links []*url.URL, err error) {
	doc, err := html.Parse(bytes.NewReader(b))
	if err != nil {
		return "", "", nil, err
	}
	at := page
	if n := find(doc, atom.Base); n != nil {
		if u, err := page.Parse(attr(n, "href")); err == nil && attr(n, "href") != "" {
			at = u
		}
	}
	if n := find(doc, atom.Title); n != nil {
		title = collapse(text(n))
	}
	if title == "" {
		if n := find(doc, atom.H1); n != nil {
			title = collapse(text(n))
		}
	}
	walk(doc, func(n *html.Node) {
		if n.Type == html.ElementNode && n.DataAtom == atom.A {
			if u, ok := resolve(at, attr(n, "href")); ok && (u.Scheme == "http" || u.Scheme == "https") {
				links = append(links, u)
			}
		}
	})
	root := find(doc, atom.Main)
	if root == nil {
		root = find(doc, atom.Article)
	}
	if root == nil {
		root = find(doc, atom.Body)
	}
	if root == nil {
		root = doc
	}
	clean(root, at)
	var buf bytes.Buffer
	for c := root.FirstChild; c != nil; c = c.NextSibling {
		if err := html.Render(&buf, c); err != nil {
			return "", "", nil, err
		}
	}
	conv := converter.NewConverter(converter.WithPlugins(base.NewBasePlugin(), commonmark.NewCommonmarkPlugin(), table.NewTablePlugin()))
	md, err := conv.ConvertString(buf.String())
	if err != nil {
		return "", "", nil, fmt.Errorf("web: convert %s: %w", page, err)
	}
	return title, strings.TrimSpace(md), links, nil
}

// clean removes the dropped elements below n and rewrites its links.
func clean(n *html.Node, base *url.URL) {
	for c := n.FirstChild; c != nil; {
		next := c.NextSibling
		switch {
		case c.Type == html.CommentNode:
			n.RemoveChild(c)
		case c.Type == html.ElementNode && dropped[c.DataAtom]:
			n.RemoveChild(c)
		case c.Type == html.ElementNode && c.DataAtom == atom.A:
			clean(c, base)
			u, ok := resolve(base, attr(c, "href"))
			if ok && allowed(u) {
				setAttr(c, "href", u.String())
			} else {
				// The link text stays without the link.
				for gc := c.FirstChild; gc != nil; {
					gnext := gc.NextSibling
					c.RemoveChild(gc)
					n.InsertBefore(gc, c)
					gc = gnext
				}
				n.RemoveChild(c)
			}
		case c.Type == html.ElementNode:
			clean(c, base)
		}
		c = next
	}
}

func allowed(u *url.URL) bool {
	return u.Scheme == "http" || u.Scheme == "https" || u.Scheme == "mailto"
}

// resolve returns the absolute URL of a link target.
func resolve(base *url.URL, href string) (*url.URL, bool) {
	href = strings.TrimSpace(href)
	if href == "" {
		return nil, false
	}
	u, err := base.Parse(href)
	if err != nil {
		return nil, false
	}
	u.Scheme = strings.ToLower(u.Scheme)
	return u, true
}

func find(n *html.Node, a atom.Atom) *html.Node {
	if n.Type == html.ElementNode && n.DataAtom == a {
		return n
	}
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		if f := find(c, a); f != nil {
			return f
		}
	}
	return nil
}

func walk(n *html.Node, fn func(*html.Node)) {
	fn(n)
	for c := n.FirstChild; c != nil; c = c.NextSibling {
		walk(c, fn)
	}
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if a.Key == key {
			return a.Val
		}
	}
	return ""
}

func setAttr(n *html.Node, key, val string) {
	for i, a := range n.Attr {
		if a.Key == key {
			n.Attr[i].Val = val
			return
		}
	}
	n.Attr = append(n.Attr, html.Attribute{Key: key, Val: val})
}

func text(n *html.Node) string {
	var b strings.Builder
	walk(n, func(c *html.Node) {
		if c.Type == html.TextNode {
			b.WriteString(c.Data)
		}
	})
	return b.String()
}

var spaces = regexp.MustCompile(`\s+`)

func collapse(s string) string { return strings.TrimSpace(spaces.ReplaceAllString(s, " ")) }

var (
	// mdLink is an inline link or image: ![text](target "title").
	mdLink = regexp.MustCompile(`(!?)\[((?:[^\[\]]|\[[^\[\]]*\])*)\]\(\s*(?:<([^<>]*)>|([^\s()]*(?:\([^\s()]*\)[^\s()]*)*))(?:\s+(?:"[^"]*"|'[^']*'|\([^)]*\)))?\s*\)`)
	// mdAutolink is a CommonMark autolink: <scheme:target>.
	mdAutolink = regexp.MustCompile(`<([A-Za-z][A-Za-z0-9+.-]{1,31}):([^<>\s]*)>`)
	// mdDefinition is a link reference definition: [label]: target.
	mdDefinition = regexp.MustCompile(`^ {0,3}\[([^\]]+)\]:\s*<?([^\s>]+)>?(.*)$`)
	fence        = regexp.MustCompile("^ {0,3}(```|~~~)")
)

// fromMarkdown cleans a Markdown page: links become absolute against the
// page URL and keep only http, https and mailto targets, and images keep
// their text. Code blocks stay as they are. The title is the title of the
// front matter or the first H1.
func fromMarkdown(md string, page *url.URL) (title, body string, links []*url.URL) {
	md = strings.ReplaceAll(md, "\r\n", "\n")
	md, title = frontMatter(md)
	lines := strings.Split(md, "\n")
	var f fences
	for i, l := range lines {
		if f.code(l) {
			continue
		}
		if title == "" {
			if t, ok := strings.CutPrefix(l, "# "); ok {
				title = strings.TrimSpace(strings.TrimRight(strings.TrimSpace(t), "#"))
			}
		}
		if m := mdDefinition.FindStringSubmatch(l); m != nil {
			u, ok := resolve(page, m[2])
			if !ok || !allowed(u) {
				lines[i] = ""
				continue
			}
			if u.Scheme != "mailto" {
				links = append(links, u)
			}
			lines[i] = "[" + m[1] + "]: " + u.String() + m[3]
			continue
		}
		l = mdLink.ReplaceAllStringFunc(l, func(s string) string {
			m := mdLink.FindStringSubmatch(s)
			label, target := m[2], m[3]+m[4]
			if m[1] == "!" {
				return label
			}
			u, ok := resolve(page, target)
			if !ok || !allowed(u) {
				return label
			}
			if u.Scheme != "mailto" {
				links = append(links, u)
			}
			return "[" + label + "](" + strings.ReplaceAll(u.String(), " ", "%20") + ")"
		})
		l = mdAutolink.ReplaceAllStringFunc(l, func(s string) string {
			m := mdAutolink.FindStringSubmatch(s)
			u, err := url.Parse(m[1] + ":" + m[2])
			if err != nil || !allowed(&url.URL{Scheme: strings.ToLower(u.Scheme)}) {
				return m[1] + ":" + m[2]
			}
			if u.Scheme != "mailto" {
				links = append(links, u)
			}
			return s
		})
		lines[i] = l
	}
	return title, strings.TrimSpace(strings.Join(lines, "\n")), links
}

// frontMatter removes YAML front matter and returns its title.
func frontMatter(md string) (string, string) {
	rest, ok := strings.CutPrefix(md, "---\n")
	if !ok {
		return md, ""
	}
	head, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return md, ""
	}
	title := ""
	for _, l := range strings.Split(head, "\n") {
		if v, ok := strings.CutPrefix(l, "title:"); ok {
			title = strings.Trim(strings.TrimSpace(v), `"'`)
		}
	}
	return body, title
}

// llmsLink is a link of an llms.txt list: [name](url): notes.
var llmsLink = regexp.MustCompile(`\[[^\]]*\]\(\s*<?([^)\s>]+)>?[^)]*\)`)

// parseLLMs reads an llms.txt file (llmstxt.org): the H1 is the title of
// the site, and each link under an H2 is a page. The links of the Optional
// section are pages too.
func parseLLMs(b []byte, file *url.URL) (title string, pages []*url.URL) {
	var f fences
	inSection := false
	for _, l := range strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n") {
		if f.code(l) {
			continue
		}
		switch {
		case strings.HasPrefix(l, "# ") && title == "":
			title = strings.TrimSpace(l[2:])
			continue
		case strings.HasPrefix(l, "## "):
			inSection = true
			continue
		}
		if !inSection {
			continue
		}
		for _, m := range llmsLink.FindAllStringSubmatch(l, -1) {
			if u, ok := resolve(file, m[1]); ok && (u.Scheme == "http" || u.Scheme == "https") {
				pages = append(pages, u)
			}
		}
	}
	return title, pages
}

// section is one top-level section of llms-full.txt.
type section struct {
	Title string
	Body  string
	Slug  string
	// Link is the first link of the section, or nil.
	Link *url.URL
}

// splitFull splits llms-full.txt into its top-level (#) sections. Text
// before the first H1 is not a section. A file without an H1 is one
// section.
func splitFull(b []byte, file *url.URL) []section {
	lines := strings.Split(strings.ReplaceAll(string(b), "\r\n", "\n"), "\n")
	var out []section
	var cur []string
	title := ""
	slugs := map[string]int{}
	flush := func() {
		body := strings.TrimSpace(strings.Join(cur, "\n"))
		cur = nil
		if body == "" || (title == "" && len(out) == 0 && hasH1(lines)) {
			return
		}
		t := title
		if t == "" {
			t = "llms-full.txt"
		}
		slug := slugify(t)
		slugs[slug]++
		if n := slugs[slug]; n > 1 {
			slug = fmt.Sprintf("%s-%d", slug, n)
		}
		s := section{Title: t, Body: body, Slug: slug}
		for _, m := range mdLink.FindAllStringSubmatch(body, -1) {
			if m[1] == "!" {
				continue
			}
			if u, ok := resolve(file, m[3]+m[4]); ok && (u.Scheme == "http" || u.Scheme == "https") {
				s.Link = u
				break
			}
		}
		if s.Link == nil {
			for _, m := range mdAutolink.FindAllStringSubmatch(body, -1) {
				if u, err := url.Parse(m[1] + ":" + m[2]); err == nil && (u.Scheme == "http" || u.Scheme == "https") {
					s.Link = u
					break
				}
			}
		}
		out = append(out, s)
	}
	var f fences
	for _, l := range lines {
		if !f.code(l) && strings.HasPrefix(l, "# ") {
			flush()
			title = strings.TrimSpace(l[2:])
		}
		cur = append(cur, l)
	}
	flush()
	return out
}

func hasH1(lines []string) bool {
	var f fences
	for _, l := range lines {
		if !f.code(l) && strings.HasPrefix(l, "# ") {
			return true
		}
	}
	return false
}

// fences follows the fenced code blocks of Markdown lines.
type fences struct{ open string }

// code reports whether a line is a fence or inside a code block.
func (f *fences) code(l string) bool {
	if m := fence.FindStringSubmatch(l); m != nil {
		switch f.open {
		case "":
			f.open = m[1]
		case m[1]:
			f.open = ""
		}
		return true
	}
	return f.open != ""
}

var nonSlug = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.Trim(nonSlug.ReplaceAllString(strings.ToLower(s), "-"), "-")
	if s == "" {
		return "section"
	}
	return s
}

// sitemap is a sitemap or a sitemap index (sitemaps.org).
type sitemap struct {
	URLs []struct {
		Loc string `xml:"loc"`
	} `xml:"url"`
	Sitemaps []struct {
		Loc string `xml:"loc"`
	} `xml:"sitemap"`
}

// parseSitemap reads a sitemap, gzipped or not, and returns its page and
// child sitemap URLs.
func parseSitemap(b []byte, file *url.URL) (pages, children []*url.URL, err error) {
	if len(b) > 2 && b[0] == 0x1f && b[1] == 0x8b {
		zr, err := gzip.NewReader(bytes.NewReader(b))
		if err != nil {
			return nil, nil, err
		}
		if b, err = io.ReadAll(io.LimitReader(zr, 64<<20)); err != nil {
			return nil, nil, err
		}
	}
	var s sitemap
	if err := xml.Unmarshal(b, &s); err != nil {
		return nil, nil, fmt.Errorf("web: %s is not a valid sitemap: %w", file, err)
	}
	for _, u := range s.URLs {
		if p, ok := resolve(file, u.Loc); ok {
			pages = append(pages, p)
		}
	}
	for _, c := range s.Sitemaps {
		if p, ok := resolve(file, c.Loc); ok {
			children = append(children, p)
		}
	}
	return pages, children, nil
}
