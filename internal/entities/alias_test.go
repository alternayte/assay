package entities_test

import (
	"testing"

	"github.com/alternayte/assay/internal/connectors/catalog"
	"github.com/alternayte/assay/internal/connectors/codeowners"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/entities"
)

func TestSCN_ENT_001_KeysAndAliases(t *testing.T) {
	for in, want := range map[string]string{
		"Payments-API (v2)":         "payments api v2",
		"  Team   Hermes ":          "team hermes",
		"Étude, Café!":              "étude café",
		"acme/payments-api":         "acme payments api",
		"@acme/Payments-Team":       "acme payments team",
		"PAY-1":                     "pay 1",
		"Peer Review Service (PRS)": "peer review service prs",
	} {
		if got := entities.NormalizeAlias(in); got != want {
			t.Errorf("NormalizeAlias(%q) = %q, want %q", in, got, want)
		}
	}

	repo := content.EntityRef{Type: "repository", Key: "github:github.com:acme/payments-api", Name: "payments-api"}
	facts, err := catalog.Facts([]byte("kind: Component\nmetadata:\n  name: Payments-API\nspec:\n  owner: group:finance/payments-team\n"),
		catalog.Repository{Ref: repo})
	if err != nil {
		t.Fatal(err)
	}
	owners := codeowners.Facts([]byte("* @acme/payments-team\n"), repo, "github", "github.com")
	refs := []content.EntityRef{repo, facts[0].Subject, *facts[0].Object, *owners[0].Object}
	wantKeys := []string{"github:github.com:acme/payments-api", "backstage:component:default/payments-api",
		"backstage:group:finance/payments-team", "github:github.com:team:acme/payments-team"}
	for i, r := range refs {
		if r.Key != wantKeys[i] {
			t.Errorf("key %d: %s, want %s", i, r.Key, wantKeys[i])
		}
		if err := entities.CheckRef(r.Type, r.Key); err != nil {
			t.Errorf("%s: %v", r.Key, err)
		}
	}

	if err := entities.CheckRef("gadget", "backstage:component:default/x"); err == nil {
		t.Error("an unknown type passes")
	}
	if err := entities.CheckRef("service", "payments-api"); err == nil {
		t.Error("a key without provider passes")
	}
	if err := entities.CheckRef("service", "acme:payments-api"); err == nil {
		t.Error("a key with an unknown provider passes")
	}
}
