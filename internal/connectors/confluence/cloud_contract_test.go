//go:build contract

package confluence_test

import (
	"context"
	"encoding/base64"
	"errors"
	"net/url"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/confluence"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/outbound"
	"github.com/alternayte/assay/internal/testkit"
)

func cloudClient(t *testing.T, base, site string) *connectors.Client {
	t.Helper()
	c, err := connectors.NewClient(outbound.Settings{}, connectors.ClientOptions{BaseURL: base, SiteURL: site,
		Authorization: "Basic " + base64.StdEncoding.EncodeToString([]byte("assay.reader@acme.test:"+fixtures.SourceToken)),
		Concurrency:   4, PerSecond: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// portable gives an item without the parts that differ between Data
// Center and Cloud: the host and the page ID in keys.
func portable(it *content.Item, host, id string) map[string]any {
	key := func(k string) string {
		return strings.ReplaceAll(strings.ReplaceAll(k, strings.ToLower(host), "h"), host, "h")
	}
	var ent *content.EntityRef
	if it.Entity != nil {
		e := *it.Entity
		e.Key = strings.TrimSuffix(key(e.Key), ":"+id)
		ent = &e
	}
	facts := []content.Fact{}
	for _, f := range it.Facts {
		f.Subject.Key = key(f.Subject.Key)
		facts = append(facts, f)
	}
	ents := []content.EntityRef{}
	for _, e := range it.Entities {
		e.Key = key(e.Key)
		ents = append(ents, e)
	}
	return map[string]any{"kind": it.Kind, "title": it.Title, "author": it.Author, "editedBy": it.EditedBy,
		"created": it.Created.UTC(), "updated": it.Updated.UTC(), "body": it.Body, "revision": it.Revision,
		"metadata": it.Metadata, "entity": ent, "entities": ents, "facts": facts}
}

func TestSCN_CON_017_ConfluenceCloudMatchesDataCenter(t *testing.T) {
	src, base := testkit.FakeSources(t)
	ctx := context.Background()
	scope := []byte(`{"spaces":["ENG"]}`)
	limits := connectors.Limits{MaxItemBytes: 512 << 10}
	dcClient := testkit.SourceClient(t, base+"/confluence")
	dc, err := confluence.New(dcClient, scope, testkit.DefaultRules, limits)
	if err != nil {
		t.Fatal(err)
	}
	site := strings.Replace(base, "127.0.0.1", "localhost", 1) + fixtures.CloudSitePath
	dcIDs := []string{"1001", "1002", "1003", "1004", "1005", "1006", "1007", "1008"}

	for _, tc := range []struct {
		name   string
		client *connectors.Client
		site   string
		path   string
	}{
		{"classic token", cloudClient(t, base+fixtures.CloudSitePath, ""), base + fixtures.CloudSitePath, fixtures.CloudSitePath + "/wiki/"},
		{"scoped token", cloudClient(t, base+fixtures.CloudAPIPath+"/ex/confluence/"+fixtures.CloudID, site), site,
			fixtures.CloudAPIPath + "/ex/confluence/" + fixtures.CloudID + "/wiki/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			cloud, err := confluence.NewCloud(tc.client, scope, testkit.DefaultRules, limits)
			if err != nil {
				t.Fatal(err)
			}
			v, err := cloud.Validate(ctx)
			if err != nil || v.User != "Assay Reader" || v.Sample != 8 || v.More {
				t.Fatalf("validation %+v %v", v, err)
			}
			scopes, err := cloud.Scopes(ctx)
			dcScopes, _ := dc.Scopes(ctx)
			if err != nil || !reflect.DeepEqual(scopes, dcScopes) {
				t.Fatalf("scopes %+v, want %+v (%v)", scopes, dcScopes, err)
			}
			part := connectors.Part{Key: "ENG"}
			var ids []string
			if err := cloud.ListIDs(ctx, part, func(id string) error { ids = append(ids, id); return nil }); err != nil {
				t.Fatal(err)
			}
			want := make([]string, len(dcIDs))
			for i, id := range dcIDs {
				want[i] = fixtures.CloudPageID(id)
			}
			if slices.Sort(ids); !slices.Equal(ids, want) {
				t.Fatalf("IDs %v, want %v", ids, want)
			}
			listed := map[string]*content.Item{}
			if err := cloud.Changed(ctx, part, time.Time{}, func(r connectors.Ref) error { listed[r.ID] = r.Item; return nil }); err != nil {
				t.Fatal(err)
			}
			for _, id := range dcIDs {
				w, err := dc.Fetch(ctx, id)
				if err != nil {
					t.Fatal(err)
				}
				cid := fixtures.CloudPageID(id)
				got, err := cloud.Fetch(ctx, cid)
				if err != nil {
					t.Fatal(err)
				}
				wp := portable(w, dcClient.Host(), id)
				for name, it := range map[string]*content.Item{"fetch": got, "listing": listed[cid]} {
					if it == nil {
						t.Fatalf("the listing has no item %s", cid)
					}
					if g := portable(it, tc.client.Host(), cid); !reflect.DeepEqual(g, wp) {
						t.Fatalf("%s of %s on Cloud:\n%+v\nwant\n%+v", name, cid, g, wp)
					}
					if it.ExternalID != cid {
						t.Fatalf("%s of %s has the ID %s", name, cid, it.ExternalID)
					}
					if p, _ := src.Page(id); it.URL != tc.site+"/wiki/spaces/ENG/pages/"+cid+"/"+strings.ReplaceAll(url.PathEscape(p.Title), "%20", "+") {
						t.Fatalf("%s of %s links to %s", name, cid, it.URL)
					}
				}
			}
			adr, _ := cloud.Fetch(ctx, fixtures.CloudPageID("1003"))
			if adr.Entity == nil || adr.Entity.Type != "decision" || adr.Entity.Key != "confluence:"+tc.client.Host()+":"+fixtures.CloudPageID("1003") {
				t.Fatalf("the Cloud decision is %+v", adr.Entity)
			}
			if _, err := cloud.Fetch(ctx, "9999"); !errors.Is(err, connectors.ErrNotFound) {
				t.Fatalf("missing page: %v", err)
			}
			for _, h := range src.Hits() {
				if h.At.Before(start) || strings.HasPrefix(h.Path, "/confluence/") {
					continue
				}
				if !strings.HasPrefix(h.Path, tc.path) || h.Auth != "Basic" || (h.Method != "GET" && h.Method != "HEAD") {
					t.Fatalf("a request went to %s %s with %s", h.Method, h.Path, h.Auth)
				}
				if strings.Contains(h.Path, "/rest/api/content/") && !strings.HasSuffix(h.Path, "/rest/api/content/search") {
					t.Fatalf("the connector read a page through REST v1: %s", h.Path)
				}
			}
		})
	}
}
