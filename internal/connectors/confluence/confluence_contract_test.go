//go:build contract

package confluence_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/confluence"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/testkit"
)

type goldenItem struct {
	ExternalID string             `json:"externalId"`
	Kind       string             `json:"kind"`
	Title      string             `json:"title"`
	URL        string             `json:"url"`
	Author     string             `json:"author"`
	EditedBy   string             `json:"editedBy"`
	Updated    time.Time          `json:"updated"`
	Metadata   map[string]any     `json:"metadata"`
	Entity     *content.EntityRef `json:"entity"`
	Body       []string           `json:"body"`
}

func TestSCN_CON_003_ConfluenceMarkdownMatchesGolden(t *testing.T) {
	_, base := testkit.FakeSources(t)
	c := testkit.SourceClient(t, base+"/confluence")
	k, err := confluence.New(c, []byte(`{"spaces":["ENG"]}`), testkit.DefaultRules, connectors.Limits{MaxItemBytes: 512 << 10})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	v, err := k.Validate(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if v.User != "Assay Reader" || v.Sample != 8 {
		t.Fatalf("validation %+v", v)
	}
	var ids []string
	if err := k.ListIDs(ctx, func(id string) error { ids = append(ids, id); return nil }); err != nil {
		t.Fatal(err)
	}
	slices.Sort(ids)
	if len(ids) != 8 || slices.Contains(ids, "2001") {
		t.Fatalf("ids %v", ids)
	}
	for _, id := range []string{"1001", "1002", "1003", "1005"} {
		it, err := k.Fetch(ctx, id)
		if err != nil {
			t.Fatal(err)
		}
		g := goldenItem{it.ExternalID, it.Kind, it.Title, it.URL[len(base):], it.Author, it.EditedBy, it.Updated, it.Metadata, it.Entity, nil}
		g.Body = strings.Split(it.Body, "\n")
		if g.Entity != nil {
			g.Entity.Key = strings.ReplaceAll(g.Entity.Key, c.Host(), "fixtures")
		}
		testkit.Golden(t, "testdata/page-"+id+".json", g)
	}
	adr, err := k.Fetch(ctx, "1003")
	if err != nil {
		t.Fatal(err)
	}
	if adr.Entity == nil || adr.Entity.Type != "decision" {
		t.Fatalf("a page labelled adr is not a decision: %+v", adr.Entity)
	}
	runbook, _ := k.Fetch(ctx, "1002")
	if runbook.Entity != nil {
		t.Fatalf("a runbook is a %s", runbook.Entity.Type)
	}
	if _, err := k.Fetch(ctx, "9999"); err != connectors.ErrNotFound {
		t.Fatalf("missing page: %v", err)
	}
}
