package sync_test

import (
	"bytes"
	"encoding/pem"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/outbound"
	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_SYN_009_PurgeRemovesAllRows(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	_, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-jira", Kind: "jira", BaseURL: base,
		Scope: map[string]any{"projects": []string{"PAY", "REM"}}})
	env.RunNow(admin, conn, "sync")
	tables := []string{"source_items", "source_versions", "passages", "chunks", "claim_evidence", "entity_sources", "entity_aliases", "sync_runs"}
	for _, table := range tables {
		if env.Count("SELECT count(*) FROM "+table+" WHERE connection_id = $1", conn) == 0 {
			t.Fatalf("no %s rows before the purge", table)
		}
	}
	admin.Must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("/api/v1/connections/%d", conn), nil)
	for _, table := range append(tables, "connection_visibility", "connections WHERE id = $1 OR connection_id") {
		q := "SELECT count(*) FROM " + table + " WHERE connection_id = $1"
		if strings.Contains(table, "WHERE") {
			q = "SELECT count(*) FROM connections WHERE id = $1"
		}
		if n := env.Count(q, conn); n != 0 {
			t.Fatalf("%s: %d rows after the purge", table, n)
		}
	}
	if n := env.Count("SELECT count(*) FROM secrets WHERE name = $1", fmt.Sprintf("connection:%d:token", conn)); n != 0 {
		t.Fatal("the token is left after the purge")
	}
	if n := env.Count("SELECT count(*) FROM audit_log WHERE action = 'connections.purge' AND target = $1", fmt.Sprintf("connection:%d", conn)); n != 1 {
		t.Fatalf("want 1 purge audit entry, got %d", n)
	}
	var registry int64
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT id FROM connections WHERE kind = 'registry'").Scan(&registry); err != nil {
		t.Fatal(err)
	}
	r := admin.Do(http.MethodDelete, fmt.Sprintf("/api/v1/connections/%d", registry), nil)
	if r.Status != http.StatusConflict || r.Code() != "registry" {
		t.Fatalf("registry purge: %d %s", r.Status, r.Body)
	}
}

func TestSCN_SET_002_TokenNeverLeaves(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	var cipher []byte
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT ciphertext FROM secrets WHERE name = $1",
		fmt.Sprintf("connection:%d:token", conn)).Scan(&cipher); err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(cipher, []byte(fixtures.SourceToken)) {
		t.Fatal("the stored token contains the plaintext")
	}
	src.RevokeToken()
	run := env.RunNow(admin, conn, "sync")
	if run.Status != "failed" {
		t.Fatalf("run %+v", run)
	}
	var bodies [][]byte
	for _, path := range []string{"/api/v1/connections", fmt.Sprintf("/api/v1/connections/%d", conn), fmt.Sprintf("/api/v1/connections/%d/runs", conn), "/api/v1/audit"} {
		bodies = append(bodies, admin.Must(http.StatusOK, http.MethodGet, path, nil).Body)
	}
	bodies = append(bodies, admin.Do(http.MethodPost, "/api/v1/connections/test", map[string]any{"kind": "confluence",
		"baseUrl": base + "/confluence", "connectionId": conn}).Body)
	bodies = append(bodies, env.Logs.Bytes())
	for i, b := range bodies {
		if bytes.Contains(b, []byte(fixtures.SourceToken)) || bytes.Contains(b, []byte("Bearer")) {
			t.Fatalf("output %d contains the token or an Authorization value:\n%s", i, b)
		}
	}
	if !bytes.Contains(env.Logs.Bytes(), []byte("sync run finished")) {
		t.Fatal("the failed sync wrote no log line")
	}
}

func TestSCN_VIS_008_HiddenAndMissingGiveSame404(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	member, _ := env.Invite(admin, "member@acme.test", "member", "member-password-1")
	_, base := testkit.FakeSources(t)
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-restricted", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"RST"}}})
	env.RunNow(admin, conn, "sync")
	var hidden int64
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT max(id) FROM source_items WHERE connection_id = $1", conn).Scan(&hidden); err != nil {
		t.Fatal(err)
	}
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/items/%d", hidden), nil)
	a := member.Do(http.MethodGet, fmt.Sprintf("/api/v1/items/%d", hidden), nil)
	b := member.Do(http.MethodGet, fmt.Sprintf("/api/v1/items/%d", hidden+1000), nil)
	if a.Status != http.StatusNotFound || a.Status != b.Status || !bytes.Equal(a.Body, b.Body) {
		t.Fatalf("hidden: %d %s\nmissing: %d %s", a.Status, a.Body, b.Status, b.Body)
	}
}

func TestSCN_INS_006_ConnectionTestThroughProxy(t *testing.T) {
	tlsSrv := httptest.NewTLSServer(fixtures.NewSources())
	defer tlsSrv.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw}))
	listen := strings.TrimPrefix(tlsSrv.URL, "https://")
	_, port, _ := strings.Cut(listen, ":")
	base := "https://example.com:" + port + "/confluence"
	run := func(t *testing.T, settings outbound.Settings) (*testkit.Response, *testkit.Proxy) {
		proxy := testkit.StartProxy(t, listen)
		settings.HTTPSProxy = proxy.URL
		env := testkit.Start(t, testkit.WithOutbound(settings))
		admin := env.Setup("owner@acme.test")
		return admin.Do(http.MethodPost, "/api/v1/connections/test", map[string]any{"kind": "confluence", "baseUrl": base,
			"token": fixtures.SourceToken, "scope": map[string]any{"spaces": []string{"ENG"}}}), proxy
	}
	t.Run("with the extra CA", func(t *testing.T) {
		r, proxy := run(t, outbound.Settings{ExtraCAPEM: caPEM})
		if r.Status != http.StatusOK || !strings.Contains(string(r.Body), `"sample":7`) {
			t.Fatalf("test: %d %s", r.Status, r.Body)
		}
		if len(proxy.Targets()) == 0 {
			t.Fatal("the request did not go through the proxy")
		}
	})
	t.Run("without the extra CA", func(t *testing.T) {
		r, proxy := run(t, outbound.Settings{})
		if r.Status != http.StatusBadGateway || !strings.Contains(string(r.Body), "certificate") {
			t.Fatalf("want a TLS error, got %d %s", r.Status, r.Body)
		}
		if len(proxy.Targets()) == 0 {
			t.Fatal("the request did not go through the proxy")
		}
	})
}

func TestSCN_OPS_007_ContentURLsAreNotFetched(t *testing.T) {
	var hits atomic.Int32
	elsewhere := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { hits.Add(1) }))
	defer elsewhere.Close()
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	src.PutPage(fixtures.Page{ID: "1098", Space: "ENG", Title: "Links", Author: "sam.acme", Updated: time.Now().UTC(),
		Body: fmt.Sprintf(`<p>See <a href="%[1]s/doc">the doc</a>.</p><ac:image><ri:url ri:value="%[1]s/img.png"/></ac:image><img src="%[1]s/x.png"/>`, elsewhere.URL)})
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-public", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	run := env.RunNow(admin, conn, "sync")
	if run.Status != "succeeded" || run.Created != 8 {
		t.Fatalf("run %+v", run)
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("a URL from content was requested %d times", n)
	}

	// A source redirect to another host fails the run and is not followed.
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, elsewhere.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirect.Close()
	other := testkit.CreateConnection(t, admin, testkit.Connection{Name: "redirecting", Kind: "confluence", BaseURL: redirect.URL,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	run = env.RunNow(admin, other, "sync")
	if run.Status != "failed" || !strings.Contains(run.Error, "redirect") || hits.Load() != 0 {
		t.Fatalf("redirect run %+v, hits %d", run, hits.Load())
	}
}
