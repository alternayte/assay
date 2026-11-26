package jira

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// adfNode is one node of an Atlassian Document Format document.
type adfNode struct {
	Type    string         `json:"type"`
	Text    string         `json:"text"`
	Attrs   map[string]any `json:"attrs"`
	Marks   []adfMark      `json:"marks"`
	Content []adfNode      `json:"content"`
}

type adfMark struct {
	Type  string         `json:"type"`
	Attrs map[string]any `json:"attrs"`
}

// ADFToMarkdown converts an Atlassian Document Format document, the
// description and comment format of Jira Cloud, to Markdown. It follows the
// rules of ToMarkdown: images are dropped, links keep only http, https and
// mailto targets, and the blocks match those of the same wiki markup.
func ADFToMarkdown(raw json.RawMessage) (string, error) {
	var doc adfNode
	if err := json.Unmarshal(raw, &doc); err != nil {
		return "", fmt.Errorf("the document is not valid ADF: %w", err)
	}
	return strings.Join(adfBlocks(doc.Content), "\n\n"), nil
}

// adfBlocks converts block nodes. Each block is one Markdown block.
func adfBlocks(nodes []adfNode) []string {
	var out []string
	for _, n := range nodes {
		if b := adfBlock(n); strings.TrimSpace(b) != "" {
			out = append(out, b)
		}
	}
	return out
}

func adfBlock(n adfNode) string {
	switch n.Type {
	case "paragraph":
		return strings.TrimSpace(adfInline(n.Content))
	case "heading":
		level := 1
		if l, ok := n.Attrs["level"].(float64); ok && l >= 1 && l <= 6 {
			level = int(l)
		}
		return strings.Repeat("#", level) + " " + strings.TrimSpace(adfInline(n.Content))
	case "bulletList", "orderedList":
		return strings.Join(adfList(n, 0), "\n")
	case "codeBlock":
		lang, _ := n.Attrs["language"].(string)
		var text strings.Builder
		for _, c := range n.Content {
			text.WriteString(c.Text)
		}
		return fence(lang, strings.Trim(text.String(), "\n"))
	case "blockquote", "panel":
		return prefixQuote(strings.Join(adfBlocks(n.Content), "\n\n"), true)
	case "table":
		return adfTable(n)
	case "expand", "nestedExpand", "layoutSection", "layoutColumn", "bodiedExtension":
		return strings.Join(adfBlocks(n.Content), "\n\n")
	case "rule", "mediaSingle", "mediaGroup", "media", "extension":
		return ""
	}
	if len(n.Content) > 0 {
		return strings.Join(adfBlocks(n.Content), "\n\n")
	}
	return ""
}

// adfList converts a list to lines. A nested list is indented by three
// spaces per level, the way ToMarkdown indents nested wiki lists.
func adfList(n adfNode, depth int) []string {
	marker := "- "
	if n.Type == "orderedList" {
		marker = "1. "
	}
	var out []string
	for _, item := range n.Content {
		first := true
		for _, c := range item.Content {
			switch c.Type {
			case "bulletList", "orderedList":
				out = append(out, adfList(c, depth+1)...)
			default:
				text := strings.ReplaceAll(adfBlock(c), "\n", " ")
				if first {
					out = append(out, strings.Repeat("   ", depth)+marker+text)
					first = false
				} else if text != "" {
					out = append(out, strings.Repeat("   ", depth)+strings.Repeat(" ", len(marker))+text)
				}
			}
		}
		if first {
			out = append(out, strings.Repeat("   ", depth)+strings.TrimSpace(marker))
		}
	}
	return out
}

// adfTable converts a table. The first row is the header when its cells are
// header cells; else the table gets an empty header, as in ToMarkdown.
func adfTable(n adfNode) string {
	var lines []string
	for i, row := range n.Content {
		var cells []string
		header := len(row.Content) > 0
		for _, cell := range row.Content {
			if cell.Type != "tableHeader" {
				header = false
			}
			text := strings.ReplaceAll(strings.Join(adfBlocks(cell.Content), " "), "\n", " ")
			cells = append(cells, strings.ReplaceAll(text, "|", `\|`))
		}
		line := "| " + strings.Join(cells, " | ") + " |"
		switch {
		case i > 0:
			lines = append(lines, line)
		case header:
			lines = append(lines, line, "|"+strings.Repeat("---|", len(cells)))
		default:
			empty := make([]string, len(cells))
			lines = append(lines, "| "+strings.Join(empty, " | ")+" |", "|"+strings.Repeat("---|", len(cells)), line)
		}
	}
	return strings.Join(lines, "\n")
}

// adfInline converts inline nodes with their marks.
func adfInline(nodes []adfNode) string {
	var b strings.Builder
	for _, n := range nodes {
		switch n.Type {
		case "text":
			b.WriteString(adfMarks(n.Text, n.Marks))
		case "hardBreak":
			b.WriteString("\n")
		case "mention":
			text, _ := n.Attrs["text"].(string)
			b.WriteString(strings.TrimPrefix(text, "@"))
		case "emoji":
			text, _ := n.Attrs["text"].(string)
			if text == "" {
				text, _ = n.Attrs["shortName"].(string)
			}
			b.WriteString(text)
		case "status":
			text, _ := n.Attrs["text"].(string)
			b.WriteString(text)
		case "date":
			b.WriteString(adfDate(n.Attrs["timestamp"]))
		case "inlineCard", "blockCard":
			u, _ := n.Attrs["url"].(string)
			if allowedLink(u) {
				b.WriteString("[" + u + "](" + u + ")")
			}
		default:
			b.WriteString(adfInline(n.Content))
		}
	}
	return b.String()
}

// adfMarks wraps a text in the Markdown of its marks. A link keeps only an
// http, https or mailto target.
func adfMarks(text string, marks []adfMark) string {
	var href string
	for _, m := range marks {
		switch m.Type {
		case "code":
			text = "`" + text + "`"
		case "strong":
			text = "**" + text + "**"
		case "em":
			text = "_" + text + "_"
		case "strike":
			text = "~~" + text + "~~"
		case "link":
			href, _ = m.Attrs["href"].(string)
		}
	}
	if href != "" && allowedLink(href) {
		return "[" + text + "](" + href + ")"
	}
	return text
}

func allowedLink(u string) bool {
	return strings.HasPrefix(u, "http://") || strings.HasPrefix(u, "https://") || strings.HasPrefix(u, "mailto:")
}

// adfDate formats the timestamp of a date node, milliseconds since the
// epoch as a string or a number, as a date.
func adfDate(v any) string {
	var ms int64
	switch t := v.(type) {
	case string:
		ms, _ = strconv.ParseInt(t, 10, 64)
	case float64:
		ms = int64(t)
	}
	if ms == 0 {
		return ""
	}
	return time.UnixMilli(ms).UTC().Format("2006-01-02")
}
