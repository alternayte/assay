package design_test

import (
	"fmt"
	"slices"
	"testing"

	"github.com/alternayte/assay/internal/design"
)

func summary(cs []design.Component) []string {
	out := make([]string, len(cs))
	for i, c := range cs {
		out[i] = fmt.Sprintf("%s %s %s %d", c.Path, c.Kind, c.Name, len(c.Files))
	}
	return out
}

// TestSCN_DSG_002_ComponentsComeFromCodeStructure pins the component rules:
// Go packages under cmd/ and internal/, .csproj, workspace package.json and
// top-level Python packages, the root for the rest, and the merge of the
// smallest component into its parent at the limit.
func TestSCN_DSG_002_ComponentsComeFromCodeStructure(t *testing.T) {
	files := []design.File{
		{Path: "README.md", Passages: 4},
		{Path: "main.go", Code: true, Passages: 2},
		{Path: "cmd/server/main.go", Code: true, Passages: 3},
		{Path: "internal/sync/run.go", Code: true, Passages: 20},
		{Path: "internal/sync/store/store.go", Code: true, Passages: 5},
		{Path: "internal/auth/auth.go", Code: true, Passages: 1},
		{Path: "src/Ledger/Ledger.csproj", Code: true, Passages: 1},
		{Path: "src/Ledger/Post.cs", Code: true, Passages: 9},
		{Path: "web/package.json", Code: true, Passages: 1},
		{Path: "web/src/app.ts", Code: true, Passages: 6},
		{Path: "fraud/__init__.py", Code: true, Passages: 1},
		{Path: "fraud/rules/__init__.py", Code: true, Passages: 1},
		{Path: "fraud/rules/score.py", Code: true, Passages: 4},
	}
	got := summary(design.Components("payments-api", files, 12))
	want := []string{". root payments-api 1", "cmd/server go server 1", "fraud python fraud 3", "internal/auth go auth 1",
		"internal/sync go sync 1", "internal/sync/store go store 1", "src/Ledger csproj Ledger 2", "web npm web 2"}
	if !slices.Equal(got, want) {
		t.Fatalf("components\n%q\nwant\n%q", got, want)
	}
	got = summary(design.Components("payments-api", files, 5))
	want = []string{". root payments-api 3", "fraud python fraud 3", "internal/sync go sync 2", "src/Ledger csproj Ledger 2",
		"web npm web 2"}
	if !slices.Equal(got, want) {
		t.Fatalf("merged components\n%q\nwant\n%q", got, want)
	}
}
