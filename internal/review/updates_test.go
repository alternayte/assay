package review_test

import (
	"net/http"
	"slices"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
)

type update struct {
	Name    string `json:"name"`
	NewTerm bool   `json:"newTerm"`
	Items   []struct {
		Title   string `json:"title"`
		Changed bool   `json:"changed"`
	} `json:"items"`
	Conflicts []claim `json:"conflicts"`
}

func updates(t *testing.T, cl *testkit.Client) map[string]update {
	t.Helper()
	var out struct {
		Entities []update `json:"entities"`
	}
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/updates", nil).JSON(t, &out)
	m := map[string]update{}
	for _, u := range out.Entities {
		m[u.Name] = u
	}
	return m
}

func TestUpdatesGroupChangesByEntity(t *testing.T) {
	w := start(t)
	id := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-eng", Kind: "confluence", BaseURL: w.base, Path: "/confluence",
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	testkit.ShareWithEveryone(t, w.env, w.owner, id)
	sync := func() {
		if r := w.env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync: %+v", r)
		}
	}
	sync()
	p, ok := w.src.Page("1008")
	if !ok {
		t.Fatal("no glossary page")
	}
	p.Body += `<table><tbody><tr><th>Term</th><th>Definition</th></tr>
<tr><td>Editor</td><td>The person who checks the language of a manuscript.</td></tr>
<tr><td>Peer review</td><td>The check of a manuscript by other experts.</td></tr></tbody></table>`
	w.src.PutPage(p)
	sync()

	us := updates(t, w.member)
	editor, ok := us["Editor"]
	if !ok || !slices.ContainsFunc(editor.Items, func(i struct {
		Title   string `json:"title"`
		Changed bool   `json:"changed"`
	}) bool {
		return i.Title == "Acme glossary" && i.Changed
	}) || len(editor.Conflicts) < 2 {
		t.Fatalf("Editor: %+v", editor)
	}
	if peer := us["Peer review"]; !peer.NewTerm {
		t.Fatalf("Peer review: %+v", peer)
	}
	restricted := func(us map[string]update) int {
		n := 0
		for _, u := range us {
			for _, i := range u.Items {
				if slices.Contains([]string{"Project Falcon acquisition plan", "ADR-009 Select Falcon Typesetting", "Security incident review 2026-01"}, i.Title) {
					n++
				}
			}
		}
		return n
	}
	if n := restricted(us); n != 0 {
		t.Fatalf("the member sees %d restricted items", n)
	}
	if restricted(updates(t, w.owner)) == 0 {
		t.Fatal("the owner sees no restricted item")
	}
}
