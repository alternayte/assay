//go:build contract

package sync_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

// rewriteRun is a run with the counts of its key rewrite.
type rewriteRun struct {
	ID      int64  `json:"id"`
	Kind    string `json:"kind"`
	Status  string `json:"status"`
	Deleted int    `json:"deleted"`
	Error   string `json:"error"`
	Rewrite *struct {
		From      string `json:"from"`
		To        string `json:"to"`
		Items     int    `json:"items"`
		Entities  int    `json:"entities"`
		People    int    `json:"people"`
		Unmatched int    `json:"unmatched"`
	} `json:"rewrite"`
}

// switchToCloud switches a connection to the Cloud site, works the full
// sync and the reconciliation that it queues, and returns both runs.
func switchToCloud(t *testing.T, env *testkit.Env, admin *testkit.Client, conn int64, site string, scoped bool) (rewriteRun, rewriteRun) {
	t.Helper()
	var q struct {
		RunID int64 `json:"runId"`
	}
	admin.Must(http.StatusAccepted, http.MethodPost, fmt.Sprintf("/api/v1/connections/%d/cloud", conn), map[string]any{
		"baseUrl": site, "email": fixtures.Email("assay.reader"), "token": fixtures.SourceToken, "scoped": scoped,
	}).JSON(t, &q)
	if err := env.Server.Sync().Run(t.Context(), q.RunID); err != nil {
		t.Fatal(err)
	}
	var reconcileID int64
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT id FROM sync_runs WHERE connection_id = $1 AND kind = 'reconcile' AND status = 'queued'",
		conn).Scan(&reconcileID); err != nil {
		t.Fatalf("the switch queued no reconciliation: %v", err)
	}
	if err := env.Server.Sync().Run(t.Context(), reconcileID); err != nil {
		t.Fatal(err)
	}
	var runs struct {
		Runs []rewriteRun `json:"runs"`
	}
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/connections/%d/runs", conn), nil).JSON(t, &runs)
	var syncRun, rec rewriteRun
	for _, r := range runs.Runs {
		switch r.ID {
		case q.RunID:
			syncRun = r
		case reconcileID:
			rec = r
		}
	}
	if syncRun.Status != "succeeded" || syncRun.Rewrite == nil || rec.Status != "succeeded" {
		t.Fatalf("switch sync %+v, reconciliation %+v", syncRun, rec)
	}
	return syncRun, rec
}

type claimRow struct {
	ID       int64
	Subject  int64
	Evidence int
}

// claimsOf returns the claims with current evidence of a connection by ID.
func claimsOf(t *testing.T, env *testkit.Env, conn int64) map[int64]claimRow {
	t.Helper()
	rows, err := env.Server.Pool().Query(t.Context(), `
		SELECT c.id, c.subject_id, count(*) FROM claims c JOIN claim_evidence e ON e.claim_id = c.id
		WHERE e.connection_id = $1 AND e.state = 'current' AND c.retracted_at IS NULL GROUP BY c.id`, conn)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	out := map[int64]claimRow{}
	for rows.Next() {
		var c claimRow
		if err := rows.Scan(&c.ID, &c.Subject, &c.Evidence); err != nil {
			t.Fatal(err)
		}
		out[c.ID] = c
	}
	return out
}

func TestSCN_SYN_021_SwitchToCloudKeepsClaimsAndEvidence(t *testing.T) {
	src, base := testkit.FakeSources(t)
	// The site is on localhost, and its downloads redirect to the media
	// host on 127.0.0.1.
	env := testkit.Start(t, testkit.WithAtlassianAPI(base+fixtures.CloudAPIPath), testkit.WithAtlassianMedia(base))
	admin := env.Setup("owner@acme.test")
	jiraConn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-jira", Kind: "jira", BaseURL: base,
		Scope: map[string]any{"projects": []string{"PAY", "REM"}}})
	wikiConn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-engineering", Kind: "confluence", BaseURL: base,
		Scope: map[string]any{"spaces": []string{"ENG"}}})
	for _, c := range []int64{jiraConn, wikiConn} {
		if r := env.RunNow(admin, c, "sync"); r.Status != "succeeded" {
			t.Fatalf("Data Center sync %+v", r)
		}
	}
	dcHost := strings.TrimPrefix(base, "http://")
	entities := env.Count("SELECT count(*) FROM entities")
	jiraClaims, wikiClaims := claimsOf(t, env, jiraConn), claimsOf(t, env, wikiConn)
	if len(jiraClaims) == 0 || len(wikiClaims) == 0 {
		t.Fatalf("no claims before the switch: %d Jira, %d Confluence", len(jiraClaims), len(wikiClaims))
	}
	pay2 := itemID(t, env, jiraConn, "PAY-2")
	glossary := itemID(t, env, wikiConn, "1008")
	var adr int64
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT id FROM entities WHERE key = $1", "confluence:"+dcHost+":1003").Scan(&adr); err != nil {
		t.Fatal(err)
	}

	// The Cloud site is on another host name than the Data Center server.
	siteHost := strings.Replace(dcHost, "127.0.0.1", "localhost", 1)
	site := "http://" + siteHost + fixtures.CloudSitePath
	// Jira Cloud answers one issue with 429 and Retry-After: 2.
	scopedIssue := fixtures.CloudAPIPath + "/ex/jira/" + fixtures.CloudID + "/rest/api/3/issue/PAY-2"
	src.Fail(scopedIssue, http.StatusTooManyRequests, 1, "2")
	start := time.Now()
	jiraRun, jiraRec := switchToCloud(t, env, admin, jiraConn, site, true)
	if w := jiraRun.Rewrite; w.From != dcHost || w.To != siteHost || w.Items != 8 || w.People != 4 || w.Unmatched != 0 || w.Entities == 0 {
		t.Fatalf("Jira rewrite %+v", w)
	}
	var first, retry time.Time
	for _, h := range src.Hits() {
		if h.At.Before(start) {
			continue
		}
		if h.Path == scopedIssue {
			if first.IsZero() {
				first = h.At
			} else if retry.IsZero() {
				retry = h.At
			}
		}
		if strings.HasPrefix(h.Path, fixtures.CloudSitePath+"/rest/") {
			t.Fatalf("a scoped token request went to the site: %s", h.Path)
		}
	}
	if retry.IsZero() || retry.Sub(first) < 2*time.Second {
		t.Fatalf("the retry came %v after the 429", retry.Sub(first))
	}
	wikiRun, wikiRec := switchToCloud(t, env, admin, wikiConn, site, false)
	// Nine pages and three attachments match their Cloud items.
	if w := wikiRun.Rewrite; w.Items != 12 || w.People != 0 || w.Entities == 0 {
		t.Fatalf("Confluence rewrite %+v", w)
	}
	if jiraRec.Deleted != 0 || wikiRec.Deleted != 0 {
		t.Fatalf("the reconciliations tombstoned %d and %d items", jiraRec.Deleted, wikiRec.Deleted)
	}

	if n := env.Count("SELECT count(*) FROM entities"); n != entities {
		t.Fatalf("%d entities after the switch, want %d", n, entities)
	}
	if n := env.Count("SELECT count(*) FROM entities WHERE key LIKE $1", "%"+dcHost+"%"); n != 0 {
		t.Fatalf("%d entity keys keep the Data Center host", n)
	}
	for name, before := range map[string]map[int64]claimRow{"Jira": jiraClaims, "Confluence": wikiClaims} {
		conn := jiraConn
		if name == "Confluence" {
			conn = wikiConn
		}
		after := claimsOf(t, env, conn)
		for id, c := range before {
			if a, ok := after[id]; !ok || a.Subject != c.Subject || a.Evidence < c.Evidence {
				t.Errorf("%s claim %d: before %+v, after %+v", name, id, c, a)
			}
		}
	}
	// The items and the decision keep their IDs, with Cloud links and keys.
	if id := itemID(t, env, jiraConn, "PAY-2"); id != pay2 {
		t.Fatalf("PAY-2 is item %d, was %d", id, pay2)
	}
	if id := itemID(t, env, wikiConn, fixtures.CloudPageID("1008")); id != glossary {
		t.Fatalf("the glossary is item %d, was %d", id, glossary)
	}
	var url string
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT url FROM source_items WHERE id = $1", pay2).Scan(&url); err != nil {
		t.Fatal(err)
	}
	if url != site+"/browse/PAY-2" {
		t.Fatalf("PAY-2 links to %s", url)
	}
	var adrKey string
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT key FROM entities WHERE id = $1", adr).Scan(&adrKey); err != nil {
		t.Fatal(err)
	}
	if adrKey != "confluence:"+siteHost+":"+fixtures.CloudPageID("1003") {
		t.Fatalf("the decision key is %s", adrKey)
	}
	var priya string
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT key FROM entities WHERE metadata->>'email' = $1",
		fixtures.Email("priya.acme")).Scan(&priya); err != nil {
		t.Fatal(err)
	}
	if priya != "jira:"+siteHost+":user:"+fixtures.AccountID("priya.acme") {
		t.Fatalf("the person key is %s", priya)
	}

	var details []json.RawMessage
	rows, err := env.Server.Pool().Query(t.Context(), "SELECT details FROM audit_log WHERE action = 'connections.switch' ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	for rows.Next() {
		var d json.RawMessage
		if err := rows.Scan(&d); err != nil {
			t.Fatal(err)
		}
		details = append(details, d)
	}
	rows.Close()
	if len(details) != 2 || !strings.Contains(string(details[0]), `"people": 4`) && !strings.Contains(string(details[0]), `"people":4`) {
		t.Fatalf("switch audit entries %s", details)
	}

	// A second switch is refused.
	admin.Must(http.StatusConflict, http.MethodPost, fmt.Sprintf("/api/v1/connections/%d/cloud", jiraConn), map[string]any{
		"baseUrl": site, "email": fixtures.Email("assay.reader"), "token": fixtures.SourceToken})
	again := env.RunNow(admin, jiraConn, "sync")
	if again.Status != "succeeded" || again.Created != 0 {
		t.Fatalf("the sync after the switch %+v", again)
	}
	if n := env.Count("SELECT count(*) FROM entities"); n != entities {
		t.Fatalf("%d entities after a second sync, want %d", n, entities)
	}
	if !slices.ContainsFunc(src.Hits(), func(h fixtures.Hit) bool { return h.Path == fixtures.CloudSitePath+"/_edge/tenant_info" }) {
		t.Fatal("Assay did not read the cloud ID from the tenant info")
	}
}

func TestSCN_SYN_022_CloudPersonWithoutEmailIsNewPerson(t *testing.T) {
	src, base := testkit.FakeSources(t)
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-jira", Kind: "jira", BaseURL: base,
		Scope: map[string]any{"projects": []string{"PAY", "REM"}}})
	env.RunNow(admin, conn, "sync")
	people := env.Count("SELECT count(*) FROM entities WHERE type = 'person'")
	src.HideEmail("sam.acme")
	siteHost := strings.Replace(strings.TrimPrefix(base, "http://"), "127.0.0.1", "localhost", 1)
	run, _ := switchToCloud(t, env, admin, conn, "http://"+siteHost+fixtures.CloudSitePath, false)
	if run.Rewrite.People != 3 || run.Rewrite.Unmatched != 1 {
		t.Fatalf("rewrite %+v", run.Rewrite)
	}
	if n := env.Count("SELECT count(*) FROM entities WHERE type = 'person'"); n != people+1 {
		t.Fatalf("%d people, want %d", n, people+1)
	}
	if n := env.Count("SELECT count(*) FROM entities WHERE key = $1 AND metadata = '{}'",
		"jira:"+siteHost+":user:"+fixtures.AccountID("sam.acme")); n != 1 {
		t.Fatal("the Cloud person without email is not a new entity")
	}
}
