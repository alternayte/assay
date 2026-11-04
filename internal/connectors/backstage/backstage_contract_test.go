//go:build contract

package backstage_test

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/connectors/backstage"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_CON_011_BackstageReadsTheCatalog(t *testing.T) {
	src, base := testkit.FakeSources(t)
	k, err := backstage.New(testkit.SourceClient(t, base+"/backstage"), []byte(`{}`), []byte(`{"frontendUrl":"https://backstage.acme.test"}`),
		connectors.Limits{MaxItemBytes: 512 << 10})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	v, err := k.Validate(ctx)
	if err != nil || v.Sample != 10 {
		t.Fatalf("validation %+v, %v", v, err)
	}
	scopes, err := k.Scopes(ctx)
	if err != nil || len(scopes) != 1 || scopes[0].ID != "default" {
		t.Fatalf("scopes %+v, %v", scopes, err)
	}
	var refs []connectors.Ref
	if err := k.Changed(ctx, time.Time{}, func(r connectors.Ref) error { refs = append(refs, r); return nil }); err != nil {
		t.Fatal(err)
	}
	if len(refs) != 10 {
		t.Fatalf("%d entities, want 10 without the Location", len(refs))
	}
	var api, user *content.Item
	for _, r := range refs {
		if r.Item == nil || r.Version == "" {
			t.Fatalf("ref %+v has no item or etag", r)
		}
		switch r.ID {
		case "api:default/payments-rest":
			api = r.Item
		case "user:default/dana.acme":
			user = r.Item
		}
	}
	if api == nil || api.URL != "https://backstage.acme.test/catalog/default/api/payments-rest" || api.Title != "Payments REST API" {
		t.Fatalf("api item %+v", api)
	}
	ops := slices.IndexFunc(api.Facts, func(f content.Fact) bool {
		return f.Predicate == "has_operation" && f.Object.Key == "operation:backstage:api:default/payments-rest:POST /payments"
	})
	if ops < 0 {
		t.Fatalf("no operation in %+v", api.Facts)
	}
	f := api.Facts[ops]
	lines := strings.Split(api.Body, "\n")
	// The body is fenced: line n of the YAML is line n+1 of the body.
	if f.Line < 1 || f.Line+1 >= len(lines) || strings.TrimSpace(lines[f.Line]) != f.Quote {
		t.Fatalf("the operation quotes %q at line %d", f.Quote, f.Line)
	}
	if user == nil || strings.Contains(user.Body, "dana@acme.test") {
		t.Fatalf("user item %+v", user)
	}
	var ids []string
	if err := k.ListIDs(ctx, func(id string) error { ids = append(ids, id); return nil }); err != nil || len(ids) != 10 {
		t.Fatalf("ids %v, %v", ids, err)
	}
	it, err := k.Fetch(ctx, "component:default/payments-api")
	if err != nil || it.ExternalID != "component:default/payments-api" {
		t.Fatalf("fetch %+v, %v", it, err)
	}
	src.DeleteEntity("component:default/payments-api")
	if _, err := k.Fetch(ctx, "component:default/payments-api"); err != connectors.ErrNotFound {
		t.Fatalf("fetch of a removed entity: %v", err)
	}
}
