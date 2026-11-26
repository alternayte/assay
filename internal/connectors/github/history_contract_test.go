//go:build contract

package github_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/github"
	"github.com/alternayte/assay/internal/connectors/tree"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/testkit"
)

// quoted checks that the quote of each fact is in a passage of the body.
func quoted(t *testing.T, it *content.Item) {
	t.Helper()
	ps := content.Split(it.Body)
	for _, f := range it.Facts {
		if !slices.ContainsFunc(ps, func(p content.Passage) bool { return strings.Contains(p.Norm, content.Normalize(f.Quote)) }) {
			t.Fatalf("%s: the quote %q of %s is in no passage", it.ExternalID, f.Quote, f.Predicate)
		}
	}
}

// TestSCN_CON_012_GitHubHistory reads the merged pull requests and the
// issues of an Acme repository inside the window, with the review
// passages, the implements facts and the last commit of each file.
func TestSCN_CON_012_GitHubHistory(t *testing.T) {
	src, base := testkit.FakeSources(t)
	c := testkit.SourceClient(t, base+"/github")
	hist := connectors.History{Months: 24, PullRequests: true, Issues: true, Wiki: true, Now: time.Now,
		Jira: []connectors.JiraProject{{Host: "jira.acme.test", Key: "PAY"}}}
	k, err := github.New(c, []byte(`{"repositories":["acme/payments-api"]}`), tree.Globs{Include: tree.DefaultGlobs},
		connectors.Limits{MaxItemBytes: 512 << 10, MaxFilesPerRepo: 2000}, hist)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	part := connectors.Part{Key: "acme/payments-api"}
	var history []connectors.Ref
	if err := k.Changed(ctx, part, time.Time{}, func(r connectors.Ref) error {
		if strings.Contains(r.ID, "#") {
			history = append(history, r)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	var ids []string
	for i, r := range history {
		ids = append(ids, r.ID)
		if i > 0 && r.Updated.Before(history[i-1].Updated) {
			t.Fatalf("the history is not oldest first: %v", ids)
		}
	}
	// #15 is closed without a merge, #5 and #9 are older than the window.
	want := []string{"acme/payments-api#issue/12", "acme/payments-api#issue/13", "acme/payments-api#pr/14", "acme/payments-api#pr/16"}
	got := slices.Sorted(slices.Values(ids))
	if !slices.Equal(got, want) {
		t.Fatalf("history %v, want %v", got, want)
	}
	var listed []string
	if err := k.ListIDs(ctx, part, func(id string) error { listed = append(listed, id); return nil }); err != nil {
		t.Fatal(err)
	}
	for _, id := range want {
		if !slices.Contains(listed, id) {
			t.Fatalf("reconcile does not list %s", id)
		}
	}
	// A cursor at the update of #16 lists only #16 and the newer issue.
	var newer []string
	if err := k.Changed(ctx, part, history[len(history)-2].Updated, func(r connectors.Ref) error {
		if strings.Contains(r.ID, "#") {
			newer = append(newer, r.ID)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	if len(newer) != 2 {
		t.Fatalf("since the cursor: %v", newer)
	}

	pr, err := k.Fetch(ctx, "acme/payments-api#pr/14")
	if err != nil {
		t.Fatal(err)
	}
	if pr.Kind != "pull_request" || pr.Entity == nil || pr.Entity.Type != "change" ||
		pr.Entity.Key != "github:"+c.Host()+":acme/payments-api#pr/14" || !strings.HasSuffix(pr.URL, "/github/acme/payments-api/pull/14") {
		t.Fatalf("pull request %+v", pr)
	}
	paths, _ := pr.Metadata["changedPaths"].([]string)
	if !slices.Equal(paths, []string{"internal/payments/handler.go", "internal/payments/service.go"}) || pr.Metadata["mergeCommit"] == "" {
		t.Fatalf("metadata %+v", pr.Metadata)
	}
	var review []string
	for _, p := range content.Split(pr.Body) {
		if len(p.HeadingPath) > 0 && p.HeadingPath[0] == "Review" {
			review = append(review, p.Norm)
		}
	}
	if len(review) < 3 || !slices.ContainsFunc(review, func(s string) bool { return strings.Contains(s, "replayed capture") }) {
		t.Fatalf("review passages %q", review)
	}
	objects := map[string]bool{}
	for _, f := range pr.Facts {
		if f.Predicate == "implements" {
			objects[f.Object.Key] = true
		}
	}
	if !objects["github:"+c.Host()+":acme/payments-api#issue/12"] || !objects["jira:jira.acme.test:PAY-2"] || len(objects) != 2 {
		t.Fatalf("implements %v", objects)
	}
	quoted(t, pr)
	linked, err := k.Fetch(ctx, "acme/payments-api#pr/16")
	if err != nil {
		t.Fatal(err)
	}
	if len(linked.Facts) != 1 || linked.Facts[0].Object.Key != "github:"+c.Host()+":acme/payments-api#issue/13" {
		t.Fatalf("the timeline cross-reference gives %+v", linked.Facts)
	}
	quoted(t, linked)
	if _, err := k.Fetch(ctx, "acme/payments-api#pr/15"); err != connectors.ErrNotFound {
		t.Fatalf("a closed pull request without a merge: %v", err)
	}
	issue, err := k.Fetch(ctx, "acme/payments-api#issue/12")
	if err != nil {
		t.Fatal(err)
	}
	if issue.Kind != "issue" || issue.Entity.Type != "issue" || !strings.Contains(issue.Body, "## Comments") {
		t.Fatalf("issue %+v", issue)
	}
	quoted(t, issue)
	if _, err := k.Fetch(ctx, "acme/payments-api#issue/14"); err != connectors.ErrNotFound {
		t.Fatalf("a pull request as an issue: %v", err)
	}

	readme, err := k.Fetch(ctx, "acme/payments-api:README.md")
	if err != nil {
		t.Fatal(err)
	}
	if _, msg := content.CutLastChange(readme.Body); msg != "Add README.md" {
		t.Fatalf("the README has the last change %q", msg)
	}
	ps := content.Split(readme.Body)
	if last := ps[len(ps)-1]; len(last.HeadingPath) != 1 || last.HeadingPath[0] != content.LastChangeHeading {
		t.Fatalf("the last passage is %+v", last)
	}

	if !k.Keep("acme/payments-api#pr/5", time.Now().AddDate(-3, 0, 0)) || k.Keep("acme/payments-api#pr/14", time.Now()) ||
		k.Keep("acme/payments-api:README.md", time.Time{}) {
		t.Fatal("Keep keeps the wrong items")
	}
	for _, h := range src.Hits() {
		if h.Method != "GET" && h.Method != "HEAD" {
			t.Fatalf("the connector sent %s %s", h.Method, h.Path)
		}
	}
}
