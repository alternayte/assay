package jira

import (
	"fmt"
	"regexp"
	"strings"
)

var (
	heading   = regexp.MustCompile(`^h([1-6])\.\s+(.*)$`)
	listItem  = regexp.MustCompile(`^([*#-]+)\s+(.*)$`)
	codeStart = regexp.MustCompile(`^\{(code|noformat)(?::([^}]*))?\}(.*)$`)
	mono      = regexp.MustCompile(`\{\{(.+?)\}\}`)
	bold      = regexp.MustCompile(`(^|[\s(])\*(\S|\S.*?\S)\*($|[\s.,;:!?)])`)
	strike    = regexp.MustCompile(`(^|\s)-(\S|\S.*?\S)-($|[\s.,;:!?])`)
	insert    = regexp.MustCompile(`(^|\s)\+(\S|\S.*?\S)\+($|[\s.,;:!?])`)
	link      = regexp.MustCompile(`\[([^\[\]|]*)\|([^\[\]]+)\]`)
	bareLink  = regexp.MustCompile(`\[((?:https?|mailto):[^\[\]\s]+)\]`)
	mention   = regexp.MustCompile(`\[~([^\]]+)\]`)
	color     = regexp.MustCompile(`\{color(?::[^}]*)?\}`)
	image     = regexp.MustCompile(`!([^!\s][^!]*)!`)
)

// ToMarkdown converts Jira wiki markup to Markdown. Images are dropped.
// Links keep only http, https and mailto targets.
func ToMarkdown(wiki string) string {
	var out []string
	var para []string
	var table []string
	quoted := false
	flushPara := func() {
		if len(para) > 0 {
			out = append(out, prefixQuote(strings.Join(para, "\n"), quoted))
			para = nil
		}
	}
	flushTable := func() {
		if len(table) > 0 {
			out = append(out, prefixQuote(strings.Join(table, "\n"), quoted))
			table = nil
		}
	}
	lines := strings.Split(strings.ReplaceAll(wiki, "\r\n", "\n"), "\n")
	for i := 0; i < len(lines); i++ {
		line := strings.TrimRight(lines[i], " \t")
		trim := strings.TrimSpace(line)
		if m := codeStart.FindStringSubmatch(trim); m != nil {
			flushPara()
			flushTable()
			lang := ""
			if m[1] == "code" {
				lang, _, _ = strings.Cut(m[2], "|")
				if strings.Contains(lang, "=") {
					lang = ""
				}
			}
			end := "{" + m[1] + "}"
			var body []string
			rest := m[3]
			for {
				if before, _, ok := strings.Cut(rest, end); ok {
					if before != "" {
						body = append(body, before)
					}
					break
				}
				body = append(body, rest)
				i++
				if i >= len(lines) {
					break
				}
				rest = lines[i]
			}
			out = append(out, prefixQuote(fence(lang, strings.Trim(strings.Join(body, "\n"), "\n")), quoted))
			continue
		}
		if strings.HasPrefix(trim, "{quote}") || strings.HasPrefix(trim, "{panel") {
			flushPara()
			flushTable()
			quoted = !quoted
			if t := strings.TrimSpace(trim[strings.Index(trim, "}")+1:]); t != "" {
				para = append(para, inline(t))
			}
			continue
		}
		if trim == "{panel}" {
			flushPara()
			quoted = false
			continue
		}
		switch {
		case trim == "":
			flushPara()
			flushTable()
		case strings.HasPrefix(trim, "||") || (strings.HasPrefix(trim, "|") && strings.HasSuffix(trim, "|")):
			flushPara()
			table = append(table, tableRow(trim, len(table) == 0)...)
		case heading.MatchString(trim):
			flushPara()
			flushTable()
			m := heading.FindStringSubmatch(trim)
			out = append(out, prefixQuote(strings.Repeat("#", int(m[1][0]-'0'))+" "+inline(m[2]), quoted))
		case strings.HasPrefix(trim, "bq. "):
			flushPara()
			flushTable()
			out = append(out, "> "+inline(strings.TrimPrefix(trim, "bq. ")))
		case trim == "----":
			flushPara()
			flushTable()
		case listItem.MatchString(trim):
			flushTable()
			m := listItem.FindStringSubmatch(trim)
			depth := len(m[1])
			marker := "- "
			if strings.HasSuffix(m[1], "#") {
				marker = "1. "
			}
			if len(para) > 0 && !isList(para) {
				flushPara()
			}
			para = append(para, strings.Repeat("   ", depth-1)+marker+inline(m[2]))
		default:
			flushTable()
			if isList(para) {
				flushPara()
			}
			para = append(para, inline(trim))
		}
	}
	flushPara()
	flushTable()
	return strings.Join(out, "\n\n")
}

func isList(para []string) bool {
	if len(para) == 0 {
		return false
	}
	l := strings.TrimLeft(para[0], " ")
	return strings.HasPrefix(l, "- ") || strings.HasPrefix(l, "1. ")
}

func prefixQuote(s string, quoted bool) string {
	if !quoted {
		return s
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight("> "+l, " ")
	}
	return strings.Join(lines, "\n")
}

func fence(lang, body string) string {
	ticks := "```"
	for strings.Contains(body, ticks) {
		ticks += "`"
	}
	return fmt.Sprintf("%s%s\n%s\n%s", ticks, strings.TrimSpace(lang), body, ticks)
}

// tableRow converts one table row. The first row of a table is the header.
func tableRow(row string, first bool) []string {
	header := strings.HasPrefix(row, "||")
	sep := "|"
	if header {
		sep = "||"
	}
	cells := strings.Split(strings.Trim(row, "|"), sep)
	for i, c := range cells {
		cells[i] = strings.ReplaceAll(inline(strings.Trim(strings.TrimSpace(c), "|")), "|", `\|`)
	}
	line := "| " + strings.Join(cells, " | ") + " |"
	if !first {
		return []string{line}
	}
	if header {
		return []string{line, "|" + strings.Repeat("---|", len(cells))}
	}
	empty := make([]string, len(cells))
	return []string{"| " + strings.Join(empty, " | ") + " |", "|" + strings.Repeat("---|", len(cells)), line}
}

func inline(s string) string {
	s = image.ReplaceAllString(s, "")
	s = color.ReplaceAllString(s, "")
	s = mention.ReplaceAllString(s, "$1")
	s = mono.ReplaceAllString(s, "`$1`")
	s = link.ReplaceAllStringFunc(s, func(m string) string {
		p := link.FindStringSubmatch(m)
		text, target := strings.TrimSpace(p[1]), strings.TrimSpace(p[2])
		if text == "" {
			text = target
		}
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") || strings.HasPrefix(target, "mailto:") {
			return "[" + text + "](" + target + ")"
		}
		return text
	})
	s = bareLink.ReplaceAllString(s, "[$1]($1)")
	s = bold.ReplaceAllString(s, "$1**$2**$3")
	s = strike.ReplaceAllString(s, "$1~~$2~~$3")
	s = insert.ReplaceAllString(s, "$1$2$3")
	s = strings.ReplaceAll(s, `\\`, "\\\n")
	return s
}
