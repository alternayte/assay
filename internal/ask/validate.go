package ask

import (
	"regexp"
	"slices"
	"strings"
)

// NoEvidence is the answer when no valid citation remains (REQ-ASK-003).
const NoEvidence = "Assay found no evidence for this question."

// Checked is a validated answer.
type Checked struct {
	// Markdown keeps only the markers of known sources and claims, written
	// as [S1] and [C2].
	Markdown string
	// Cited lists the markers that the answer uses, in order of first use.
	Cited []string
	// Uncited lists the sentences without a valid marker.
	Uncited []string
	// Unknowns are the parts of the question that the context does not
	// answer, from "Unknown:" lines.
	Unknowns   []string
	NoEvidence bool
}

var (
	markerGroup = regexp.MustCompile(`\[\s*([A-Za-z]+\d+(?:\s*[,;]\s*[A-Za-z]+\d+)*)\s*\]`)
	markerID    = regexp.MustCompile(`[A-Za-z]+\d+`)
	unknownLine = regexp.MustCompile(`(?i)^\s*(?:[-*]\s+)?\**unknowns?:?\**:?\s*(.+)$`)
	listPrefix  = regexp.MustCompile(`^\s*(?:[-*+]|\d+[.)])\s+`)
	sentenceEnd = regexp.MustCompile(`[.!?](?:\s*\[[^\]]*\])*(?:\s+|$)`)
	validMarker = regexp.MustCompile(`\[[SC]\d+\]`)
)

// Validate removes unknown citation markers, collects the "Unknown:"
// lines and flags sentences without a valid marker. known holds the IDs of
// the context, such as S1 and C2. An ID outside known, such as a page
// orientation block, is not valid evidence.
func Validate(answer string, known map[string]bool) Checked {
	var out Checked
	var lines []string
	for _, l := range strings.Split(strings.ReplaceAll(answer, "\r\n", "\n"), "\n") {
		if m := unknownLine.FindStringSubmatch(l); m != nil {
			if u := strings.TrimSpace(validMarker.ReplaceAllString(markerGroup.ReplaceAllString(m[1], ""), "")); u != "" {
				out.Unknowns = append(out.Unknowns, u)
			}
			continue
		}
		l = markerGroup.ReplaceAllStringFunc(l, func(g string) string {
			var keep strings.Builder
			for _, id := range markerID.FindAllString(g, -1) {
				id = strings.ToUpper(id)
				if known[id] {
					keep.WriteString("[" + id + "]")
					if !slices.Contains(out.Cited, id) {
						out.Cited = append(out.Cited, id)
					}
				}
			}
			return keep.String()
		})
		lines = append(lines, strings.TrimRight(l, " \t"))
	}
	md := strings.TrimSpace(collapseBlank(strings.Join(lines, "\n")))
	for _, l := range strings.Split(md, "\n") {
		text := strings.TrimSpace(listPrefix.ReplaceAllString(l, ""))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		for _, s := range sentences(text) {
			if !validMarker.MatchString(s) {
				out.Uncited = append(out.Uncited, strings.TrimSpace(s))
			}
		}
	}
	if len(out.Cited) == 0 {
		out.Markdown = NoEvidence
		out.NoEvidence = true
		out.Uncited = nil
		return out
	}
	out.Markdown = md
	return out
}

// sentences splits a line after sentence ends. Markers after the end stay
// with the sentence.
func sentences(line string) []string {
	var out []string
	for line != "" {
		loc := sentenceEnd.FindStringIndex(line)
		if loc == nil {
			out = append(out, line)
			break
		}
		if s := strings.TrimSpace(line[:loc[1]]); s != "" {
			out = append(out, s)
		}
		line = line[loc[1]:]
	}
	return out
}

func collapseBlank(s string) string {
	for strings.Contains(s, "\n\n\n") {
		s = strings.ReplaceAll(s, "\n\n\n", "\n\n")
	}
	return s
}
