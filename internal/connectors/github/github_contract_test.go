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

func TestSCN_CON_004_GitHubIndexesOnlyMatchingFiles(t *testing.T) {
	want := []string{
		"acme/payments-api:.github/CODEOWNERS",
		"acme/payments-api:README.md",
		"acme/payments-api:api/openapi.yaml",
		"acme/payments-api:catalog-info.yaml",
		"acme/payments-api:deploy/prod.yaml",
		"acme/payments-api:docs/adr/0001-use-postgres.md",
		"acme/payments-api:docs/architecture.md",
		"acme/payments-api:go.mod",
		"acme/reminder-service:CODEOWNERS",
		"acme/reminder-service:README.md",
		"acme/reminder-service:catalog-info.yaml",
		"acme/reminder-service:package.json",
	}
	src, base := testkit.FakeSources(t)
	for _, tc := range []struct{ name, path, web string }{
		{"cloud", "/github", "/github"},
		{"enterprise server", "/ghes/api/v3", "/ghes"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := testkit.SourceClient(t, base+tc.path)
			k, err := github.New(c, []byte(`{"repositories":["acme/payments-api","acme/reminder-service"]}`),
				tree.Globs{Include: tree.DefaultGlobs}, connectors.Limits{MaxItemBytes: 512 << 10, MaxFilesPerRepo: 2000})
			if err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			v, err := k.Validate(ctx)
			if err != nil || v.User != "Assay Reader (assay-reader)" || v.Sample != len(want) {
				t.Fatalf("validation %+v %v", v, err)
			}
			scopes, err := k.Scopes(ctx)
			if err != nil || len(scopes) != 2 || scopes[0].ID != "acme/payments-api" {
				t.Fatalf("scopes %+v %v", scopes, err)
			}
			var got []string
			if err := k.Changed(ctx, time.Time{}, func(r connectors.Ref) error {
				if r.Version == "" {
					t.Errorf("%s has no blob ID", r.ID)
				}
				got = append(got, r.ID)
				return nil
			}); err != nil {
				t.Fatal(err)
			}
			slices.Sort(got)
			if !slices.Equal(got, want) {
				t.Fatalf("files\n got %v\nwant %v", got, want)
			}
			var ids []string
			if err := k.ListIDs(ctx, func(id string) error { ids = append(ids, id); return nil }); err != nil {
				t.Fatal(err)
			}
			slices.Sort(ids)
			if !slices.Equal(ids, want) {
				t.Fatalf("reconcile IDs %v", ids)
			}

			readme, err := k.Fetch(ctx, "acme/payments-api:README.md")
			if err != nil {
				t.Fatal(err)
			}
			if readme.Kind != "file" || readme.URL != base+tc.web+"/acme/payments-api/blob/main/README.md" ||
				!strings.HasPrefix(readme.Body, "# Payments API") || readme.Entity == nil || readme.Entity.Type != "repository" {
				t.Fatalf("readme %+v", readme)
			}
			mod, err := k.Fetch(ctx, "acme/payments-api:go.mod")
			if err != nil || !strings.HasPrefix(mod.Body, "```go-mod\nmodule github.com/acme/payments-api") {
				t.Fatalf("go.mod %+v %v", mod, err)
			}
			cat, err := k.Fetch(ctx, "acme/payments-api:catalog-info.yaml")
			if err != nil {
				t.Fatal(err)
			}
			if len(cat.Facts) == 0 {
				t.Fatal("the catalog file has no facts")
			}
			ps := content.Split(cat.Body)
			for _, f := range cat.Facts {
				if !slices.ContainsFunc(ps, func(p content.Passage) bool { return strings.Contains(p.Norm, content.Normalize(f.Quote)) }) {
					t.Fatalf("the quote %q of %s is in no passage", f.Quote, f.Predicate)
				}
			}
			owners, err := k.Fetch(ctx, "acme/payments-api:.github/CODEOWNERS")
			if err != nil || len(owners.Facts) != 1 || owners.Facts[0].Object.Name != "payments-team" {
				t.Fatalf("CODEOWNERS %+v %v", owners, err)
			}
			if _, err := k.Fetch(ctx, "acme/payments-api:cmd/server/main.go"); err != connectors.ErrNotFound {
				t.Fatalf("a file outside the globs: %v", err)
			}
			if _, err := k.Fetch(ctx, "acme/other:README.md"); err != connectors.ErrNotFound {
				t.Fatalf("a repository outside the scope: %v", err)
			}
		})
	}
	for _, h := range src.Hits() {
		if h.Method != "GET" {
			t.Fatalf("the connector sent %s %s", h.Method, h.Path)
		}
	}
}
