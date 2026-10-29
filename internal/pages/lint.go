package pages

import (
	"errors"
	"fmt"
	"regexp"
	"strings"
	"unicode"

	"github.com/alternayte/assay/internal/settings"
	"github.com/alternayte/assay/internal/vocab"
)

// PageRules are the writing lint thresholds and the extraction limit of a page.
type PageRules struct {
	SentenceWords   int      `json:"sentenceWords" minimum:"5" doc:"The most words in one sentence of a summary."`
	SummaryWords    int      `json:"summaryWords" minimum:"20" doc:"The most words in a summary."`
	BannedWords     []string `json:"bannedWords" doc:"Words and phrases that a summary must not use."`
	ExtractPassages int      `json:"extractPassages" minimum:"0" maximum:"200" doc:"The most passages that a compile sends to extraction."`
}

// DefaultRules are the rules of SDD section 15.
func DefaultRules() PageRules {
	return PageRules{SentenceWords: 25, SummaryWords: 120, ExtractPassages: 40, BannedWords: []string{"leverage", "utilize", "robust", "seamless",
		"delve", "crucial", "comprehensive", "holistic", "synergy", "cutting-edge", "game-changer", "in order to", "it is important to note"}}
}

// Setting is the page rules setting (REQ-PAG-004).
var Setting = settings.Define("pages", "Writing lint of page summaries and the extraction limit of a page compile.", DefaultRules(),
	func(r PageRules) error {
		if r.SentenceWords < 5 || r.SummaryWords < 20 {
			return errors.New("a sentence needs a limit of 5 words or more and a summary 20 or more")
		}
		if r.ExtractPassages < 0 || r.ExtractPassages > 200 {
			return errors.New("the extraction limit must be 0 to 200 passages")
		}
		return nil
	})

// The lint rules.
const (
	RuleSentence   = "sentence_length"
	RuleSummary    = "summary_length"
	RuleBanned     = "banned_word"
	RuleDeprecated = "deprecated_term"
	RuleUncited    = "uncited"
)

// Failure is one lint finding.
type Failure struct {
	Rule    string `json:"rule" enum:"sentence_length,summary_length,banned_word,deprecated_term,uncited"`
	Message string `json:"message"`
}

var (
	marker   = regexp.MustCompile(`\[[A-Za-z]+\d+\]`)
	sentence = regexp.MustCompile(`[.!?](?:\s*\[[^\]]*\])*(?:\s+|$)`)
)

// countWords counts the words of a text without citation markers.
func countWords(s string) int {
	n := 0
	for _, w := range strings.Fields(marker.ReplaceAllString(s, " ")) {
		if strings.ContainsFunc(w, func(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }) {
			n++
		}
	}
	return n
}

// sentences splits a summary into sentences. Markers after the end stay
// with their sentence.
func sentences(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*+ "))
		for line != "" {
			loc := sentence.FindStringIndex(line)
			if loc == nil {
				out = append(out, line)
				break
			}
			if t := strings.TrimSpace(line[:loc[1]]); t != "" {
				out = append(out, t)
			}
			line = line[loc[1]:]
		}
	}
	return out
}

// Lint checks a summary against the rules: sentence length, summary length,
// banned words and deprecated terms. ts are the visible terms.
func Lint(summary string, r PageRules, ts []vocab.Term) []Failure {
	out := []Failure{}
	for _, s := range sentences(summary) {
		if n := countWords(s); n > r.SentenceWords {
			out = append(out, Failure{Rule: RuleSentence, Message: fmt.Sprintf("A sentence has %d words. The limit is %d: %q", n, r.SentenceWords,
				strings.TrimSpace(marker.ReplaceAllString(s, "")))})
		}
	}
	if n := countWords(summary); n > r.SummaryWords {
		out = append(out, Failure{Rule: RuleSummary, Message: fmt.Sprintf("The summary has %d words. The limit is %d.", n, r.SummaryWords)})
	}
	lower := strings.ToLower(summary)
	for _, w := range r.BannedWords {
		w = strings.ToLower(strings.TrimSpace(w))
		if w == "" {
			continue
		}
		re := regexp.MustCompile(`(^|[^\pL\pN])` + strings.ReplaceAll(regexp.QuoteMeta(w), " ", `\s+`) + `($|[^\pL\pN])`)
		if re.MatchString(lower) {
			out = append(out, Failure{Rule: RuleBanned, Message: fmt.Sprintf("Do not use %q.", w)})
		}
	}
	for _, f := range vocab.Lint(ts, summary) {
		out = append(out, Failure{Rule: RuleDeprecated, Message: fmt.Sprintf("%q is deprecated. Use %q.", f.Form, f.Preferred)})
	}
	return out
}
