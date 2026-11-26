//go:build contract

package sync_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

// TestSCN_SYN_024_RepositoryHistory syncs the Acme GitHub and Azure DevOps
// fixtures with their history: search finds a merged pull request by a word
// of its review, get_item returns its review passages, the pull request
// implements the issue that its body closes and the Jira issue that it
// names, and every work item and wiki page is an item. With historyMonths
// 0, sync reads no pull requests and reconcile keeps the items.
func TestSCN_SYN_024_RepositoryHistory(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	u, _ := url.Parse(base)
	host := u.Host
	jira := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-jira", Kind: "jira", BaseURL: base,
		Scope: map[string]any{"projects": []string{"PAY"}}})
	if r := env.RunNow(admin, jira, "sync"); r.Status != "succeeded" {
		t.Fatalf("jira sync %+v", r)
	}
	gh := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-github", Kind: "github", BaseURL: base,
		Scope: map[string]any{"repositories": []string{"acme/payments-api"}}})
	want := src.RepoItems("github", "acme/payments-api") + src.HistoryItems("github", "acme/payments-api")
	if r := env.RunNow(admin, gh, "sync"); r.Status != "succeeded" || r.Created != want {
		t.Fatalf("github sync %+v, want %d new", r, want)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND kind = 'pull_request'", gh); n != 2 {
		t.Fatalf("%d pull requests", n)
	}

	var found struct {
		Results []struct {
			ItemID int64  `json:"itemId"`
			Kind   string `json:"kind"`
			Title  string `json:"title"`
		} `json:"results"`
	}
	admin.Must(http.StatusOK, http.MethodGet, "/api/v1/search?q=replayed", nil).JSON(t, &found)
	i := slices.IndexFunc(found.Results, func(r struct {
		ItemID int64  `json:"itemId"`
		Kind   string `json:"kind"`
		Title  string `json:"title"`
	}) bool {
		return r.Kind == "pull_request"
	})
	if i < 0 || !strings.Contains(found.Results[i].Title, "#14") {
		t.Fatalf("search for a review word: %+v", found.Results)
	}
	var item struct {
		Kind     string `json:"kind"`
		Passages []struct {
			HeadingPath []string `json:"headingPath"`
			Text        string   `json:"text"`
		} `json:"passages"`
	}
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/items/%d", found.Results[i].ItemID), nil).JSON(t, &item)
	var body, review bool
	for _, p := range item.Passages {
		if len(p.HeadingPath) > 0 && p.HeadingPath[0] == "Description" && strings.Contains(p.Text, "card network drops") {
			body = true
		}
		if len(p.HeadingPath) > 0 && p.HeadingPath[0] == "Review" && strings.Contains(p.Text, "replayed capture") {
			review = true
		}
	}
	if item.Kind != "pull_request" || !body || !review {
		t.Fatalf("get_item %+v", item)
	}

	implements := func(object, quote string) int {
		return env.Count(`SELECT count(*) FROM claims c
			JOIN entities s ON s.id = c.subject_id
			JOIN entities o ON o.id = c.object_entity_id
			JOIN claim_evidence e ON e.claim_id = c.id
			JOIN passages p ON p.item_id = e.item_id AND p.hash = e.passage_hash
			WHERE c.predicate = 'implements' AND c.retracted_at IS NULL AND s.type = 'change' AND s.key = $1 AND o.key = $2
			  AND strpos(p.norm, $3) > 0`, "github:"+host+":acme/payments-api#pr/14", object, quote)
	}
	if n := implements("github:"+host+":acme/payments-api#issue/12", "Closes #12"); n != 1 {
		t.Fatalf("the pull request has %d implements claims on the issue that it closes", n)
	}
	if n := implements("jira:"+host+":PAY-2", "Tracks PAY-2"); n != 1 {
		t.Fatalf("the pull request has %d implements claims on the Jira issue", n)
	}
	if n := env.Count("SELECT count(*) FROM entities WHERE key = $1 AND type = 'issue'", "jira:"+host+":PAY-2"); n != 1 {
		t.Fatal("the Jira issue lost its type")
	}
	if n := env.Count("SELECT count(*) FROM passages p JOIN source_items i ON i.id = p.item_id WHERE i.connection_id = $1 AND i.external_id = $2 AND p.heading_path[1] = 'Last change'",
		gh, "acme/payments-api:README.md"); n != 1 {
		t.Fatalf("the README has %d last change passages", n)
	}

	// historyMonths 0: no pull request request, and reconcile keeps the
	// stored history.
	if _, err := env.Server.Pool().Exec(t.Context(), `UPDATE connections SET options = '{"historyMonths": 0}' WHERE id = $1`, gh); err != nil {
		t.Fatal(err)
	}
	mark := len(src.Hits())
	if r := env.RunNow(admin, gh, "sync"); r.Status != "succeeded" || r.Created != 0 {
		t.Fatalf("sync without history %+v", r)
	}
	for _, h := range src.Hits()[mark:] {
		if strings.Contains(h.Path, "/pulls") || strings.Contains(h.Path, "/issues") {
			t.Fatalf("sync without history read %s", h.Path)
		}
	}
	if r := env.RunNow(admin, gh, "reconcile"); r.Status != "succeeded" || r.Deleted != 0 {
		t.Fatalf("reconcile without history %+v", r)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id LIKE '%#%'", gh); n != src.HistoryItems("github", "acme/payments-api") {
		t.Fatalf("reconcile left %d history items", n)
	}

	// A pull request that the source no longer returns inside the window
	// goes at the next reconcile.
	if _, err := env.Server.Pool().Exec(t.Context(), `UPDATE connections SET options = '{}' WHERE id = $1`, gh); err != nil {
		t.Fatal(err)
	}
	src.DeletePull("github", "acme/payments-api", 16)
	if r := env.RunNow(admin, gh, "reconcile"); r.Status != "succeeded" || r.Deleted != 1 {
		t.Fatalf("reconcile of a deleted pull request %+v", r)
	}

	ado := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-azdo", Kind: "azdo", BaseURL: base,
		Path: "/azdo/" + fixtures.AzdoCollection, Scope: map[string]any{"repositories": []string{"Platform/ledger-service"}}})
	want = src.RepoItems("azdo", "Platform/ledger-service") + src.HistoryItems("azdo", "Platform/ledger-service") + src.ProjectItems("Platform")
	if r := env.RunNow(admin, ado, "sync"); r.Status != "succeeded" || r.Created != want {
		t.Fatalf("azdo sync %+v, want %d new", r, want)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND kind = 'work_item'", ado); n != 250 {
		t.Fatalf("%d work items, want 250", n)
	}
	if n := env.Count(`SELECT count(*) FROM source_items i JOIN source_versions v ON v.id = i.version_id
		WHERE i.connection_id = $1 AND i.kind = 'page' AND v.body LIKE '# %'`, ado); n != 3 {
		t.Fatalf("%d wiki pages with their Markdown, want 3", n)
	}
	mark = len(src.Hits())
	if r := env.RunNow(admin, ado, "sync"); r.Status != "succeeded" || r.Created != 0 || r.Updated != 0 {
		t.Fatalf("second azdo sync %+v", r)
	}
	for _, h := range src.Hits()[mark:] {
		if strings.Contains(h.Path, "/_apis/wiki/wikis/") && strings.Contains(h.Path, "/pages/") {
			t.Fatalf("an unchanged wiki page was read again: %s", h.Path)
		}
	}
	for _, h := range src.Hits() {
		if h.Method != http.MethodGet && h.Method != http.MethodHead {
			t.Fatalf("a connector sent %s %s", h.Method, h.Path)
		}
	}
}
