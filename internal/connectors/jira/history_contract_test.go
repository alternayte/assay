//go:build contract

package jira_test

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/jira"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

// TestSCN_CON_020_JiraCommentsAndHistory pages through all comments past
// 100, keeps the newest comments above the cap, and gives a dated fact for
// each status, assignee and fix version change, on Data Center and Cloud.
func TestSCN_CON_020_JiraCommentsAndHistory(t *testing.T) {
	src, base := testkit.FakeSources(t)
	ctx := context.Background()
	scope := []byte(`{"projects":["PAY"]}`)
	limits := connectors.Limits{MaxItemBytes: 512 << 10}
	for _, tc := range []struct {
		name   string
		client *connectors.Client
		open   func(*connectors.Client, json.RawMessage, connectors.Rules, connectors.Limits) (*jira.Connector, error)
		api    string
	}{
		{"data center", testkit.SourceClient(t, base+"/jira"), jira.New, "/jira/rest/api/2/issue/"},
		{"cloud", cloudClient(t, base+fixtures.CloudSitePath, ""), jira.NewCloud, fixtures.CloudSitePath + "/rest/api/3/issue/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hits := func(since time.Time, suffix string) int {
				n := 0
				for _, h := range src.Hits() {
					if !h.At.Before(since) && strings.HasPrefix(h.Path, tc.api) && strings.HasSuffix(h.Path, suffix) {
						n++
					}
				}
				return n
			}
			k, err := tc.open(tc.client, scope, testkit.DefaultRules, limits)
			if err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			it, err := k.Fetch(ctx, "PAY-12")
			if err != nil {
				t.Fatal(err)
			}
			if n := hits(start, "/comment"); n != 2 {
				t.Fatalf("%d comment requests, want 2 pages of 100", n)
			}
			comments := section(it.Body, "## Comments")
			for i := 1; i <= 120; i++ {
				if !strings.Contains(comments, fmt.Sprintf("Build %d of", i)) {
					t.Fatalf("comment %d is missing", i)
				}
			}
			if strings.Index(comments, "Build 1 of") > strings.Index(comments, "Build 120 of") || strings.Contains(comments, "Assay read") {
				t.Fatalf("the comments are not all in order:\n%s", comments)
			}

			rules := testkit.DefaultRules
			rules.MaxCommentsPerIssue = 50
			capped, err := tc.open(tc.client, scope, rules, limits)
			if err != nil {
				t.Fatal(err)
			}
			it, err = capped.Fetch(ctx, "PAY-12")
			if err != nil {
				t.Fatal(err)
			}
			comments = section(it.Body, "## Comments")
			if !strings.Contains(comments, "Assay read the newest 50 of 120 comments.") || strings.Count(comments, "### ") != 50 ||
				strings.Contains(comments, "Build 70 of") || !strings.Contains(comments, "Build 71 of") || !strings.Contains(comments, "Build 120 of") {
				t.Fatalf("the capped comments are:\n%s", comments)
			}

			start = time.Now()
			pay1, err := k.Fetch(ctx, "PAY-1")
			if err != nil {
				t.Fatal(err)
			}
			want := 0
			if tc.name == "cloud" {
				// The fixture caps a changelog page at 4 entries.
				want = 2
			}
			if n := hits(start, "/changelog"); n != want {
				t.Fatalf("%d changelog requests, want %d", n, want)
			}
			day := func(s string) time.Time {
				d, _ := time.Parse(time.RFC3339, s)
				return d
			}
			dated := map[string]time.Time{}
			for _, f := range pay1.Facts {
				if f.Predicate == "status" && strings.HasPrefix(f.Quote, "20") {
					dated[f.Text] = f.At
				}
				if f.Predicate == "status" && f.Quote == "Status: Done" && !f.At.Equal(day("2026-02-01T09:00:00Z")) {
					t.Fatalf("the current status is dated %v", f.At)
				}
			}
			if !dated["To Do"].Equal(day("2026-01-10T10:00:00Z")) || !dated["In Progress"].Equal(day("2026-01-15T09:00:00Z")) ||
				!dated["Done"].Equal(day("2026-02-01T09:00:00Z")) || len(dated) != 3 {
				t.Fatalf("the status changes are %v", dated)
			}
			history := section(pay1.Body, "## History")
			if strings.Contains(history, "priority") || strings.Contains(history, "Medium") {
				t.Fatalf("the History holds another field:\n%s", history)
			}
			assignees := map[string]time.Time{}
			for _, f := range factsOf(it, "assigned_to") {
				assignees[f.Object.Name+" "+f.Quote[:4]] = f.At
			}
			if len(assignees) != 3 || !assignees["Sam Acme 2026"].Equal(day("2026-02-05T10:00:00Z")) ||
				!assignees["Priya Acme 2026"].Equal(day("2026-02-08T09:00:00Z")) || !assignees["Priya Acme Assi"].Equal(day("2026-02-08T09:00:00Z")) {
				t.Fatalf("the assignee facts are %v", assignees)
			}
		})
	}
}

// section returns the text of a ## section of a body.
func section(body, heading string) string {
	_, rest, ok := strings.Cut(body, heading+"\n")
	if !ok {
		return ""
	}
	if i := strings.Index(rest, "\n## "); i >= 0 {
		rest = rest[:i]
	}
	return rest
}

func factsOf(it *content.Item, predicate string) []content.Fact {
	var out []content.Fact
	for _, f := range it.Facts {
		if f.Predicate == predicate {
			out = append(out, f)
		}
	}
	return out
}
