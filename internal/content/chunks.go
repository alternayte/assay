package content

import (
	"strings"

	"github.com/alternayte/assay/internal/models"
)

// MaxChunkTokens is the chunk size limit, prefix included (REQ-SYN-005).
const MaxChunkTokens = 400

// Chunk is a run of passages inside one top-level section, for search.
type Chunk struct {
	Ordinal     int
	HeadingPath []string
	// Text starts with the title and the heading path.
	Text          string
	PassageHashes [][]byte
	Tokens        int
}

// prefix is the first line of a chunk: the title and the heading path.
func prefix(title string, path []string) string {
	parts := append([]string{title}, path...)
	return strings.Join(parts, " > ") + "\n\n"
}

// Chunks groups passages into chunks. A chunk holds consecutive passages of
// one top-level section and has at most MaxChunkTokens tokens. A passage
// that alone exceeds the limit is cut at word boundaries.
func Chunks(title string, ps []Passage) []Chunk {
	var out []Chunk
	var cur *Chunk
	var body []string
	sec := -1
	tokens := func(path []string, parts []string) int {
		return models.EstimateTokens(prefix(title, path) + strings.Join(parts, "\n\n"))
	}
	flush := func() {
		if cur != nil && len(body) > 0 {
			cur.Text = prefix(title, cur.HeadingPath) + strings.Join(body, "\n\n")
			cur.Tokens = models.EstimateTokens(cur.Text)
			cur.Ordinal = len(out)
			out = append(out, *cur)
		}
		cur, body = nil, nil
	}
	for _, p := range ps {
		if cur != nil && (p.Section != sec || tokens(cur.HeadingPath, append(body, p.Text)) > MaxChunkTokens) {
			flush()
		}
		if cur == nil {
			cur = &Chunk{HeadingPath: p.HeadingPath}
			sec = p.Section
		}
		if tokens(cur.HeadingPath, []string{p.Text}) <= MaxChunkTokens {
			body = append(body, p.Text)
			cur.PassageHashes = append(cur.PassageHashes, p.Hash)
			continue
		}
		// One passage over the limit: cut it into pieces that fit.
		flush()
		for _, piece := range cutWords(p.Text, func(s string) bool { return tokens(p.HeadingPath, []string{s}) <= MaxChunkTokens }) {
			cur = &Chunk{HeadingPath: p.HeadingPath, PassageHashes: [][]byte{p.Hash}}
			body = []string{piece}
			flush()
		}
	}
	flush()
	return out
}

// cutWords cuts s at spaces into the longest pieces that fit.
func cutWords(s string, fits func(string) bool) []string {
	words := strings.Fields(s)
	var out []string
	var cur []string
	for _, w := range words {
		next := append(cur, w)
		if len(cur) > 0 && !fits(strings.Join(next, " ")) {
			out = append(out, strings.Join(cur, " "))
			next = []string{w}
		}
		cur = next
	}
	if len(cur) > 0 {
		out = append(out, strings.Join(cur, " "))
	}
	return out
}
