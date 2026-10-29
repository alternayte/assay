package entities_test

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

type evidence struct {
	Stance     string `json:"stance"`
	SourceType string `json:"sourceType"`
}

type claim struct {
	ID            int64      `json:"id"`
	Predicate     string     `json:"predicate"`
	SubjectKey    string     `json:"subjectKey"`
	ObjectKey     string     `json:"objectKey"`
	ObjectName    string     `json:"objectName"`
	ObjectText    string     `json:"objectText"`
	Status        string     `json:"status"`
	ConflictNoted bool       `json:"conflictNoted"`
	Current       bool       `json:"current"`
	ValidFrom     *time.Time `json:"validFrom"`
	Evidence      []evidence `json:"evidence"`
}

type detail struct {
	Key      string   `json:"key"`
	Type     string   `json:"type"`
	Name     string   `json:"name"`
	Aliases  []string `json:"aliases"`
	Claims   []claim  `json:"claims"`
	Incoming []claim  `json:"incoming"`
	Timeline []claim  `json:"timeline"`
}

type world struct {
	env              *testkit.Env
	src              *fixtures.Sources
	owner, member    *testkit.Client
	public, jira     int64
	github, restrict int64
	host             string
}

// start syncs the public space, Jira and GitHub, shared with Everyone, and
// the restricted space of the owner.
func start(t *testing.T) *world {
	t.Helper()
	env := testkit.Start(t)
	w := &world{env: env, owner: env.Setup("owner@acme.test")}
	w.member, _ = env.Invite(w.owner, "member@acme.test", "member", "member-password-1")
	var base string
	w.src, base = testkit.FakeSources(t)
	u, _ := url.Parse(base)
	w.host = u.Host
	mk := func(name, kind string, scope map[string]any, share bool) int64 {
		id := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: name, Kind: kind, BaseURL: base, Scope: scope})
		if share {
			testkit.ShareWithEveryone(t, env, w.owner, id)
		}
		if r := env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync %s: %+v", name, r)
		}
		return id
	}
	w.public = mk("acme-public", "confluence", map[string]any{"spaces": []string{"ENG"}}, true)
	w.jira = mk("acme-jira", "jira", map[string]any{"projects": []string{"PAY", "REM"}}, true)
	w.github = mk("acme-github", "github", map[string]any{"repositories": []string{"acme/payments-api", "acme/reminder-service"}}, true)
	w.restrict = mk("acme-restricted", "confluence", map[string]any{"spaces": []string{"RST"}}, false)
	return w
}

func path(key string) string { return "/api/v1/entities/" + url.PathEscape(key) }

func get(t *testing.T, cl *testkit.Client, key string) detail {
	t.Helper()
	var d detail
	cl.Must(http.StatusOK, http.MethodGet, path(key), nil).JSON(t, &d)
	return d
}

func (w *world) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.env.Server.Pool().Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// addClaim inserts a claim with one evidence row on the first passage of
// an item. object is an entity key or a text.
func (w *world) addClaim(t *testing.T, subject, predicate, object, itemExternal, stance, source string) int64 {
	t.Helper()
	var id int64
	err := w.env.Server.Pool().QueryRow(context.Background(), `
WITH s AS (SELECT id FROM entities WHERE key = $1),
     o AS (SELECT id FROM entities WHERE key = $3),
     c AS (INSERT INTO claims (subject_id, predicate, object_entity_id, object_text, object_norm)
           SELECT s.id, $2, (SELECT id FROM o), CASE WHEN EXISTS (SELECT 1 FROM o) THEN NULL ELSE $3 END,
                  CASE WHEN EXISTS (SELECT 1 FROM o) THEN '' ELSE lower($3) END FROM s
           ON CONFLICT DO NOTHING RETURNING id),
     cid AS (SELECT id FROM c UNION ALL
             SELECT cl.id FROM claims cl, s WHERE cl.subject_id = s.id AND cl.predicate = $2
               AND (cl.object_entity_id = (SELECT id FROM o) OR (cl.object_text = $3)) LIMIT 1),
     p AS (SELECT p.item_id, p.hash, p.connection_id, p.norm FROM passages p JOIN source_items i ON i.id = p.item_id
           WHERE i.external_id = $4 ORDER BY p.ordinal LIMIT 1)
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, stance, quote, source_type)
SELECT (SELECT id FROM cid LIMIT 1), p.connection_id, p.item_id, p.hash, 'passage', $5, left(p.norm, 40), $6 FROM p
RETURNING claim_id`, subject, predicate, object, itemExternal, stance, source).Scan(&id)
	if err != nil {
		t.Fatalf("add claim %s %s %s: %v", subject, predicate, object, err)
	}
	return id
}

func find(cs []claim, pred func(claim) bool) *claim {
	for i := range cs {
		if pred(cs[i]) {
			return &cs[i]
		}
	}
	return nil
}

func TestSCN_ENT_003_MergeAndSplit(t *testing.T) {
	w := start(t)
	team := "backstage:group:default/payments-team"
	ghTeam := "github:" + w.host + ":team:acme/payments-team"
	before := get(t, w.owner, team)
	beforeGH := get(t, w.owner, ghTeam)
	if len(before.Incoming) == 0 || len(beforeGH.Incoming) == 0 {
		t.Fatalf("no owner claims: %d and %d", len(before.Incoming), len(beforeGH.Incoming))
	}
	var m struct {
		ID     int64  `json:"id"`
		Winner string `json:"winner"`
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/merges", map[string]any{"keys": []string{team, ghTeam}}).JSON(t, &m)
	merged := get(t, w.owner, team)
	if merged.Key != m.Winner || len(merged.Incoming) != len(before.Incoming)+len(beforeGH.Incoming) {
		t.Fatalf("merged %s with %d incoming claims", merged.Key, len(merged.Incoming))
	}
	if loser := get(t, w.owner, ghTeam); loser.Key != m.Winner {
		t.Fatalf("the loser key gives %s", loser.Key)
	}
	if !slices.Contains(merged.Aliases, "@acme/payments-team") {
		t.Fatalf("aliases %v", merged.Aliases)
	}
	var list struct {
		Entities []struct {
			Key string `json:"key"`
		} `json:"entities"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/entities?type=team&limit=500", nil).JSON(t, &list)
	n := 0
	for _, e := range list.Entities {
		if e.Key == team || e.Key == ghTeam {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Explore lists %d of the merged teams", n)
	}

	// A member cannot merge.
	w.member.Must(http.StatusForbidden, http.MethodPost, "/api/v1/merges", map[string]any{"keys": []string{team, ghTeam}})

	w.owner.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/merges/%d/split", m.ID), nil)
	a, b := get(t, w.owner, team), get(t, w.owner, ghTeam)
	if a.Key != team || b.Key != ghTeam || len(a.Incoming) != len(before.Incoming) || len(b.Incoming) != len(beforeGH.Incoming) {
		t.Fatalf("after split: %s %d, %s %d", a.Key, len(a.Incoming), b.Key, len(b.Incoming))
	}
	w.owner.Must(http.StatusConflict, http.MethodPost, fmt.Sprintf("/api/v1/merges/%d/split", m.ID), nil)
}

func TestSCN_ENT_005_TraceLimits(t *testing.T) {
	w := start(t)
	hub := "backstage:component:default/payments-api"
	w.owner.Must(http.StatusBadRequest, http.MethodGet, path(hub)+"/trace?depth=4", nil)

	// 250 dependencies with public evidence.
	w.exec(t, `
WITH hub AS (SELECT id FROM entities WHERE key = $1),
     deps AS (INSERT INTO entities (type, key) SELECT 'service', 'backstage:component:default/dep-' || g FROM generate_series(1, 250) g RETURNING id, key),
     src AS (INSERT INTO entity_sources (entity_id, connection_id) SELECT id, $2 FROM deps),
     cl AS (INSERT INTO claims (subject_id, predicate, object_entity_id) SELECT hub.id, 'depends_on', deps.id FROM hub, deps RETURNING id),
     p AS (SELECT p.item_id, p.hash FROM passages p JOIN source_items i ON i.id = p.item_id
           WHERE i.external_id = 'acme/payments-api:catalog-info.yaml' LIMIT 1)
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, quote, source_type)
SELECT cl.id, $2, p.item_id, p.hash, 'passage', 'dependsOn', 'catalog' FROM cl, p`, hub, w.github)
	var tr struct {
		Root  string `json:"root"`
		Edges []struct {
			ClaimID int64  `json:"claimId"`
			From    string `json:"from"`
			To      string `json:"to"`
			Depth   int    `json:"depth"`
		} `json:"edges"`
		Truncated bool `json:"truncated"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, path(hub)+"/trace?depth=3", nil).JSON(t, &tr)
	if len(tr.Edges) != 200 || !tr.Truncated {
		t.Fatalf("%d edges, truncated %v", len(tr.Edges), tr.Truncated)
	}
	for _, e := range tr.Edges {
		if e.ClaimID == 0 || e.From == "" || e.To == "" || e.Depth < 1 {
			t.Fatalf("edge %+v", e)
		}
	}
	w.owner.Must(http.StatusOK, http.MethodGet, path(hub)+"/trace?depth=1&predicates=part_of", nil).JSON(t, &tr)
	if tr.Truncated || len(tr.Edges) != 1 {
		t.Fatalf("part_of trace: %+v", tr)
	}
}

func TestSCN_ENT_006_DisplayNameUsesVisibleAliases(t *testing.T) {
	w := start(t)
	public := "confluence:" + w.host + ":1003"
	hidden := "confluence:" + w.host + ":2003"
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/merges", map[string]any{"keys": []string{hidden, public},
		"canonicalName": "Falcon vendor choice"})
	// The owner sees all merged entities, so the canonical name shows.
	if d := get(t, w.owner, public); d.Name != "Falcon vendor choice" || !slices.Contains(d.Aliases, "ADR-009 Select Falcon Typesetting") {
		t.Fatalf("owner name %q, aliases %v", d.Name, d.Aliases)
	}
	// The member reaches the winner only through the public key, and sees
	// only the public alias.
	r := w.member.Must(http.StatusOK, http.MethodGet, path(public), nil)
	var d detail
	r.JSON(t, &d)
	if d.Name != "ADR-001 Use PostgreSQL for job queues" {
		t.Fatalf("member name %q", d.Name)
	}
	for _, leak := range []string{"Falcon", "ADR-009", fixtures.Canary} {
		if bytes.Contains(r.Body, []byte(leak)) {
			t.Fatalf("the member response contains %q: %s", leak, r.Body)
		}
	}
	var list struct {
		Entities []struct {
			Name string `json:"name"`
		} `json:"entities"`
	}
	lr := w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities?type=decision", nil)
	lr.JSON(t, &list)
	if bytes.Contains(lr.Body, []byte("Falcon")) {
		t.Fatalf("Explore shows a restricted name: %s", lr.Body)
	}
	// The restricted key itself is not found for the member.
	w.member.Must(http.StatusNotFound, http.MethodGet, path(hidden), nil)
}

func TestSCN_VIS_004_ClaimVisibilityAndStatus(t *testing.T) {
	w := start(t)
	subject := "confluence:" + w.host + ":1003"
	a := w.addClaim(t, subject, "rationale", "a vendor deal", "2001", "supports", "confluence")
	b := w.addClaim(t, subject, "rationale", "one datastore is easier", "1003", "supports", "confluence")
	w.addClaim(t, subject, "rationale", "one datastore is easier", "2002", "disputes", "confluence")

	m := get(t, w.member, subject)
	if find(m.Claims, func(c claim) bool { return c.ID == a }) != nil {
		t.Fatal("the member sees a claim with only restricted support")
	}
	mb := find(m.Claims, func(c claim) bool { return c.ID == b })
	if mb == nil || mb.Status != "supported" || len(mb.Evidence) != 1 {
		t.Fatalf("member claim B %+v", mb)
	}
	o := get(t, w.owner, subject)
	ob := find(o.Claims, func(c claim) bool { return c.ID == b })
	if ob == nil || ob.Status != "conflicting" || len(ob.Evidence) != 2 {
		t.Fatalf("owner claim B %+v", ob)
	}
	if find(o.Claims, func(c claim) bool { return c.ID == a }) == nil {
		t.Fatal("the owner does not see claim A")
	}
}

// routes are the read routes of an entity: the entity, its trace, its page,
// the compile of the page and the two Markdown forms.
func routes(key string) [][2]string {
	return [][2]string{{http.MethodGet, path(key)}, {http.MethodGet, path(key) + "/trace"}, {http.MethodGet, path(key) + "/page"},
		{http.MethodPost, path(key) + "/page/compile"}, {http.MethodGet, path(key) + "/page.md"}, {http.MethodGet, "/e/" + url.PathEscape(key) + ".md"}}
}

func TestSCN_VIS_005_RestrictedEntityIsNotFound(t *testing.T) {
	w := start(t)
	hidden := "confluence:" + w.host + ":2003"
	get(t, w.owner, hidden)
	for _, p := range routes(hidden) {
		w.member.Must(http.StatusNotFound, p[0], p[1], nil)
	}
	var list struct {
		Entities []struct {
			Key string `json:"key"`
		} `json:"entities"`
	}
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities?limit=500", nil).JSON(t, &list)
	for _, e := range list.Entities {
		if e.Key == hidden {
			t.Fatal("Explore lists the restricted entity for the member")
		}
	}

	// A term that only the restricted space defines.
	w.src.PutPage(fixtures.Page{ID: "2097", Space: "RST", Title: "Falcon terms", Author: "alex.acme",
		Body: "<p>The Falcon Typesetting Charge (FTC) is private.</p>"})
	if r := w.env.RunNow(w.owner, w.restrict, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	term := "confluence:" + w.host + ":term:falcon-typesetting-charge"
	get(t, w.owner, term)
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/terms/"+url.PathEscape(term), nil)
	w.member.Must(http.StatusNotFound, http.MethodGet, "/api/v1/terms/"+url.PathEscape(term), nil)
	for _, p := range routes(term) {
		w.member.Must(http.StatusNotFound, p[0], p[1], nil)
	}
	terms := string(w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/terms", nil).Body)
	if strings.Contains(terms, "Falcon") || strings.Contains(terms, "FTC") {
		t.Fatalf("the member term list shows the restricted term: %s", terms)
	}
}

func TestSCN_VIS_006_TraceStopsAtRestrictedEdge(t *testing.T) {
	w := start(t)
	from := "confluence:" + w.host + ":1003"
	to := "confluence:" + w.host + ":1004"
	w.addClaim(t, from, "supersedes", to, "2001", "supports", "confluence")
	edges := func(cl *testkit.Client) int {
		var tr struct {
			Edges []struct {
				To string `json:"to"`
			} `json:"edges"`
		}
		cl.Must(http.StatusOK, http.MethodGet, path(from)+"/trace?depth=3&direction=out", nil).JSON(t, &tr)
		n := 0
		for _, e := range tr.Edges {
			if e.To == to {
				n++
			}
		}
		return n
	}
	if edges(w.owner) != 1 {
		t.Fatal("the owner trace has no restricted edge")
	}
	if edges(w.member) != 0 {
		t.Fatal("the member trace crosses an edge with only restricted evidence")
	}
}

func TestSCN_VIS_008_HiddenAndMissingEntityGiveSame404(t *testing.T) {
	w := start(t)
	hidden, missing := routes("confluence:"+w.host+":2003"), routes("confluence:"+w.host+":9999")
	for i := range hidden {
		a := w.member.Do(hidden[i][0], hidden[i][1], nil)
		b := w.member.Do(missing[i][0], missing[i][1], nil)
		if a.Status != http.StatusNotFound || a.Status != b.Status || !bytes.Equal(a.Body, b.Body) {
			t.Fatalf("%s hidden: %d %s\nmissing: %d %s", hidden[i][1], a.Status, a.Body, b.Status, b.Body)
		}
	}
}

func setPriority(t *testing.T, cl *testkit.Client, value map[string]any) {
	t.Helper()
	var cur struct {
		Revision int `json:"revision"`
	}
	var list struct {
		Settings []struct {
			Key      string `json:"key"`
			Revision int    `json:"revision"`
		} `json:"settings"`
	}
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/settings", nil).JSON(t, &list)
	for _, s := range list.Settings {
		if s.Key == "priority" {
			cur.Revision = s.Revision
		}
	}
	cl.Must(http.StatusOK, http.MethodPut, "/api/v1/settings/priority", map[string]any{"value": value, "revision": cur.Revision})
}

func TestSCN_CLM_004_PriorityChangeAppliesOnRead(t *testing.T) {
	w := start(t)
	comp := "backstage:component:default/payments-api"
	w.exec(t, `INSERT INTO entities (type, key) VALUES ('team', 'confluence:wiki:team-hermes')`)
	w.exec(t, `INSERT INTO entity_sources (entity_id, connection_id) SELECT id, $1 FROM entities WHERE key = 'confluence:wiki:team-hermes'`, w.public)
	page := w.addClaim(t, comp, "owned_by", "confluence:wiki:team-hermes", "1001", "supports", "confluence")

	owners := func() (catalog, confluence *claim) {
		d := get(t, w.member, comp)
		return find(d.Claims, func(c claim) bool { return c.Predicate == "owned_by" && c.ID != page }),
			find(d.Claims, func(c claim) bool { return c.ID == page })
	}
	cat, conf := owners()
	if cat == nil || cat.Status != "supported" || !cat.ConflictNoted || conf.Status != "conflicting" {
		t.Fatalf("default priority: catalog %+v, confluence %+v", cat, conf)
	}
	setPriority(t, w.owner, map[string]any{"predicates": map[string]any{"owned_by": []string{"confluence", "others"}}})
	cat, conf = owners()
	if conf.Status != "supported" || !conf.ConflictNoted || cat.Status != "conflicting" {
		t.Fatalf("confluence first: catalog %+v, confluence %+v", cat, conf)
	}
}

func TestSCN_CLM_005_ResolutionSupersedesAndReopens(t *testing.T) {
	w := start(t)
	comp := "backstage:component:default/payments-api"
	w.exec(t, `INSERT INTO entities (type, key) VALUES ('team', 'confluence:wiki:team-hermes')`)
	w.exec(t, `INSERT INTO entity_sources (entity_id, connection_id) SELECT id, $1 FROM entities WHERE key = 'confluence:wiki:team-hermes'`, w.public)
	page := w.addClaim(t, comp, "owned_by", "confluence:wiki:team-hermes", "1001", "supports", "confluence")
	d := get(t, w.owner, comp)
	cat := find(d.Claims, func(c claim) bool { return c.Predicate == "owned_by" && c.ID != page })

	w.member.Must(http.StatusForbidden, http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/resolution", page), nil)
	w.owner.Must(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/resolution", page), nil)
	d = get(t, w.member, comp)
	if c := find(d.Claims, func(c claim) bool { return c.ID == cat.ID }); c.Status != "superseded" {
		t.Fatalf("loser %+v", c)
	}
	if c := find(d.Claims, func(c claim) bool { return c.ID == page }); c.Status != "supported" {
		t.Fatalf("winner %+v", c)
	}

	// A second catalog file names the same owner: new support for the loser.
	time.Sleep(10 * time.Millisecond)
	w.src.PutFile("github", "acme/reminder-service", "catalog-info.yaml", []byte(
		"kind: Component\nmetadata:\n  name: reminder-service\nspec:\n  owner: reminders-team\n---\n"+
			"kind: Component\nmetadata:\n  name: payments-api\nspec:\n  owner: group:payments-team\n"))
	if r := w.env.RunNow(w.owner, w.github, "sync"); r.Status != "succeeded" || r.Updated != 1 {
		t.Fatalf("sync %+v", r)
	}
	if n := w.env.Count("SELECT count(*) FROM resolutions WHERE reopened_at IS NOT NULL"); n != 1 {
		t.Fatalf("%d reopened resolutions", n)
	}
	if n := w.env.Count("SELECT count(*) FROM proposals WHERE kind = 'conflict' AND status = 'open'"); n != 1 {
		t.Fatalf("%d conflict cards", n)
	}
	d = get(t, w.member, comp)
	if c := find(d.Claims, func(c claim) bool { return c.ID == cat.ID }); c.Status == "superseded" {
		t.Fatalf("the loser stays superseded after new support: %+v", c)
	}
}

func TestSCN_CLM_007_TimelineOfStatus(t *testing.T) {
	w := start(t)
	key := "jira:" + w.host + ":PAY-1"
	for _, st := range []string{"Review", "Done"} {
		is, _ := w.src.Issue("PAY-1")
		is.Status = st
		is.Updated = time.Time{}
		time.Sleep(1100 * time.Millisecond)
		w.src.PutIssue(is)
		if r := w.env.RunNow(w.owner, w.jira, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync %+v", r)
		}
	}
	d := get(t, w.member, key)
	var values []string
	current := 0
	for _, c := range d.Timeline {
		if c.Predicate != "status" {
			continue
		}
		values = append(values, c.ObjectText)
		if c.Current {
			current++
			if c.ObjectText != "Done" {
				t.Fatalf("the current value is %s", c.ObjectText)
			}
		}
		if c.ValidFrom == nil {
			t.Fatalf("value %s has no date", c.ObjectText)
		}
	}
	if strings.Join(values, ",") != "Done,Review,In Progress" || current != 1 {
		t.Fatalf("timeline %v with %d current", values, current)
	}
	if c := find(d.Claims, func(c claim) bool { return c.Predicate == "status" }); c == nil || c.ObjectText != "Done" {
		t.Fatalf("current status %+v", c)
	}
}
