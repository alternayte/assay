package vocab

import (
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"
)

// LinkTerm is an approved term with the forms that link to it: its name,
// synonyms and deprecated forms.
type LinkTerm struct {
	Key    string
	Domain int64
	Forms  []string
}

// Match is one linked form in a text. Terms holds one key, or every key
// when the domain does not decide a homonym.
type Match struct {
	Start int      `json:"start" doc:"Byte offset of the form in the text."`
	End   int      `json:"end"`
	Form  string   `json:"form"`
	Terms []string `json:"terms"`
}

type termRef struct {
	key    string
	domain int64
}

type form struct {
	tokens []string // lower case, or as written for an all-capital form
	exact  bool
	terms  []termRef
}

// Linker links the forms of approved terms in Markdown (REQ-VOC-005).
type Linker struct {
	byFirst map[string][]*form
}

type token struct {
	start, end int
	text       string
}

func tokenize(s string) []token {
	var out []token
	start := -1
	for i, r := range s {
		word := unicode.IsLetter(r) || unicode.IsDigit(r)
		switch {
		case word && start < 0:
			start = i
		case !word && start >= 0:
			out = append(out, token{start, i, s[start:i]})
			start = -1
		}
	}
	if start >= 0 {
		out = append(out, token{start, len(s), s[start:]})
	}
	return out
}

// allCapitals reports whether a form has two or more letters and no lower
// case letter. Such forms match case-sensitively.
func allCapitals(s string) bool {
	letters := 0
	for _, r := range s {
		if unicode.IsLetter(r) {
			letters++
			if !unicode.IsUpper(r) {
				return false
			}
		}
	}
	return letters >= 2
}

// NewLinker indexes the forms of terms. Two terms with the same form are
// homonyms of that form.
func NewLinker(terms []LinkTerm) *Linker {
	l := &Linker{byFirst: map[string][]*form{}}
	index := map[string]*form{}
	for _, t := range terms {
		for _, f := range t.Forms {
			toks := tokenize(f)
			if len(toks) == 0 {
				continue
			}
			exact := allCapitals(f)
			words := make([]string, len(toks))
			for i, tk := range toks {
				words[i] = tk.text
				if !exact {
					words[i] = strings.ToLower(tk.text)
				}
			}
			id := strings.Join(words, " ")
			if exact {
				id = "=" + id
			}
			fm := index[id]
			if fm == nil {
				fm = &form{tokens: words, exact: exact}
				index[id] = fm
				first := strings.ToLower(words[0])
				l.byFirst[first] = append(l.byFirst[first], fm)
			}
			if !slices.ContainsFunc(fm.terms, func(r termRef) bool { return r.key == t.Key }) {
				fm.terms = append(fm.terms, termRef{t.Key, t.Domain})
			}
		}
	}
	for _, fs := range l.byFirst {
		slices.SortStableFunc(fs, func(a, b *form) int { return len(b.tokens) - len(a.tokens) })
	}
	return l
}

var (
	inlineCode = regexp.MustCompile("`[^`\n]*`")
	mdLink     = regexp.MustCompile(`!?\[[^\]\n]*\]\([^)\n]*\)`)
	bareURL    = regexp.MustCompile(`https?://\S+`)
	heading    = regexp.MustCompile(`(?m)^#{1,6} `)
)

// skips returns the byte ranges that never link: fenced code blocks,
// inline code, Markdown links and URLs.
func skips(s string) [][2]int {
	var out [][2]int
	fence := -1
	off := 0
	for _, line := range strings.SplitAfter(s, "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "```") {
			if fence < 0 {
				fence = off
			} else {
				out = append(out, [2]int{fence, off + len(line)})
				fence = -1
			}
		}
		off += len(line)
	}
	if fence >= 0 {
		out = append(out, [2]int{fence, len(s)})
	}
	for _, re := range []*regexp.Regexp{inlineCode, mdLink, bareURL} {
		for _, m := range re.FindAllStringIndex(s, -1) {
			out = append(out, [2]int{m[0], m[1]})
		}
	}
	return out
}

func inside(rs [][2]int, start, end int) bool {
	for _, r := range rs {
		if start < r[1] && end > r[0] {
			return true
		}
	}
	return false
}

// joined reports whether only spaces or hyphens separate two tokens.
func joined(s string) bool {
	if s == "" {
		return false
	}
	for len(s) > 0 {
		r, n := utf8.DecodeRuneInString(s)
		if r != '-' && !unicode.IsSpace(r) || r == '\n' {
			return false
		}
		s = s[n:]
	}
	return true
}

func (f *form) matches(text string, toks []token, i int) bool {
	if i+len(f.tokens) > len(toks) {
		return false
	}
	for j, w := range f.tokens {
		tk := toks[i+j]
		if f.exact && tk.text != w || !f.exact && strings.ToLower(tk.text) != w {
			return false
		}
		if j > 0 && !joined(text[toks[i+j-1].end:tk.start]) {
			return false
		}
	}
	return true
}

func (f *form) resolve(domain int64) []string {
	var keys []string
	if domain != 0 {
		for _, t := range f.terms {
			if t.domain == domain {
				keys = append(keys, t.key)
			}
		}
		if len(keys) == 1 {
			return keys
		}
		keys = nil
	}
	for _, t := range f.terms {
		keys = append(keys, t.key)
	}
	return keys
}

// Link finds the first use of each term per section: whole words, the
// longest form first, case-sensitive for all-capital forms, not in code or
// links. domain is the domain of the source connection or entity; 0 means
// none.
func (l *Linker) Link(text string, domain int64) []Match {
	if len(l.byFirst) == 0 {
		return nil
	}
	skip := skips(text)
	heads := heading.FindAllStringIndex(text, -1)
	toks := tokenize(text)
	seen := map[string]bool{}
	var out []Match
	h := 0
	for i := 0; i < len(toks); i++ {
		for h < len(heads) && heads[h][0] <= toks[i].start {
			seen = map[string]bool{}
			h++
		}
		var hit *form
		for _, f := range l.byFirst[strings.ToLower(toks[i].text)] {
			if f.matches(text, toks, i) && !inside(skip, toks[i].start, toks[i+len(f.tokens)-1].end) {
				hit = f
				break
			}
		}
		if hit == nil {
			continue
		}
		last := toks[i+len(hit.tokens)-1]
		keys := hit.resolve(domain)
		id := strings.Join(keys, ",")
		if !seen[id] {
			seen[id] = true
			out = append(out, Match{Start: toks[i].start, End: last.end, Form: text[toks[i].start:last.end], Terms: keys})
		}
		i += len(hit.tokens) - 1
	}
	return out
}
