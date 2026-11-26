//go:build contract

package sync_test

import (
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

// norms returns the passage norms of an item in order.
func norms(t *testing.T, env *testkit.Env, conn int64, external string) []string {
	t.Helper()
	rows, err := env.Server.Pool().Query(t.Context(), `SELECT p.norm FROM passages p JOIN source_items i ON i.id = p.item_id
WHERE i.connection_id = $1 AND i.external_id = $2 ORDER BY p.ordinal`, conn, external)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var n string
		if err := rows.Scan(&n); err != nil {
			t.Fatal(err)
		}
		out = append(out, n)
	}
	return out
}

func startsWith(ns []string, authors ...string) bool {
	if len(ns) != len(authors) {
		return false
	}
	for i, a := range authors {
		if !strings.HasPrefix(ns[i], a+", ") {
			return false
		}
	}
	return true
}

type scopeList struct {
	Scopes []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"scopes"`
}

func (s scopeList) ids() []string {
	var out []string
	for _, o := range s.Scopes {
		out = append(out, o.ID+" "+o.Name)
	}
	return out
}

func TestSCN_CON_022_SlackChannelThreads(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)

	// The scope picker lists only the public channels where the bot is a
	// member.
	var sc scopeList
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/connections/scopes", map[string]any{"kind": "slack", "baseUrl": base + "/slack/api",
		"token": fixtures.SourceToken}).JSON(t, &sc)
	if got := sc.ids(); !slices.Equal(got, []string{"C0PAYMENTS #payments", "C0PLATFORM #platform"}) {
		t.Fatalf("scopes %v", got)
	}
	r := admin.Do(http.MethodPost, "/api/v1/connections/test", map[string]any{"kind": "slack", "baseUrl": base + "/slack/api", "token": "xoxb-wrong"})
	if r.Status != http.StatusBadGateway || !strings.Contains(string(r.Body), "invalid_auth") {
		t.Fatalf("a wrong token: %d %s", r.Status, r.Body)
	}

	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-slack", Kind: "slack", BaseURL: base,
		Scope: map[string]any{"channels": []string{fixtures.SlackPayments}}})
	roots := src.SlackThreads(fixtures.SlackPayments)
	ext := func(i int) string { return fixtures.SlackTeam + "/" + fixtures.SlackPayments + "/" + roots[i] }

	// Each thread is one item. The join event and the bot message are no
	// thread.
	first := env.RunNow(admin, conn, "sync")
	if first.Status != "succeeded" || first.Created != 4 {
		t.Fatalf("first sync %+v", first)
	}
	for _, i := range []int{3, 4} {
		if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id = $2", conn, ext(i)); n != 0 {
			t.Fatalf("root %d is stored", i)
		}
	}
	// Each message is one passage that starts with its author and date.
	// The bot reply is dropped.
	decision := norms(t, env, conn, ext(2))
	if !startsWith(decision, "Dana Ito", "Ravi Mehta", "Lena Park") || strings.Contains(strings.Join(decision, " "), "Deploy of") {
		t.Fatalf("decision passages %q", decision)
	}
	title, body := webBody(t, env, conn, ext(5))
	if !strings.HasPrefix(title, "#payments: @here The Kestrel ledger export moved to the ledger page.") {
		t.Fatalf("title %q", title)
	}
	for _, want := range []string{"*Kestrel ledger export*", "[the ledger page](https://wiki.acme.test/display/ENG/Ledger)", "@Dana Ito",
		"#platform", "access & keys", "```\nassay-export --since 2026-01-01 > ledger.csv\n```", "> Run it from the bastion host only.",
		"Do not click is not a link we keep."} {
		if !strings.Contains(body, want) {
			t.Fatalf("the body has no %q: %s", want, body)
		}
	}
	if strings.Contains(body, "javascript:") || strings.Contains(body, "U0DANA") {
		t.Fatalf("the body keeps a script link or a user ID: %s", body)
	}
	var link string
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT url FROM source_items WHERE connection_id = $1 AND external_id = $2",
		conn, ext(2)).Scan(&link); err != nil {
		t.Fatal(err)
	}
	if want := "https://acme-chat.slack.test/archives/C0PAYMENTS/p" + strings.Replace(roots[2], ".", "", 1); link != want {
		t.Fatalf("url %q, want %q", link, want)
	}
	// Each author is a person with a posted_in fact.
	if n := env.Count(`SELECT count(*) FROM claims c JOIN entities e ON e.id = c.subject_id JOIN claim_evidence v ON v.claim_id = c.id
WHERE e.key = 'slack:T0ACME:user:U0LENA' AND e.type = 'person' AND c.predicate = 'posted_in' AND v.source_type = 'slack'`); n != 2 {
		t.Fatalf("%d posted_in facts of Lena, want 2", n)
	}

	// A second sync reads replies of no thread.
	mark := len(src.Hits())
	second := env.RunNow(admin, conn, "sync")
	if second.Status != "succeeded" || second.Created+second.Updated != 0 {
		t.Fatalf("second sync %+v", second)
	}
	// replies counts the threads whose replies the connector read.
	replies := func(from int) int {
		threads := map[string]bool{}
		for _, h := range src.Hits()[from:] {
			if h.Path == "/slack/api/conversations.replies" {
				q, _ := url.ParseQuery(h.Query)
				threads[q.Get("ts")] = true
			}
		}
		return len(threads)
	}
	if n := replies(mark); n != 0 {
		t.Fatalf("the second sync read the replies of %d threads", n)
	}

	// A reply to an old thread gives a new version of that thread only.
	src.AddSlackReply(fixtures.SlackPayments, roots[2], "U0RAVI", "Confirmed in the Kestrel runbook.")
	mark = len(src.Hits())
	third := env.RunNow(admin, conn, "sync")
	if third.Status != "succeeded" || third.Updated != 1 || third.Created != 0 {
		t.Fatalf("sync after a reply %+v", third)
	}
	if got := norms(t, env, conn, ext(2)); !startsWith(got, "Dana Ito", "Ravi Mehta", "Lena Park", "Ravi Mehta") {
		t.Fatalf("passages after the reply %q", got)
	}
	if n := env.Count(`SELECT count(*) FROM source_versions v JOIN source_items i ON i.id = v.item_id
WHERE i.connection_id = $1 AND i.external_id = $2`, conn, ext(2)); n != 2 {
		t.Fatalf("%d versions of the thread, want 2", n)
	}
	if n := replies(mark); n != 1 {
		t.Fatalf("the sync read the replies of %d threads, want 1", n)
	}

	// A thread that is gone is a tombstone after reconcile.
	src.DeleteSlackThread(fixtures.SlackPayments, roots[0])
	if rec := env.RunNow(admin, conn, "reconcile"); rec.Status != "succeeded" || rec.Deleted != 1 {
		t.Fatalf("reconcile %+v", rec)
	}

	for _, h := range src.Hits() {
		if strings.HasPrefix(h.Path, "/slack/") && (h.Method != http.MethodGet || h.Auth != "Bearer") {
			t.Fatalf("the connector sent %s %s with %q", h.Method, h.Path, h.Auth)
		}
	}
}

func TestSCN_SYN_031_RetentionRemovesOldThreads(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	scope := map[string]any{"channels": []string{fixtures.SlackPayments}}
	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-slack", Kind: "slack", BaseURL: base, Scope: scope})
	if r := env.RunNow(admin, conn, "sync"); r.Status != "succeeded" || r.Created != 4 {
		t.Fatalf("sync %+v", r)
	}

	// Two threads have their newest message more than 30 days ago. A
	// retention of 30 days queues a reconcile that removes them.
	admin.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/connections/%d", conn), map[string]any{
		"name": "acme-slack", "kind": "slack", "baseUrl": base + "/slack/api", "scope": scope, "options": map[string]any{"retainDays": 30},
		"syncMinutes": 30, "reconcileHours": 24, "rateConcurrency": 2, "ratePerSecond": 100})
	var runID int64
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT id FROM sync_runs WHERE connection_id = $1 AND kind = 'reconcile' AND status = 'queued'",
		conn).Scan(&runID); err != nil {
		t.Fatalf("no queued reconcile after the change: %v", err)
	}
	if err := env.Server.Sync().Run(t.Context(), runID); err != nil {
		t.Fatal(err)
	}
	var deleted int
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT deleted FROM sync_runs WHERE id = $1", runID).Scan(&deleted); err != nil || deleted != 2 {
		t.Fatalf("the reconcile deleted %d threads, want 2 (%v)", deleted, err)
	}
	roots := src.SlackThreads(fixtures.SlackPayments)
	for i, want := range map[int]int{0: 0, 1: 0, 2: 1, 5: 1} {
		external := fixtures.SlackTeam + "/" + fixtures.SlackPayments + "/" + roots[i]
		if n := env.Count("SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id = $2", conn, external); n != want {
			t.Fatalf("thread %d: %d items, want %d", i, n, want)
		}
	}
	// A later reconcile does not store the old threads again.
	if r := env.RunNow(admin, conn, "reconcile"); r.Status != "succeeded" || r.Created != 0 || r.Deleted != 0 {
		t.Fatalf("second reconcile %+v", r)
	}
}

func TestSCN_CON_023_TeamsChannelThreads(t *testing.T) {
	idp, ibase := testkit.FakeIdentity(t)
	env := testkit.Start(t, testkit.WithEntraAuthority(ibase+"/entra"))
	admin := env.Setup("owner@acme.test")
	source := map[string]any{"kind": "teams", "baseUrl": fixtures.GraphBase(ibase), "token": fixtures.IdentityClientSecret,
		"options": map[string]any{"tenant": fixtures.IdentityTenant, "clientId": fixtures.IdentityClientID}}

	// The admin picks teams, then the standard channels of those teams.
	var v struct {
		User   string `json:"user"`
		Sample int    `json:"sample"`
	}
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/connections/test", source).JSON(t, &v)
	if v.User != fixtures.IdentityClientID || v.Sample != 2 {
		t.Fatalf("test %+v", v)
	}
	var sc scopeList
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/connections/scopes", source).JSON(t, &sc)
	if got := sc.ids(); !slices.Equal(got, []string{fixtures.TeamsPayments + " Acme Payments", fixtures.TeamsPlatform + " Acme Platform"}) {
		t.Fatalf("teams %v", got)
	}
	source["scope"] = map[string]any{"teams": []string{fixtures.TeamsPayments}}
	sc = scopeList{}
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/connections/scopes", source).JSON(t, &sc)
	part := fixtures.TeamsPayments + "/" + fixtures.TeamsDesign
	if got := sc.ids(); !slices.Equal(got, []string{part + " Acme Payments / Design reviews"}) {
		t.Fatalf("channels %v", got)
	}

	conn := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-teams", Kind: "teams", BaseURL: ibase,
		Scope: map[string]any{"teams": []string{fixtures.TeamsPayments}, "channels": []string{part}}})
	roots := idp.TeamsThreads(fixtures.TeamsPayments, fixtures.TeamsDesign)
	ext := func(i int) string { return part + "/" + roots[i] }
	first := env.RunNow(admin, conn, "sync")
	if first.Status != "succeeded" || first.Created != 3 {
		t.Fatalf("first sync %+v", first)
	}

	// The thread with more replies than one page reads them all. System
	// events and app messages are dropped.
	decision := norms(t, env, conn, ext(1))
	if !startsWith(decision, "Ada Park", "Grace Obi", "Linus Vale", "Grace Obi", "Ada Park") || strings.Contains(strings.Join(decision, " "), "Alert") {
		t.Fatalf("decision passages %q", decision)
	}
	title, body := webBody(t, env, conn, ext(1))
	if title != "Design reviews: Design review: how long does Payments API keep an Osprey refund request open?" {
		t.Fatalf("title %q", title)
	}
	for _, want := range []string{"does Payments API keep", "rules 👍", "**7 days**", "[the refund page](https://wiki.acme.test/display/ENG/Refunds)",
		"- Cancel after 7 days\n- Notify the customer"} {
		if !strings.Contains(body, want) {
			t.Fatalf("the body has no %q: %s", want, body)
		}
	}
	// A thread with a deleted root keeps its replies.
	if got := norms(t, env, conn, ext(2)); !startsWith(got, "Grace Obi") {
		t.Fatalf("passages of the thread with a deleted root %q", got)
	}
	if n := env.Count(`SELECT count(*) FROM claims c JOIN entities e ON e.id = c.subject_id
WHERE e.key = $1 AND e.type = 'person' AND c.predicate = 'posted_in'`, "teams:"+fixtures.IdentityTenant+":user:"+fixtures.UserLinus); n != 1 {
		t.Fatalf("%d posted_in facts of Linus, want 1", n)
	}

	// The next sync follows the delta link and stores nothing new. A new
	// reply gives a new version of its thread.
	mark := len(idp.Hits())
	if r := env.RunNow(admin, conn, "sync"); r.Status != "succeeded" || r.Created+r.Updated != 0 {
		t.Fatalf("second sync %+v", r)
	}
	delta := false
	for _, h := range idp.Hits()[mark:] {
		delta = delta || (strings.HasSuffix(h.Path, "/messages/delta") && strings.Contains(h.Query, "deltatoken"))
		if strings.HasSuffix(h.Path, "/messages") {
			t.Fatalf("the second sync listed the channel: %s", h.Path)
		}
	}
	if !delta {
		t.Fatal("the second sync did not follow the delta link")
	}
	idp.AddTeamsReply(fixtures.TeamsPayments, fixtures.TeamsDesign, roots[1], fixtures.UserKim, "<p>I add the 7 day rule to the Osprey runbook.</p>")
	if r := env.RunNow(admin, conn, "sync"); r.Status != "succeeded" || r.Updated != 1 || r.Created != 0 {
		t.Fatalf("sync after a reply %+v", r)
	}
	if got := norms(t, env, conn, ext(1)); !startsWith(got, "Ada Park", "Grace Obi", "Linus Vale", "Grace Obi", "Ada Park", "Kim Ross") {
		t.Fatalf("passages after the reply %q", got)
	}

	// A client secret that the tenant refuses pauses the connection.
	bad := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-teams-bad", Kind: "teams", BaseURL: ibase, Token: "wrong-secret",
		Scope: map[string]any{"teams": []string{fixtures.TeamsPayments}, "channels": []string{part}}})
	if r := env.RunNow(admin, bad, "sync"); r.Status != "failed" {
		t.Fatalf("sync with a wrong secret %+v", r)
	}
	if n := env.Count("SELECT count(*) FROM connections WHERE id = $1 AND status = 'paused'", bad); n != 1 {
		t.Fatal("a refused secret does not pause the connection")
	}

	// The one POST goes to the token endpoint. Every Graph read is a GET.
	for _, h := range idp.Hits() {
		switch {
		case h.Method == http.MethodPost && h.Path != "/entra/"+fixtures.IdentityTenant+"/oauth2/v2.0/token":
			t.Fatalf("the connector sent POST %s", h.Path)
		case strings.HasPrefix(h.Path, "/graph/") && h.Method != http.MethodGet:
			t.Fatalf("the connector sent %s %s", h.Method, h.Path)
		}
	}
}
