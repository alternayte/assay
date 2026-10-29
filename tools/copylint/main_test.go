package main

import (
	"os"
	"strings"
	"testing"
)

func TestSCN_UI_004_CopyLint(t *testing.T) {
	data, err := os.ReadFile("../../web/src/copy/en.json")
	if err != nil {
		t.Fatal(err)
	}
	findings, err := Lint(data)
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range findings {
		t.Errorf("%s: %s: %q", f.Key, f.Rule, f.Text)
	}

	long := strings.TrimSpace(strings.Repeat("word ", 21)) + "."
	if got := LintString("test.long", long); len(got) != 1 {
		t.Fatalf("a 21-word sentence gives %d findings, want 1", len(got))
	}
	if got := LintString("test.ok", strings.TrimSpace(strings.Repeat("word ", 20))+". Next sentence."); len(got) != 0 {
		t.Fatalf("a 20-word sentence fails: %v", got)
	}
	if got := LintString("test.banned", "Use a robust check."); len(got) != 1 {
		t.Fatalf("a banned word gives %d findings, want 1", len(got))
	}
	if _, err := Lint([]byte(`{"a": 1}`)); err == nil {
		t.Fatal("a value that is not a string passes")
	}
}
