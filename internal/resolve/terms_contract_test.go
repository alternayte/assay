//go:build contract

package resolve_test

import (
	"context"
	"slices"
	"testing"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/confluence"
	"github.com/alternayte/assay/internal/resolve"
	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_VOC_002_DeterministicTermSources(t *testing.T) {
	_, base := testkit.FakeSources(t)
	c := testkit.SourceClient(t, base+"/confluence")
	k, err := confluence.New(c, []byte(`{"spaces":["ENG"]}`), testkit.DefaultRules, connectors.Limits{MaxItemBytes: 512 << 10})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	// A glossary table gives defined_as facts with the row as the quote.
	it, err := k.Fetch(ctx, "1008")
	if err != nil {
		t.Fatal(err)
	}
	defs := map[string]string{}
	for _, f := range it.Facts {
		if f.Predicate == "defined_as" && f.Subject.Type == "term" {
			defs[f.Subject.Name] = f.Text
			if !slices.Contains([]string{"| Manuscript | A paper that an author sends to a journal for review. |",
				"| Article Processing Charge | The fee that an author pays when a journal accepts a paper. |",
				"| Editor | The person who decides whether a journal accepts a manuscript. |"}, f.Quote) {
				t.Errorf("quote %q is not a table row", f.Quote)
			}
		}
	}
	if len(defs) != 3 || defs["Manuscript"] != "A paper that an author sends to a journal for review." {
		t.Errorf("glossary definitions %v", defs)
	}
	// A page without the glossary label gives no definitions.
	other, err := k.Fetch(ctx, "1007")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range other.Facts {
		if f.Predicate == "defined_as" {
			t.Errorf("page 1007 gives a definition: %+v", f)
		}
	}
	// "Peer Review Service (PRS)" gives an alias.
	var prs bool
	for _, e := range other.Entities {
		prs = prs || (e.Type == "term" && e.Name == "Peer Review Service" && slices.Contains(e.Aliases, "PRS"))
	}
	if !prs {
		t.Errorf("no PRS alias in %+v", other.Entities)
	}

	// Phrase candidates need 3 items in 2 connections shared with Everyone.
	w := start(t)
	public := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: w.base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	testkit.ShareWithEveryone(t, w.env, w.owner, public)
	if r := w.env.RunNow(w.owner, public, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	cands, err := w.env.Server.Resolve().TermCandidates(ctx, resolve.TermRules{MinItems: 3, MinConnections: 2})
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]resolve.Phrase{}
	for _, p := range cands {
		got[p.Text] = p
	}
	if p, ok := got["Open Access Week"]; !ok || p.Items != 3 || len(p.Connections) != 2 {
		t.Errorf("Open Access Week is %+v, want 3 items in 2 connections", p)
	}
	for _, no := range []string{"Review Board", "Billing Sync", "Payments API", "Team Hermes"} {
		if _, ok := got[no]; ok {
			t.Errorf("%s is a candidate", no)
		}
	}
}
