package resolve_test

import (
	"context"
	"net/url"
	"slices"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/models"
	"github.com/alternayte/assay/internal/resolve"
	"github.com/alternayte/assay/internal/testkit"
)

type world struct {
	env              *testkit.Env
	owner, reviewer  *testkit.Client
	base, host       string
	jira, github     int64
	azdo, restricted int64
	labels           fixtures.Labels
}

// start syncs Jira, GitHub and Azure DevOps, shared with Everyone, and the
// restricted space of the owner.
func start(t *testing.T, opts ...testkit.Option) *world {
	t.Helper()
	env := testkit.Start(t, opts...)
	w := &world{env: env, owner: env.Setup("owner@acme.test")}
	w.reviewer, _ = env.Invite(w.owner, "reviewer@acme.test", "reviewer", "reviewer-password-1")
	_, w.base = testkit.FakeSources(t)
	u, _ := url.Parse(w.base)
	w.host = u.Host
	w.labels = fixtures.CorpusLabels(u.Host)
	mk := func(name, kind, path string, scope map[string]any, share bool) int64 {
		id := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: name, Kind: kind, BaseURL: w.base, Path: path, Scope: scope})
		if share {
			testkit.ShareWithEveryone(t, env, w.owner, id)
		}
		if r := env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync %s: %+v", name, r)
		}
		return id
	}
	w.jira = mk("acme-jira", "jira", "", map[string]any{"projects": []string{"PAY", "REM"}}, true)
	w.github = mk("acme-github", "github", "", map[string]any{"repositories": []string{"acme/payments-api", "acme/reminder-service"}}, true)
	w.azdo = mk("acme-azdo", "azdo", "/azdo/"+fixtures.AzdoCollection, map[string]any{"repositories": []string{"Platform/ledger-service"}}, true)
	w.restricted = mk("acme-restricted", "confluence", "/confluence", map[string]any{"spaces": []string{"RST"}}, false)
	return w
}

func (w *world) id(t *testing.T, key string) int64 {
	t.Helper()
	var id int64
	if err := w.env.Server.Pool().QueryRow(context.Background(), "SELECT id FROM entities WHERE key = $1", key).Scan(&id); err != nil {
		t.Fatalf("entity %s: %v", key, err)
	}
	return id
}

func (w *world) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.env.Server.Pool().Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

func TestSCN_RES_001_CandidatesFindLabelledDuplicates(t *testing.T) {
	w := start(t)
	ctx := context.Background()
	r := w.env.Server.Resolve()

	// Evidence of a restricted page on the Payments API must not reach its
	// profile: a profile uses only the entity's own sources.
	w.exec(t, `
INSERT INTO claims (subject_id, predicate, object_text, object_norm)
SELECT e.id, 'stated', 'test card numbers leaked', 'test card numbers leaked' FROM entities e WHERE e.key = 'backstage:component:default/payments-api'`)
	w.exec(t, `
INSERT INTO claim_evidence (claim_id, connection_id, item_id, passage_hash, kind, quote, source_type)
SELECT c.id, p.connection_id, p.item_id, p.hash, 'passage', left(p.norm, 40), 'confluence'
FROM claims c, passages p WHERE c.object_text = 'test card numbers leaked' AND p.connection_id = $1 LIMIT 1`, w.restricted)

	if _, err := r.Profiles(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := w.env.Server.Pool().Query(ctx, `
SELECT p.entity_id, p.text, p.connections,
       ARRAY(SELECT s.connection_id FROM entity_sources s WHERE s.entity_id = p.entity_id ORDER BY 1)
FROM entity_profiles p`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var id int64
		var text string
		var conns, own []int64
		if err := rows.Scan(&id, &text, &conns, &own); err != nil {
			t.Fatal(err)
		}
		n++
		slices.Sort(conns)
		if !slices.Equal(conns, own) {
			t.Errorf("profile %d has connections %v, its sources are %v", id, conns, own)
		}
		if tok := models.EstimateTokens(text); tok > resolve.MaxProfileTokens {
			t.Errorf("profile %d has %d tokens", id, tok)
		}
		if strings.Contains(text, "leaked") || strings.Contains(text, fixtures.Canary) {
			t.Errorf("profile %d holds restricted text:\n%s", id, text)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if n == 0 {
		t.Fatal("no profiles")
	}
	var payments string
	if err := w.env.Server.Pool().QueryRow(ctx, `SELECT text FROM entity_profiles WHERE entity_id = $1`,
		w.id(t, "backstage:component:default/payments-api")).Scan(&payments); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Name: Payments API", "Type: service", "owned_by: payments-team"} {
		if !strings.Contains(payments, want) {
			t.Errorf("the Payments API profile has no %q:\n%s", want, payments)
		}
	}

	cands, err := r.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for subject, cs := range cands {
		if len(cs) > resolve.MaxCandidates {
			t.Errorf("subject %d has %d candidates", subject, len(cs))
		}
	}
	has := func(a, b int64) bool {
		for _, c := range cands[a] {
			if c.ID == b {
				return true
			}
		}
		for _, c := range cands[b] {
			if c.ID == a {
				return true
			}
		}
		return false
	}
	found := 0
	for _, p := range w.labels.Duplicates {
		if has(w.id(t, p.A), w.id(t, p.B)) {
			found++
		} else {
			t.Logf("no candidate for %s and %s", p.A, p.B)
		}
	}
	if recall := float64(found) / float64(len(w.labels.Duplicates)); recall < 0.9 {
		t.Errorf("candidate recall %.2f, want 0.9 or more", recall)
	}
	// The acronym pair has no shared trigrams: only the initials find it.
	prs, peer := w.id(t, "backstage:component:default/prs"), w.id(t, "backstage:component:default/peer-review-service")
	if !has(prs, peer) {
		t.Fatal("the acronym pair is not a candidate")
	}

	// With an embedding space, profiles get vectors and the cosine signal
	// adds candidates. A changed profile loses its vector.
	_, openai, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, w.owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "embed", testkit.Function{Endpoint: ep, Model: "fake-embed-64"})
	if err := w.env.Server.Embedder().Run(ctx); err != nil {
		t.Fatal(err)
	}
	vectors := func() int {
		var tbl string
		if err := w.env.Server.Pool().QueryRow(ctx, "SELECT 'emb_' || id FROM embedding_spaces WHERE status = 'active'").Scan(&tbl); err != nil {
			t.Fatal(err)
		}
		return w.env.Count("SELECT count(*) FROM " + tbl + " WHERE ref_kind = 'profile'")
	}
	if got, want := vectors(), w.env.Count("SELECT count(*) FROM entity_profiles"); got != want {
		t.Fatalf("%d profile vectors for %d profiles", got, want)
	}
	cands, err = r.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	cosine := 0
	for _, cs := range cands {
		for _, c := range cs {
			for _, reason := range c.Reasons {
				if strings.HasPrefix(reason, "profile similarity") {
					cosine++
				}
			}
		}
	}
	if cosine == 0 {
		t.Error("no candidate has the profile signal")
	}
	w.exec(t, `INSERT INTO entity_aliases (entity_id, connection_id, alias, alias_norm)
SELECT e.id, s.connection_id, 'Card gateway', 'card gateway' FROM entities e JOIN entity_sources s ON s.entity_id = e.id
WHERE e.key = 'backstage:component:default/payments-api' LIMIT 1`)
	if changed, err := r.Profiles(ctx); err != nil || changed != 1 {
		t.Fatalf("Profiles changed %d, %v; want 1", changed, err)
	}
	if got, want := vectors(), w.env.Count("SELECT count(*) FROM entity_profiles")-1; got != want {
		t.Fatalf("%d profile vectors after a change, want %d", got, want)
	}

	// A verdict excludes the pair from the next run.
	a, b := min(prs, peer), max(prs, peer)
	w.exec(t, `INSERT INTO pair_verdicts (a_id, b_id, kind, verdict) VALUES ($1, $2, 'same_entity', 'different')`, a, b)
	cands, err = r.Candidates(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if has(prs, peer) {
		t.Error("a pair with a verdict is still a candidate")
	}
}
