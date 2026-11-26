//go:build contract

package jira_test

import (
	"context"
	"encoding/base64"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/jira"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/outbound"
	"github.com/alternayte/assay/internal/testkit"
)

// cloudClient is a client with Basic email:token authentication, as Assay
// sends it to Atlassian Cloud. site is the base of links when it differs
// from base.
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
// Center and Cloud: the host in keys, and the person keys, which are user
// names on Data Center and account IDs on Cloud. A person compares by its
// name and email.
func portable(it *content.Item, host string) map[string]any {
	ref := func(e *content.EntityRef) *content.EntityRef {
		if e == nil {
			return nil
		}
		out := *e
		out.Key = strings.ReplaceAll(out.Key, host, "h")
		if out.Type == "person" {
			out.Key, out.Aliases = "person", nil
		}
		return &out
	}
	facts := []content.Fact{}
	for _, f := range it.Facts {
		f.Subject = *ref(&f.Subject)
		f.Object = ref(f.Object)
		facts = append(facts, f)
	}
	ents := []content.EntityRef{}
	for _, e := range it.Entities {
		ents = append(ents, *ref(&e))
	}
	return map[string]any{"externalId": it.ExternalID, "kind": it.Kind, "title": it.Title, "author": it.Author,
		"created": it.Created.UTC(), "updated": it.Updated.UTC(), "body": it.Body, "metadata": it.Metadata,
		"entity": ref(it.Entity), "entities": ents, "facts": facts}
}

func TestSCN_CON_012_JiraCloudMatchesDataCenter(t *testing.T) {
	src, base := testkit.FakeSources(t)
	ctx := context.Background()
	scope := []byte(`{"projects":["PAY","REM"]}`)
	limits := connectors.Limits{MaxItemBytes: 512 << 10}
	dcClient := testkit.SourceClient(t, base+"/jira")
	dc, err := jira.New(dcClient, scope, testkit.DefaultRules, limits)
	if err != nil {
		t.Fatal(err)
	}
	// The site is on another host name than the API host of the scoped
	// token: localhost and 127.0.0.1 of the same fixture server.
	site := strings.Replace(base, "127.0.0.1", "localhost", 1) + fixtures.CloudSitePath
	classicClient := cloudClient(t, base+fixtures.CloudSitePath, "")
	scopedClient := cloudClient(t, base+fixtures.CloudAPIPath+"/ex/jira/"+fixtures.CloudID, site)
	if scopedClient.Host() == strings.TrimPrefix(base, "http://") {
		t.Fatalf("the scoped client keys entities by the API host %s", scopedClient.Host())
	}

	for _, tc := range []struct {
		name   string
		client *connectors.Client
		site   string
		path   string
	}{
		{"classic token", classicClient, base + fixtures.CloudSitePath, fixtures.CloudSitePath + "/rest/api/3/"},
		{"scoped token", scopedClient, site, fixtures.CloudAPIPath + "/ex/jira/" + fixtures.CloudID + "/rest/api/3/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			start := time.Now()
			cloud, err := jira.NewCloud(tc.client, scope, testkit.DefaultRules, limits)
			if err != nil {
				t.Fatal(err)
			}
			v, err := cloud.Validate(ctx)
			if err != nil || v.User != "Assay Reader" || v.Sample != 7 || v.More {
				t.Fatalf("validation %+v %v", v, err)
			}
			scopes, err := cloud.Scopes(ctx)
			dcScopes, _ := dc.Scopes(ctx)
			if err != nil || !reflect.DeepEqual(scopes, dcScopes) {
				t.Fatalf("scopes %+v, want %+v (%v)", scopes, dcScopes, err)
			}
			parts, err := cloud.Parts(ctx)
			if err != nil || len(parts) != 2 {
				t.Fatalf("parts %+v %v", parts, err)
			}
			for _, part := range parts {
				var got, want []string
				if err := cloud.Changed(ctx, part, time.Time{}, func(r connectors.Ref) error { got = append(got, r.ID); return nil }); err != nil {
					t.Fatal(err)
				}
				if err := dc.Changed(ctx, part, time.Time{}, func(r connectors.Ref) error { want = append(want, r.ID); return nil }); err != nil {
					t.Fatal(err)
				}
				if !slices.Equal(got, want) || len(got) == 0 {
					t.Fatalf("part %s lists %v, want %v", part.Key, got, want)
				}
				var ids []string
				if err := cloud.ListIDs(ctx, part, func(id string) error { ids = append(ids, id); return nil }); err != nil {
					t.Fatal(err)
				}
				if slices.Sort(ids); !slices.Equal(ids, slices.Sorted(slices.Values(want))) {
					t.Fatalf("part %s IDs %v", part.Key, ids)
				}
			}
			for _, key := range []string{"PAY-1", "PAY-2", "PAY-3", "PAY-4", "REM-1", "REM-2", "REM-3"} {
				want, err := dc.Fetch(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
				got, err := cloud.Fetch(ctx, key)
				if err != nil {
					t.Fatal(err)
				}
				if g, w := portable(got, tc.client.Host()), portable(want, dcClient.Host()); !reflect.DeepEqual(g, w) {
					t.Fatalf("%s on Cloud:\n%+v\nwant\n%+v", key, g, w)
				}
				if got.URL != tc.site+"/browse/"+key {
					t.Fatalf("%s links to %s", key, got.URL)
				}
			}
			pay2, _ := cloud.Fetch(ctx, "PAY-2")
			for _, f := range pay2.Facts {
				if f.Predicate == "assigned_to" {
					if f.Object.Key != jira.PersonKey(tc.client.Host(), fixtures.AccountID("priya.acme")) || f.Object.Name != "Priya Acme" ||
						f.Object.Metadata["email"] != "priya.acme@acme.test" || !slices.Equal(f.Object.Aliases, []string{"Priya Acme"}) {
						t.Fatalf("the Cloud assignee is %+v", f.Object)
					}
				}
			}
			for _, h := range src.Hits() {
				// The Data Center requests of the comparison are under /jira.
				if h.At.Before(start) || strings.HasPrefix(h.Path, "/jira/") {
					continue
				}
				if !strings.HasPrefix(h.Path, tc.path) || h.Auth != "Basic" || (h.Method != "GET" && h.Method != "HEAD") {
					t.Fatalf("a request went to %s %s with %s", h.Method, h.Path, h.Auth)
				}
				if strings.HasSuffix(h.Path, "/rest/api/3/search") {
					t.Fatal("the connector used the removed search")
				}
			}
		})
	}

	// A user who hides the email is a person without metadata.
	src.HideEmail("priya.acme")
	cloud, _ := jira.NewCloud(classicClient, scope, testkit.DefaultRules, limits)
	pay2, err := cloud.Fetch(ctx, "PAY-2")
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range pay2.Facts {
		if f.Predicate == "assigned_to" && f.Object.Metadata != nil {
			t.Fatalf("a hidden email is in the metadata: %+v", f.Object.Metadata)
		}
	}
}
