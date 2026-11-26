package content

import "strings"

// LastChangeHeading is the heading of the passage that holds the commit
// message of the last change of a repository file.
const LastChangeHeading = "Last change"

const lastChangeMark = "\n\n# " + LastChangeHeading + "\n\n"

// AddLastChange adds the commit message of the last change of a file to an
// item: a top-level section at the end of the body, and a passage when the
// item has its own passages.
func AddLastChange(it *Item, message string) {
	msg := lastChangeText(message)
	if msg == "" {
		return
	}
	it.Body = strings.TrimRight(it.Body, "\n") + lastChangeMark + msg
	if it.Passages != nil {
		it.Passages = appendLastChange(it.Passages, msg)
	}
}

// CutLastChange splits a stored body into the file part and the commit
// message of the last change, or returns the body and "".
func CutLastChange(body string) (string, string) {
	i := strings.LastIndex(body, lastChangeMark)
	if i < 0 {
		return body, ""
	}
	return body[:i], body[i+len(lastChangeMark):]
}

// appendLastChange adds the passages of a last change section after the
// passages of a file, as the Markdown split makes them.
func appendLastChange(ps []Passage, msg string) []Passage {
	sec, ord := 0, 0
	for _, p := range ps {
		sec = max(sec, p.Section)
		ord = max(ord, p.Ordinal+1)
	}
	out := append([]Passage{}, ps...)
	for _, p := range Split("# " + LastChangeHeading + "\n\n" + msg) {
		p.Section += sec
		p.Ordinal += ord
		out = append(out, p)
	}
	return out
}

// lastChangeText keeps a commit message inside its section: a line that
// starts with # would start a new heading.
func lastChangeText(message string) string {
	lines := strings.Split(strings.TrimSpace(strings.ReplaceAll(message, "\r\n", "\n")), "\n")
	for i, l := range lines {
		if strings.HasPrefix(strings.TrimLeft(l, " "), "#") {
			lines[i] = `\` + strings.TrimLeft(l, " ")
		}
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
