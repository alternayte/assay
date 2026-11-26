package slack

import (
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// converter turns Slack mrkdwn into normalized Markdown. user and channel
// return the display name of a user ID and the #name of a channel ID.
type converter struct {
	user    func(id string) string
	channel func(id string) string
}

var (
	// entity is a Slack special form: <@U1>, <#C1|name>, <!here>, <url|label>.
	entity = regexp.MustCompile(`<([^<>\s][^<>]*)>`)
	bold   = regexp.MustCompile(`(^|[\s(\[{"'])\*([^*\n]*[^*\s\n])\*`)
	italic = regexp.MustCompile(`(^|[\s(\[{"'])_([^_\n]*[^_\s\n])_`)
	strike = regexp.MustCompile(`(^|[\s(\[{"'])~([^~\n]*[^~\s\n])~`)
	// inlineCode is a code span. Marks inside it stay.
	inlineCode = regexp.MustCompile("`[^`\n]+`")
)

// markdown converts the text of a message. Code blocks stay as they are.
// Mentions become the display name, links keep only http, https and mailto
// targets, and quotes become Markdown quotes.
func (c converter) markdown(text string) string {
	parts := strings.Split(text, "```")
	var b strings.Builder
	for i, p := range parts {
		if i%2 == 1 && i < len(parts)-1 {
			code := strings.Trim(p, "\n")
			b.WriteString("\n\n```\n" + unescape(code) + "\n```\n\n")
			continue
		}
		if i%2 == 1 {
			// An unclosed fence is text.
			b.WriteString("```")
		}
		b.WriteString(c.prose(p))
	}
	lines := strings.Split(b.String(), "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t")
	}
	return strings.TrimSpace(collapseBlank(strings.Join(lines, "\n")))
}

// prose converts mrkdwn outside code blocks.
func (c converter) prose(s string) string {
	var spans []string
	s = inlineCode.ReplaceAllStringFunc(s, func(m string) string {
		spans = append(spans, unescape(m))
		return "\x00" + strconv.Itoa(len(spans)-1) + "\x00"
	})
	s = entity.ReplaceAllStringFunc(s, func(m string) string { return c.entity(m[1 : len(m)-1]) })
	s = bold.ReplaceAllString(s, "$1**$2**")
	s = italic.ReplaceAllString(s, "$1*$2*")
	s = strike.ReplaceAllString(s, "$1~~$2~~")
	var lines []string
	for _, l := range strings.Split(s, "\n") {
		// A quote line starts with &gt;. Other angle brackets are text.
		quote := false
		if rest, ok := strings.CutPrefix(l, "&gt;"); ok {
			l, quote = strings.TrimPrefix(rest, " "), true
		}
		l = strings.NewReplacer("&lt;", `\<`, "&gt;", ">", "&amp;", "&").Replace(l)
		if quote {
			l = "> " + l
		}
		lines = append(lines, l)
	}
	s = strings.Join(lines, "\n")
	for i, sp := range spans {
		s = strings.Replace(s, "\x00"+strconv.Itoa(i)+"\x00", sp, 1)
	}
	return s
}

// entity converts the inside of one <...> form.
func (c converter) entity(e string) string {
	target, label, _ := strings.Cut(e, "|")
	switch {
	case strings.HasPrefix(target, "@"):
		if label != "" {
			return "@" + escapeText(label)
		}
		return "@" + escapeText(c.user(target[1:]))
	case strings.HasPrefix(target, "#"):
		if label != "" {
			return "#" + escapeText(label)
		}
		return escapeText(c.channel(target[1:]))
	case strings.HasPrefix(target, "!"):
		if label != "" {
			return escapeText(label)
		}
		special, _, _ := strings.Cut(target[1:], "^")
		return "@" + special
	}
	u, err := url.Parse(unescape(target))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https" && u.Scheme != "mailto") {
		if label != "" {
			return escapeText(unescape(label))
		}
		return escapeText(unescape(target))
	}
	link := strings.ReplaceAll(u.String(), " ", "%20")
	if label == "" {
		if u.Scheme == "mailto" {
			return "[" + escapeText(u.Opaque) + "](" + link + ")"
		}
		return "<" + link + ">"
	}
	return "[" + escapeText(unescape(label)) + "](" + link + ")"
}

func unescape(s string) string {
	return strings.NewReplacer("&lt;", "<", "&gt;", ">", "&amp;", "&").Replace(s)
}

// escapeText keeps a name or a label as text in Markdown.
func escapeText(s string) string {
	return strings.NewReplacer(`\`, `\\`, "[", `\[`, "]", `\]`, "*", `\*`, "_", `\_`, "`", "\\`", "<", `\<`).Replace(s)
}

var blank = regexp.MustCompile(`\n{3,}`)

func collapseBlank(s string) string { return blank.ReplaceAllString(s, "\n\n") }
