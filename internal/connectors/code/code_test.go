package code_test

import (
	"fmt"
	"os"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/connectors/code"
	"github.com/alternayte/assay/internal/content"
)

func ranges(ps []content.Passage) []string {
	out := make([]string, len(ps))
	for i, p := range ps {
		out[i] = fmt.Sprintf("%d-%d %s", p.StartLine, p.EndLine, strings.Join(p.HeadingPath, "/"))
	}
	return out
}

// TestSCN_DSG_001_DeclarationsBecomePassages pins one passage per
// top-level declaration with its comments and line range, one passage for
// the imports, members of a namespace, and the blank-line fallback.
func TestSCN_DSG_001_DeclarationsBecomePassages(t *testing.T) {
	cases := []struct {
		file, data string
		want       []string
	}{
		{"internal/ledger/ledger.go", "ledger.go.txt", []string{"1-7 ", "9-10 ", "12-16 ", "18-24 "}},
		{"src/Ledger.cs", "Ledger.cs.txt", []string{"1-2 ", "4-5 namespace Acme.Ledger", "6-13 namespace Acme.Ledger",
			"15-15 namespace Acme.Ledger"}},
		{"fraud/score.py", "score.py.txt", []string{"1-2 ", "5-9 ", "12-14 "}},
		{"web/src/client.ts", "client.ts.txt", []string{"1-1 ", "3-4 ", "6-8 ", "10-12 "}},
		{"notes.txt", "notes.txt", []string{"1-4 "}},
	}
	for _, c := range cases {
		src, err := os.ReadFile("testdata/" + c.data)
		if err != nil {
			t.Fatal(err)
		}
		ps := code.Split(c.file, src)
		if got := ranges(ps); !slices.Equal(got, c.want) {
			t.Errorf("%s: passages %q, want %q", c.file, got, c.want)
		}
		for _, p := range ps {
			if p.Kind != "code" || !strings.HasPrefix(p.Text, "```") || p.Norm == "" || len(p.Hash) != 32 {
				t.Errorf("%s: passage %+v", c.file, p)
			}
		}
	}
}

// TestSCN_DSG_001_LargeDeclarationsSplitIntoMembers checks that a class
// above the passage size splits into its methods, and that an unchanged
// method keeps its hash when another method changes.
func TestSCN_DSG_001_LargeDeclarationsSplitIntoMembers(t *testing.T) {
	var b strings.Builder
	b.WriteString("package big\n\ntype Store struct{}\n\n")
	method := func(i int, body string) string {
		return fmt.Sprintf("// M%d does work.\nfunc (s *Store) M%d() int {\n%s\treturn %d\n}\n\n", i, i, body, i)
	}
	pad := strings.Repeat("\t_ = 1 // padding line for the size\n", 12)
	for i := range 6 {
		b.WriteString(method(i, pad))
	}
	before := code.Split("big.go", []byte(b.String()))
	if len(before) < 7 {
		t.Fatalf("passages %q", ranges(before))
	}
	for _, p := range before {
		if len([]rune(p.Text)) > content.MaxPassageChars+20 {
			t.Fatalf("passage of %d chars", len(p.Text))
		}
	}
	changed := strings.Replace(b.String(), "return 3\n", "return 33\n", 1)
	after := code.Split("big.go", []byte(changed))
	kept := 0
	for _, p := range after {
		if slices.ContainsFunc(before, func(q content.Passage) bool { return string(q.Hash) == string(p.Hash) }) {
			kept++
		}
	}
	if kept != len(after)-1 {
		t.Fatalf("%d of %d passages kept their hash", kept, len(after))
	}
}
