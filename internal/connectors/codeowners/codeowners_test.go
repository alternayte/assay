package codeowners_test

import (
	"testing"

	"github.com/alternayte/assay/internal/connectors/codeowners"
	"github.com/alternayte/assay/internal/content"
)

func TestSCN_CON_007_OnlyTheDefaultRuleCreatesOwners(t *testing.T) {
	repo := content.EntityRef{Type: "repository", Key: "github:github.com:acme/payments-api"}
	file := "# Owners\n*  @acme/old-team\n/docs/  @acme/docs-team\n*.go @acme/go-guild\n" +
		"* @acme/Payments-Team @dana.acme sam@acme.test # default owners\n/api/ @acme/api-guild\n"
	facts := codeowners.Facts([]byte(file), repo, "github", "github.com")
	want := []struct{ typ, key string }{
		{"team", "github:github.com:team:acme/payments-team"},
		{"person", "github:github.com:user:dana.acme"},
		{"person", "github:github.com:email:sam@acme.test"},
	}
	if len(facts) != len(want) {
		t.Fatalf("facts %+v", facts)
	}
	for i, f := range facts {
		if f.Subject.Key != repo.Key || f.Predicate != "owned_by" || f.Object.Type != want[i].typ || f.Object.Key != want[i].key ||
			f.Source != "codeowners" || f.Quote != "* @acme/Payments-Team @dana.acme sam@acme.test" {
			t.Fatalf("fact %d %+v", i, f)
		}
	}
	if facts[0].Object.Name != "payments-team" {
		t.Fatalf("team name %q", facts[0].Object.Name)
	}
	if got := codeowners.Facts([]byte("/docs/ @acme/docs-team\n*.md @acme/writers\n"), repo, "github", "github.com"); len(got) != 0 {
		t.Fatalf("path rules created %+v", got)
	}
	if got := codeowners.Facts([]byte("* @acme/a\n*\n"), repo, "github", "github.com"); len(got) != 0 {
		t.Fatalf("a later default rule without owners kept %+v", got)
	}
}
