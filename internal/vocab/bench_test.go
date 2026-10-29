//go:build bench

package vocab_test

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/testkit"
	"github.com/alternayte/assay/internal/vocab"
)

// TestSCN_VOC_011_LinkingIsFast links a page of 5,000 words against 2,000
// terms. The best of 5 runs must take 20 ms or less (NFR-007).
func TestSCN_VOC_011_LinkingIsFast(t *testing.T) {
	terms := make([]vocab.LinkTerm, 2000)
	for i := range terms {
		terms[i] = vocab.LinkTerm{Key: fmt.Sprintf("t:%d", i), Domain: int64(i % 4),
			Forms: []string{fmt.Sprintf("acme term %d", i), fmt.Sprintf("AT%d", i)}}
	}
	l := vocab.NewLinker(terms)
	var b strings.Builder
	words := 0
	for s := 0; words < 5000; s++ {
		if s%40 == 0 {
			fmt.Fprintf(&b, "\n\n## Section %d\n\n", s)
		}
		fmt.Fprintf(&b, "The acme term %d and AT%d meet the review of `code %d` in [a link](https://x.test/%d). ", s%2000, (s*7)%2000, s, s)
		words += 16
	}
	text := b.String()
	best := time.Hour
	var n int
	for range 5 {
		start := time.Now()
		n = len(l.Link(text, 1))
		best = min(best, time.Since(start))
	}
	pass := best <= 20*time.Millisecond && n > 0
	testkit.BenchReport("SCN-VOC-011", pass, best.Milliseconds(), map[string]any{"words": words, "terms": len(terms), "links": n})
	t.Logf("linked %d forms in %d words in %s", n, words, best)
	if !pass {
		t.Fatalf("linking took %s with %d links, want 20 ms or less", best, n)
	}
}
