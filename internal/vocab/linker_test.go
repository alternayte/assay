package vocab_test

import (
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/vocab"
)

func links(t *testing.T, l *vocab.Linker, text string, domain int64) []string {
	t.Helper()
	var out []string
	for _, m := range l.Link(text, domain) {
		if got := text[m.Start:m.End]; got != m.Form {
			t.Errorf("match %+v covers %q", m, got)
		}
		out = append(out, m.Form+"="+strings.Join(m.Terms, ","))
	}
	return out
}

func eq(t *testing.T, got []string, want ...string) {
	t.Helper()
	if strings.Join(got, " | ") != strings.Join(want, " | ") {
		t.Errorf("links\n got %q\nwant %q", got, want)
	}
}

func TestSCN_VOC_005_LinkerRules(t *testing.T) {
	l := vocab.NewLinker([]vocab.LinkTerm{
		{Key: "t:review", Forms: []string{"review"}},
		{Key: "t:peer-review", Forms: []string{"peer review"}},
		{Key: "t:apc", Forms: []string{"Article Processing Charge", "APC"}},
		{Key: "t:editor-journal", Domain: 1, Forms: []string{"editor"}},
		{Key: "t:editor-tool", Domain: 2, Forms: []string{"editor"}},
	})

	// Whole words only, and the longest match wins.
	eq(t, links(t, l, "Every peer review counts. Reviews and reviewer do not.", 0), "peer review=t:peer-review")
	// All-capital forms are case-sensitive; others are not.
	eq(t, links(t, l, "The apc and the Apc stay. The APC links.", 0), "APC=t:apc")
	eq(t, links(t, l, "The ARTICLE PROCESSING CHARGE applies.", 0), "ARTICLE PROCESSING CHARGE=t:apc")
	// First use per section: a new heading starts a new section.
	eq(t, links(t, l, "A review. Another review.\n\n## Next\n\nOne more review.", 0), "review=t:review", "review=t:review")
	// Not in code or links.
	eq(t, links(t, l, "Run `review` and see [the review](https://x.test/review) or https://x.test/review.\n\n```\nreview\n```\n", 0))
	eq(t, links(t, l, "```\nreview\n```\n\nA review here.", 0), "review=t:review")
	// A homonym resolves by domain, or lists both.
	eq(t, links(t, l, "Ask the editor.", 1), "editor=t:editor-journal")
	eq(t, links(t, l, "Ask the editor.", 2), "editor=t:editor-tool")
	eq(t, links(t, l, "Ask the editor.", 0), "editor=t:editor-journal,t:editor-tool")
}
