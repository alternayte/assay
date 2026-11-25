//go:build contract

package sync_test

import (
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/connectors/tree"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

// hostHits counts the requests under prefix after mark whose path contains
// part.
func hostHits(src *fixtures.Sources, mark int, prefix, part string) int {
	n := 0
	for _, h := range src.Hits()[mark:] {
		if strings.HasPrefix(h.Path, prefix) && strings.Contains(h.Path, part) {
			n++
		}
	}
	return n
}

// onlyReads fails when a request under prefix is not GET or HEAD, or does
// not carry the token with auth.
func onlyReads(t *testing.T, src *fixtures.Sources, prefix, auth string) {
	t.Helper()
	n := 0
	for _, h := range src.Hits() {
		if !strings.HasPrefix(h.Path, prefix) {
			continue
		}
		n++
		if h.Method != http.MethodGet && h.Method != http.MethodHead {
			t.Fatalf("the connector sent %s %s", h.Method, h.Path)
		}
		if h.Auth != auth {
			t.Fatalf("%s %s has auth %q, want %q", h.Method, h.Path, h.Auth, auth)
		}
	}
	if n == 0 {
		t.Fatalf("no request reached %s", prefix)
	}
}

func TestSCN_CON_012_GitLabSyncFetchesOnlyChangedBlobs(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	scope := map[string]any{"repositories": []string{fixtures.GitLabRepo}}
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-gitlab", Kind: "gitlab", BaseURL: base, Scope: scope,
		Code: tree.DefaultCodeGlobs})
	var v struct {
		User   string `json:"user"`
		Sample int    `json:"sample"`
	}
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/connections/test", map[string]any{"kind": "gitlab", "baseUrl": base + "/gitlab",
		"connectionId": conn, "scope": scope}).JSON(t, &v)
	knowledge := src.RepoItems("gitlab", fixtures.GitLabRepo)
	if v.User != "Assay Reader (assay-reader)" || v.Sample != knowledge {
		t.Fatalf("test %+v, want %d files", v, knowledge)
	}
	var scopes struct {
		Scopes []struct {
			ID string `json:"id"`
		} `json:"scopes"`
	}
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/connections/scopes", map[string]any{"kind": "gitlab", "baseUrl": base + "/gitlab",
		"connectionId": conn}).JSON(t, &scopes)
	if len(scopes.Scopes) != 1 || scopes.Scopes[0].ID != fixtures.GitLabRepo {
		t.Fatalf("scopes %+v", scopes)
	}

	want := knowledge + src.RepoCodeItems("gitlab", fixtures.GitLabRepo)
	first := env.RunNow(admin, conn, "sync")
	if first.Status != "succeeded" || first.Created != want || len(first.Skipped) != 0 {
		t.Fatalf("first sync %+v, want %d new", first, want)
	}
	for _, f := range []string{"README.md", "docs/architecture.md", "catalog-info.yaml", "CODEOWNERS", "go.mod", "cmd/server/main.go"} {
		if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id = $2", conn, fixtures.GitLabRepo+":"+f); n != 1 {
			t.Fatalf("%s is not indexed", f)
		}
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND kind = 'code'", conn); n == 0 {
		t.Fatal("no code item is stored")
	}
	var link string
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT url FROM source_items WHERE connection_id = $1 AND external_id = $2",
		conn, fixtures.GitLabRepo+":docs/architecture.md").Scan(&link); err != nil {
		t.Fatal(err)
	}
	if link != base+"/gitlab/"+fixtures.GitLabRepo+"/-/blob/main/docs/architecture.md" {
		t.Fatalf("url %s", link)
	}
	host, _ := url.Parse(base)
	key := "gitlab:" + host.Host + ":" + fixtures.GitLabRepo
	if n := env.Count(`SELECT count(*) FROM claims c JOIN entities e ON e.id = c.subject_id
		WHERE e.key = $1 AND e.type = 'repository' AND c.predicate = 'implements' AND c.retracted_at IS NULL`, key); n != 1 {
		t.Fatalf("the repository %s has %d implements claims, want 1", key, n)
	}
	if n := env.Count(`SELECT count(*) FROM claims c JOIN entities e ON e.id = c.subject_id
		WHERE e.key = $1 AND c.predicate = 'owned_by' AND c.retracted_at IS NULL`, key); n != 1 {
		t.Fatalf("the repository %s has %d owned_by claims, want 1", key, n)
	}

	mark := len(src.Hits())
	second := env.RunNow(admin, conn, "sync")
	if second.Status != "succeeded" || second.Created != 0 || second.Updated != 0 || second.Unchanged != want {
		t.Fatalf("second sync %+v", second)
	}
	if n := hostHits(src, mark, "/gitlab/", "/repository/blobs/"); n != 0 {
		t.Fatalf("an unchanged tree fetched %d blobs", n)
	}

	src.PutFile("gitlab", fixtures.GitLabRepo, "docs/architecture.md",
		[]byte("# Architecture\n\nThe Billing API is a Go service. It stores invoices in PostgreSQL 18.\n"))
	mark = len(src.Hits())
	third := env.RunNow(admin, conn, "sync")
	if third.Status != "succeeded" || third.Updated != 1 || third.Created != 0 {
		t.Fatalf("third sync %+v", third)
	}
	if n := hostHits(src, mark, "/gitlab/", "/repository/blobs/"); n != 1 {
		t.Fatalf("one changed file fetched %d blobs", n)
	}
	onlyReads(t, src, "/gitlab/", "PRIVATE-TOKEN")
}

func TestSCN_CON_013_BitbucketReadsEveryPage(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	if n := len(src.RepoFiles("bitbucket", fixtures.BitbucketRepo)); n <= 500 {
		t.Fatalf("the fixture has %d files, want more than one page of 500", n)
	}
	scope := map[string]any{"repositories": []string{fixtures.BitbucketRepo}}
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-bitbucket", Kind: "bitbucket", BaseURL: base, Scope: scope,
		Code: tree.DefaultCodeGlobs})
	var v struct {
		User   string `json:"user"`
		Sample int    `json:"sample"`
	}
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/connections/test", map[string]any{"kind": "bitbucket", "baseUrl": base + "/bitbucket",
		"connectionId": conn, "scope": scope}).JSON(t, &v)
	knowledge := src.RepoItems("bitbucket", fixtures.BitbucketRepo)
	if v.User != "Assay Reader (assay-reader)" || v.Sample != knowledge {
		t.Fatalf("test %+v, want %d files", v, knowledge)
	}

	want := knowledge + src.RepoCodeItems("bitbucket", fixtures.BitbucketRepo)
	first := env.RunNow(admin, conn, "sync")
	if first.Status != "succeeded" || first.Created != want || len(first.Skipped) != 0 {
		t.Fatalf("first sync %+v, want %d new", first, want)
	}
	if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1", conn); n != want {
		t.Fatalf("%d items, want %d", n, want)
	}
	var link string
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT url FROM source_items WHERE connection_id = $1 AND external_id = $2",
		conn, fixtures.BitbucketRepo+":docs/runbook.md").Scan(&link); err != nil {
		t.Fatal(err)
	}
	if link != base+"/bitbucket/projects/PLAT/repos/billing-gateway/browse/docs/runbook.md?at=main" {
		t.Fatalf("url %s", link)
	}
	host, _ := url.Parse(base)
	key := "bitbucket:" + host.Host + ":" + strings.ToLower(fixtures.BitbucketRepo)
	if n := env.Count(`SELECT count(*) FROM claims c JOIN entities e ON e.id = c.subject_id
		WHERE e.key = $1 AND c.predicate = 'owned_by' AND c.retracted_at IS NULL`, key); n != 1 {
		t.Fatalf("the repository %s has %d owned_by claims, want 1", key, n)
	}

	mark := len(src.Hits())
	second := env.RunNow(admin, conn, "sync")
	if second.Status != "succeeded" || second.Updated != 0 || second.Unchanged != want {
		t.Fatalf("second sync %+v", second)
	}
	if n := hostHits(src, mark, "/bitbucket/", "/raw/"); n != 0 {
		t.Fatalf("an unchanged repository fetched %d files", n)
	}
	src.PutFile("bitbucket", fixtures.BitbucketRepo, "docs/runbook.md", []byte("# Runbook\n\nThe billing gateway sends a rejected file again after 10 minutes.\n"))
	mark = len(src.Hits())
	third := env.RunNow(admin, conn, "sync")
	if third.Status != "succeeded" || third.Updated != 1 {
		t.Fatalf("third sync %+v", third)
	}
	if n := hostHits(src, mark, "/bitbucket/", "/raw/"); n != 1 {
		t.Fatalf("one changed file fetched %d files", n)
	}
	onlyReads(t, src, "/bitbucket/", "Bearer")

	src.OmitNextPageStart()
	failed := env.RunNow(admin, conn, "sync")
	if failed.Status != "failed" || !strings.Contains(failed.Error, "Bitbucket returned no next page. Upgrade Bitbucket to 7.14 or later.") {
		t.Fatalf("sync without nextPageStart %+v", failed)
	}
}
