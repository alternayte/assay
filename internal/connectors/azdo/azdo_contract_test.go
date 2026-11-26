//go:build contract

package azdo_test

import (
	"context"
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/azdo"
	"github.com/alternayte/assay/internal/connectors/tree"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/outbound"
	"github.com/alternayte/assay/internal/testkit"
)

// TestSCN_CON_019_AzdoHistory reads the completed pull requests of an Acme
// repository and the work items and wiki pages of its project, with GET
// requests only: the reporting revisions list the work items, the work
// items list reads them 200 at a time, and the page tree lists the wiki.
func TestSCN_CON_019_AzdoHistory(t *testing.T) {
	src, base := testkit.FakeSources(t)
	c, err := connectors.NewClient(outbound.Settings{}, connectors.ClientOptions{BaseURL: base + "/azdo/" + fixtures.AzdoCollection,
		Authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte(":"+fixtures.SourceToken)), Concurrency: 4, PerSecond: 1000})
	if err != nil {
		t.Fatal(err)
	}
	hist := connectors.History{Months: 24, PullRequests: true, Issues: true, Wiki: true, Now: time.Now,
		InitiativeTypes: []string{"Epic"}}
	k, err := azdo.New(c, []byte(`{"repositories":["Platform/ledger-service"]}`), nil, tree.Globs{Include: tree.DefaultGlobs},
		connectors.Limits{MaxItemBytes: 512 << 10, MaxFilesPerRepo: 2000}, hist)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	parts, err := k.Parts(ctx)
	if err != nil || len(parts) != 2 || parts[0].Key != "Platform/ledger-service" || parts[1].Key != "Platform" {
		t.Fatalf("parts %+v %v", parts, err)
	}

	var pulls []string
	if err := k.Changed(ctx, parts[0], time.Time{}, func(r connectors.Ref) error {
		if strings.Contains(r.ID, "#") {
			pulls = append(pulls, r.ID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(pulls, []string{"Platform/ledger-service#pr/101"}) {
		t.Fatalf("pull requests %v", pulls)
	}
	pr, err := k.Fetch(ctx, pulls[0])
	if err != nil {
		t.Fatal(err)
	}
	if pr.Kind != "pull_request" || pr.Entity.Type != "change" || len(pr.Facts) != 1 ||
		pr.Facts[0].Object.Key != "azdo:"+c.Host()+":platform#wi/7" || pr.Facts[0].Quote != "Work item: 7" {
		t.Fatalf("pull request %+v", pr)
	}
	ps := content.Split(pr.Body)
	if !slices.ContainsFunc(ps, func(p content.Passage) bool { return strings.Contains(p.Norm, "Work item: 7") }) {
		t.Fatalf("no passage holds the work item link:\n%s", pr.Body)
	}
	var review int
	for _, p := range ps {
		if len(p.HeadingPath) > 0 && p.HeadingPath[0] == "Review" && !strings.HasPrefix(p.Norm, "Policy") {
			review++
		}
		if strings.Contains(p.Norm, "Policy check passed") {
			t.Fatal("a system comment is a passage")
		}
	}
	if review != 2 {
		t.Fatalf("%d review passages:\n%s", review, pr.Body)
	}
	if paths, _ := pr.Metadata["changedPaths"].([]string); !slices.Equal(paths, []string{"src/Ledger.cs"}) {
		t.Fatalf("changed paths %v", pr.Metadata["changedPaths"])
	}
	if _, err := k.Fetch(ctx, "Platform/ledger-service#pr/102"); err != connectors.ErrNotFound {
		t.Fatalf("an active pull request: %v", err)
	}

	var wikis, items []connectors.Ref
	if err := k.Changed(ctx, parts[1], time.Time{}, func(r connectors.Ref) error {
		_, kind, _, _ := connectors.ParseHistoryID(r.ID)
		switch kind {
		case connectors.KindWiki:
			wikis = append(wikis, r)
		case connectors.KindWorkItem:
			items = append(items, r)
		default:
			t.Errorf("the project lists %s", r.ID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(items) != 250 || len(wikis) != 3 {
		t.Fatalf("%d work items and %d wiki pages", len(items), len(wikis))
	}
	for _, r := range items {
		if r.Item == nil || r.Item.Kind != "work_item" || r.Updated.IsZero() {
			t.Fatalf("work item ref %+v", r)
		}
	}
	epic := slices.IndexFunc(items, func(r connectors.Ref) bool { return r.ID == "Platform#wi/8" })
	if epic < 0 || items[epic].Item.Entity.Type != "initiative" || !strings.Contains(items[epic].Item.Body, "stays in balance") {
		t.Fatalf("the epic %+v", items[epic].Item)
	}
	for _, r := range wikis {
		if r.Version == "" {
			t.Fatalf("wiki page %s has no version", r.ID)
		}
	}
	page, err := k.Fetch(ctx, "Platform#wiki/2")
	if err != nil {
		t.Fatal(err)
	}
	if page.Kind != "page" || page.Title != "Posting rules" || !strings.HasPrefix(page.Body, "# Posting rules\n\nEach entry has a debit line") {
		t.Fatalf("wiki page %+v", page)
	}
	wi, err := k.Fetch(ctx, "Platform#wi/7")
	if err != nil || wi.Entity.Type != "issue" || !strings.Contains(wi.Body, "partial post") {
		t.Fatalf("work item %+v %v", wi, err)
	}

	file, err := k.Fetch(ctx, "Platform/ledger-service:README.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, msg := content.CutLastChange(file.Body); msg != "Add README.md" {
		t.Fatalf("the last change is %q", msg)
	}
	for _, h := range src.Hits() {
		if h.Method != "GET" && h.Method != "HEAD" {
			t.Fatalf("the connector sent %s %s", h.Method, h.Path)
		}
		if strings.HasSuffix(h.Path, "/_apis/wit/workitems") && strings.Count(h.Query, "%2C")+1 > 200 {
			t.Fatalf("a work item request has more than 200 IDs: %s", h.Query)
		}
	}
}
