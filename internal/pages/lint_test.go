package pages_test

import (
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/pages"
	"github.com/alternayte/assay/internal/vocab"
)

func rules(fs []pages.Failure) []string {
	var out []string
	for _, f := range fs {
		out = append(out, f.Rule)
	}
	return out
}

func words(n int) string {
	return strings.TrimSpace(strings.Repeat("word ", n))
}

func TestSCN_PAG_003_LintRules(t *testing.T) {
	cfg := pages.DefaultRules()
	terms := []vocab.Term{
		{Key: "t:submission", Name: "Submission", Status: vocab.StatusApproved},
		{Key: "t:manuscript", Name: "Manuscript", Status: vocab.StatusDeprecated, Preferred: "t:submission"},
	}
	ok := "The service sends reminder emails [C1]. Team Payments owns it [C2]."
	if fs := pages.Lint(ok, cfg, terms); len(fs) != 0 {
		t.Fatalf("a clean summary fails: %+v", fs)
	}
	cases := []struct {
		name, text, rule string
	}{
		{"sentence of 26 words", words(26) + " [C1].", pages.RuleSentence},
		{"summary of 121 words", strings.Repeat(words(11)+" [C1]. ", 11) + "end [C1].", pages.RuleSummary},
		{"banned word", "The team will leverage the queue [C1].", pages.RuleBanned},
		{"banned phrase", "It is important to note that the queue runs [C1].", pages.RuleBanned},
		{"deprecated term", "Each manuscript goes to the queue [C1].", pages.RuleDeprecated},
	}
	for _, c := range cases {
		fs := pages.Lint(c.text, cfg, terms)
		if !slices.Contains(rules(fs), c.rule) {
			t.Errorf("%s: rules %v, want %s", c.name, rules(fs), c.rule)
		}
	}
	if fs := pages.Lint("Each manuscript goes to the queue [C1].", cfg, terms); len(fs) == 0 || !strings.Contains(fs[0].Message, "Submission") {
		t.Errorf("the deprecated finding does not name the preferred term: %+v", fs)
	}
	// Markers are not words: 25 words and a marker pass.
	if fs := pages.Lint(words(25)+" [C1] [C2].", cfg, terms); len(fs) != 0 {
		t.Errorf("25 words fail: %+v", fs)
	}
	// A threshold change changes the result.
	cfg.SentenceWords = 30
	if fs := pages.Lint(words(26)+" [C1].", cfg, terms); len(fs) != 0 {
		t.Errorf("26 words fail with a limit of 30: %+v", fs)
	}
	cfg.BannedWords = []string{"queue"}
	if fs := pages.Lint("The team will leverage the queue [C1].", cfg, terms); len(fs) != 1 || !strings.Contains(fs[0].Message, "queue") {
		t.Errorf("the new word list does not apply: %+v", fs)
	}
}
