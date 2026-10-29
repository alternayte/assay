package content_test

import (
	"encoding/hex"
	"os"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/models"
	"github.com/alternayte/assay/internal/testkit"
)

type goldenPassage struct {
	Ordinal     int      `json:"ordinal"`
	Section     int      `json:"section"`
	Kind        string   `json:"kind"`
	HeadingPath []string `json:"headingPath"`
	Text        string   `json:"text"`
	Norm        string   `json:"norm"`
	Hash        string   `json:"hash"`
}

func golden(ps []content.Passage) []goldenPassage {
	out := make([]goldenPassage, len(ps))
	for i, p := range ps {
		out[i] = goldenPassage{p.Ordinal, p.Section, p.Kind, p.HeadingPath, p.Text, p.Norm, hex.EncodeToString(p.Hash)[:16]}
	}
	return out
}

func hashes(ps []content.Passage) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = hex.EncodeToString(p.Hash)
	}
	return out
}

func TestSCN_SYN_004_MarkdownPassagesMatchGolden(t *testing.T) {
	src, err := os.ReadFile("testdata/markdown.md")
	if err != nil {
		t.Fatal(err)
	}
	ps := content.Split(string(src))
	testkit.Golden(t, "testdata/markdown.passages.json", golden(ps))

	// Whitespace and inline formatting changes keep the hashes. A changed
	// link target is not passage text.
	re, err := os.ReadFile("testdata/markdown-reformatted.md")
	if err != nil {
		t.Fatal(err)
	}
	if a, b := hashes(ps), hashes(content.Split(string(re))); strings.Join(a, ",") != strings.Join(b, ",") {
		t.Fatalf("hashes changed after reformatting:\n%v\n%v", a, b)
	}

	// A block above 1,500 characters splits, and each part keeps the heading path.
	long := "# Big\n\n" + strings.Repeat("This sentence fills the long paragraph. ", 100)
	parts := content.Split(long)
	if len(parts) < 3 {
		t.Fatalf("want 3 or more parts, got %d", len(parts))
	}
	for _, p := range parts {
		if n := len([]rune(p.Text)); n > content.MaxPassageChars {
			t.Fatalf("part of %d characters", n)
		}
		if len(p.HeadingPath) != 1 || p.HeadingPath[0] != "Big" {
			t.Fatalf("heading path %v", p.HeadingPath)
		}
	}
}

func TestSCN_SYN_005_ChunksStayInSectionsAndLimit(t *testing.T) {
	var b strings.Builder
	b.WriteString("Intro paragraph before any heading.\n\n")
	for s := range 3 {
		b.WriteString("# Section " + string(rune('A'+s)) + "\n\n")
		for p := range 12 {
			b.WriteString("## Part " + string(rune('a'+p)) + "\n\n")
			b.WriteString(strings.Repeat("Words about the payments API and its owners. ", 12) + "\n\n")
		}
	}
	ps := content.Split(b.String())
	chunks := content.Chunks("Payments handbook", ps)
	if len(chunks) < 6 {
		t.Fatalf("want several chunks, got %d", len(chunks))
	}
	bySection := map[string]int{}
	for _, p := range ps {
		bySection[hex.EncodeToString(p.Hash)] = p.Section
	}
	for _, c := range chunks {
		if c.Tokens > content.MaxChunkTokens || models.EstimateTokens(c.Text) > content.MaxChunkTokens {
			t.Fatalf("chunk %d has %d tokens", c.Ordinal, c.Tokens)
		}
		want := strings.Join(append([]string{"Payments handbook"}, c.HeadingPath...), " > ")
		if !strings.HasPrefix(c.Text, want+"\n\n") {
			t.Fatalf("chunk %d does not start with %q: %q", c.Ordinal, want, c.Text[:60])
		}
		sec := -1
		for _, h := range c.PassageHashes {
			s := bySection[hex.EncodeToString(h)]
			if sec >= 0 && s != sec {
				t.Fatalf("chunk %d crosses a top-level heading", c.Ordinal)
			}
			sec = s
		}
	}

	// One passage above the limit is cut into chunks that fit.
	huge := []content.Passage{{Section: 1, Kind: "list", HeadingPath: []string{"H"}, Text: strings.Repeat("a b c d ", 400), Hash: []byte{1}}}
	for _, c := range content.Chunks("T", huge) {
		if c.Tokens > content.MaxChunkTokens {
			t.Fatalf("cut chunk has %d tokens", c.Tokens)
		}
	}
}
