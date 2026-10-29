//go:build contract

package jira_test

import (
	"context"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/jira"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/testkit"
)

type goldenIssue struct {
	Title    string         `json:"title"`
	URL      string         `json:"url"`
	Author   string         `json:"author"`
	Metadata map[string]any `json:"metadata"`
	Entity   *content.EntityRef
	Facts    []content.Fact `json:"facts"`
	Body     []string       `json:"body"`
}

func TestSCN_CON_002_JiraIssuesMatchGolden(t *testing.T) {
	_, base := testkit.FakeSources(t)
	c := testkit.SourceClient(t, base+"/jira")
	k, err := jira.New(c, []byte(`{"projects":["PAY","REM"]}`), testkit.DefaultRules, connectors.Limits{MaxItemBytes: 512 << 10})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	v, err := k.Validate(ctx)
	if err != nil || v.Sample != 7 {
		t.Fatalf("validation %+v %v", v, err)
	}
	host := c.Host()
	for _, key := range []string{"PAY-1", "PAY-2", "REM-2", "REM-3"} {
		it, err := k.Fetch(ctx, key)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range it.Facts {
			found := false
			for _, p := range content.Split(it.Body) {
				if strings.Contains(p.Norm, content.Normalize(f.Quote)) {
					found = true
				}
			}
			if !found {
				t.Fatalf("%s: the quote %q of %s is in no passage", key, f.Quote, f.Predicate)
			}
		}
		raw := strings.ReplaceAll(mustJSON(t, goldenIssue{it.Title, it.URL[len(base):], it.Author, it.Metadata, it.Entity, it.Facts,
			strings.Split(it.Body, "\n")}), host, "fixtures")
		testkit.Golden(t, "testdata/"+key+".json", rawJSON(raw))
	}
}
