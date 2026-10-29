package resolve_test

import (
	"context"
	"fmt"
	"net/http"
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
	src              *fixtures.Sources
}

// start syncs Jira, GitHub and Azure DevOps, shared with Everyone, and the
// restricted space of the owner.
func start(t *testing.T, opts ...testkit.Option) *world {
	t.Helper()
	env := testkit.Start(t, opts...)
	w := &world{env: env, owner: env.Setup("owner@acme.test")}
	w.reviewer, _ = env.Invite(w.owner, "reviewer@acme.test", "reviewer", "reviewer-password-1")
	w.src, w.base = testkit.FakeSources(t)
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

type subject struct {
	Key  string `json:"key"`
	Name string `json:"name"`
	Type string `json:"type"`
}

type card struct {
	ID         int64     `json:"id"`
	Kind       string    `json:"kind"`
	Band       string    `json:"band"`
	P          *float64  `json:"p"`
	Calibrated bool      `json:"calibrated"`
	Reasons    []string  `json:"reasons"`
	Subjects   []subject `json:"subjects"`
	DecisionID int64     `json:"decisionId"`
}

func cards(t *testing.T, cl *testkit.Client) []card {
	t.Helper()
	var out struct {
		Cards []card `json:"cards"`
	}
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/cards", nil).JSON(t, &out)
	return out.Cards
}

// cardFor returns the open merge card of two keys, or nil.
func cardFor(cs []card, a, b string) *card {
	for i, c := range cs {
		var keys []string
		for _, s := range c.Subjects {
			keys = append(keys, s.Key)
		}
		if c.Kind == "merge" && slices.Contains(keys, a) && slices.Contains(keys, b) {
			return &cs[i]
		}
	}
	return nil
}

// decideOn turns on the fake decide function. Labelled duplicates get
// 0.97, other pairs 0.1.
func (w *world) decideOn(t *testing.T) *fixtures.Models {
	t.Helper()
	fake, _, decisions := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, w.owner, "decide", "decisions", decisions, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "decide", testkit.Function{Endpoint: ep, Model: "fake-decide"})
	if _, err := w.env.Server.Resolve().Profiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	same := map[string]bool{}
	name := func(key string) string {
		var text string
		if err := w.env.Server.Pool().QueryRow(context.Background(), `SELECT p.text FROM entity_profiles p JOIN entities e ON e.id = p.entity_id
WHERE e.key = $1`, key).Scan(&text); err != nil {
			t.Fatalf("profile %s: %v", key, err)
		}
		return strings.SplitN(text, "\n", 2)[0]
	}
	for _, p := range w.labels.Duplicates {
		same[name(p.A)+"|"+name(p.B)] = true
		same[name(p.B)+"|"+name(p.A)] = true
	}
	fake.OnDecide(func(_ string, _ map[string]any, state map[string]any) map[string]any {
		a, _ := state["a"].(string)
		b, _ := state["b"].(string)
		if a == "" || b == "" {
			return nil
		}
		p := 0.1
		if same[strings.SplitN(a, "\n", 2)[0]+"|"+strings.SplitN(b, "\n", 2)[0]] {
			p = 0.97
		}
		return map[string]any{"type": "noul", "noul": p}
	})
	return fake
}

// restrictedTwin adds a service named Payments API whose only source is the
// restricted connection.
func (w *world) restrictedTwin(t *testing.T) string {
	t.Helper()
	key := "confluence:" + strings.ToLower(w.host) + ":service:payments-api"
	w.exec(t, `
WITH e AS (INSERT INTO entities (type, key) VALUES ('service', $1) RETURNING id),
     s AS (INSERT INTO entity_sources (entity_id, connection_id) SELECT id, $2 FROM e)
INSERT INTO entity_aliases (entity_id, connection_id, alias, alias_norm, is_name) SELECT id, $2, 'Payments API', 'payments api', true FROM e`,
		key, w.restricted)
	return key
}

func TestSCN_RES_002_DecisionsAreStoredAndCardsNeedVisibleInput(t *testing.T) {
	w := start(t)
	ctx := context.Background()
	twin := w.restrictedTwin(t)
	w.decideOn(t)
	if err := w.env.Server.Resolve().Run(ctx); err != nil {
		t.Fatal(err)
	}
	rows, err := w.env.Server.Pool().Query(ctx, `
SELECT d.kind, d.probs::text, d.band, d.reasons::text, d.connections, d.model,
       ARRAY(SELECT DISTINCT s.connection_id FROM entity_sources s JOIN entities e ON e.id = s.entity_id
             WHERE e.key = ANY(d.subject_refs) ORDER BY 1)
FROM decisions d`)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for rows.Next() {
		var kind, probs, reasons, model string
		var band *string
		var conns, want []int64
		if err := rows.Scan(&kind, &probs, &band, &reasons, &conns, &model, &want); err != nil {
			t.Fatal(err)
		}
		n++
		slices.Sort(conns)
		if kind != "same_entity" || !strings.Contains(probs, `"yes"`) || band == nil || reasons == "[]" || model == "" ||
			!slices.Equal(conns, want) {
			t.Errorf("decision %s probs %s band %v reasons %s connections %v (sources %v) model %q", kind, probs, band, reasons, conns, want, model)
		}
	}
	if n == 0 {
		t.Fatal("no decisions")
	}

	pay := "backstage:component:default/payments-api"
	if c := cardFor(cards(t, w.reviewer), pay, twin); c != nil {
		t.Errorf("the reviewer sees a card with restricted input: %+v", c)
	}
	for _, c := range cards(t, w.reviewer) {
		for _, s := range c.Subjects {
			if s.Key == twin {
				t.Errorf("the reviewer sees the restricted entity in card %d", c.ID)
			}
		}
	}
	if cardFor(cards(t, w.owner), pay, twin) == nil {
		t.Error("the owner does not see the card of the restricted pair")
	}
}

func TestSCN_ENT_002_SameNameGivesCardAndNoMerge(t *testing.T) {
	w := start(t)
	w.decideOn(t)
	if err := w.env.Server.Resolve().Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	a, b := "backstage:group:default/payments-team", "github:"+strings.ToLower(w.host)+":team:acme/payments-team"
	c := cardFor(cards(t, w.reviewer), a, b)
	if c == nil {
		t.Fatal("no merge card for the two payments-team entities")
	}
	if c.Band != "high" || c.Calibrated {
		t.Errorf("card band %q calibrated %v, want high and not calibrated", c.Band, c.Calibrated)
	}
	if n := w.env.Count("SELECT count(*) FROM merges"); n != 0 {
		t.Errorf("%d merges with auto-apply off", n)
	}
}

func TestSCN_RES_005_NotTheSameStoresVerdict(t *testing.T) {
	w := start(t)
	ctx := context.Background()
	w.decideOn(t)
	r := w.env.Server.Resolve()
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	a, b := "backstage:component:default/peer-review-service", "backstage:component:default/reminder-service"
	c := cardFor(cards(t, w.reviewer), a, b)
	if c == nil {
		t.Fatal("no card for the peer review service and the reminder service")
	}
	if code := w.reviewer.Do(http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/reject", c.ID), map[string]any{}).Code(); code != "invalid" {
		t.Errorf("a reject without a reason gives %q", code)
	}
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/reject", c.ID), map[string]any{"reason": "Two different teams."})
	if n := w.env.Count(`SELECT count(*) FROM pair_verdicts WHERE verdict = 'different' AND kind = 'same_entity'
AND a_id = least($1::bigint, $2::bigint) AND b_id = greatest($1::bigint, $2::bigint)`, w.id(t, a), w.id(t, b)); n != 1 {
		t.Errorf("%d verdicts, want 1", n)
	}
	var cal struct {
		Kinds []struct {
			Kind     string `json:"kind"`
			Reviewed int    `json:"reviewed"`
		} `json:"kinds"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/calibration", nil).JSON(t, &cal)
	reviewed := -1
	for _, k := range cal.Kinds {
		if k.Kind == "same_entity" {
			reviewed = k.Reviewed
		}
	}
	if reviewed != 1 {
		t.Errorf("calibration counts %d reviewed outcomes, want 1", reviewed)
	}

	// Accept with a canonical name merges the pair.
	x, y := "backstage:group:default/reminders-team", "github:"+strings.ToLower(w.host)+":team:acme/reminders-team"
	acc := cardFor(cards(t, w.reviewer), x, y)
	if acc == nil {
		t.Fatal("no card for the reminders teams")
	}
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", acc.ID), map[string]any{"canonicalName": "Reminders"})
	if n := w.env.Count(`SELECT count(*) FROM merges WHERE canonical_name = 'Reminders' AND NOT auto`); n != 1 {
		t.Errorf("%d merges after accept", n)
	}

	before := w.env.Count("SELECT count(*) FROM proposals WHERE kind = 'merge'")
	if err := r.Run(ctx); err != nil {
		t.Fatal(err)
	}
	if cardFor(cards(t, w.reviewer), a, b) != nil {
		t.Error("a new run made a card for a pair with a verdict")
	}
	if after := w.env.Count("SELECT count(*) FROM proposals WHERE kind = 'merge'"); after != before {
		t.Errorf("a second run added %d cards", after-before)
	}
}

// seedOutcomes adds reviewed same_entity outcomes at raw probability p.
func (w *world) seedOutcomes(t *testing.T, n, accepted int, p float64) {
	t.Helper()
	w.exec(t, `
INSERT INTO decisions (kind, subject_refs, input_hash, probs, raw_p, band, connections, outcome, reviewed_at, at)
SELECT 'same_entity', ARRAY['seed:' || g], sha256(g::text::bytea), '{}', $3, 'high', '{}',
       CASE WHEN g <= $2 THEN 'accepted' ELSE 'rejected' END, now() - (g || ' seconds')::interval, now()
FROM generate_series(1, $1) g`, n, accepted, p)
	if err := w.env.Server.Resolve().Recalibrate(context.Background(), "same_entity"); err != nil {
		t.Fatal(err)
	}
}

func TestSCN_RES_004_AutoApply(t *testing.T) {
	w := start(t)
	ctx := context.Background()
	w.decideOn(t)
	enable := func(on bool) *testkit.Response {
		return w.owner.Do(http.MethodPut, "/api/v1/calibration/same_entity", map[string]any{"autoApply": on})
	}
	w.seedOutcomes(t, 40, 40, 0.97)
	if r := enable(true); r.Status != http.StatusConflict {
		t.Fatalf("enable with 40 outcomes gives %d", r.Status)
	}
	w.exec(t, "DELETE FROM decisions")
	w.seedOutcomes(t, 60, 58, 0.97)
	if r := enable(true); r.Status != http.StatusOK {
		t.Fatalf("enable with 60 outcomes at 0.97 gives %d: %s", r.Status, r.Body)
	}

	if err := w.env.Server.Resolve().Run(ctx); err != nil {
		t.Fatal(err)
	}
	merges := w.env.Count("SELECT count(*) FROM merges WHERE auto AND undone_at IS NULL")
	if merges == 0 {
		t.Fatal("no auto-applied merge")
	}
	if n := w.env.Count(`SELECT count(*) FROM audit_log WHERE action = 'entities.merge' AND (details->>'auto')::boolean`); n != merges {
		t.Errorf("%d audit rows for %d auto merges", n, merges)
	}
	var auto struct {
		Decisions []struct {
			ID       int64     `json:"id"`
			Outcome  string    `json:"outcome"`
			Subjects []subject `json:"subjects"`
		} `json:"decisions"`
	}
	w.reviewer.Must(http.StatusOK, http.MethodGet, "/api/v1/decisions?outcome=auto", nil).JSON(t, &auto)
	if len(auto.Decisions) != merges {
		t.Fatalf("the Auto-applied list has %d decisions for %d merges", len(auto.Decisions), merges)
	}
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/decisions/%d/undo", auto.Decisions[0].ID), nil)
	if n := w.env.Count("SELECT count(*) FROM merges WHERE auto AND undone_at IS NULL"); n != merges-1 {
		t.Errorf("%d active auto merges after undo, want %d", n, merges-1)
	}
	if n := w.env.Count("SELECT count(*) FROM decisions WHERE id = $1 AND outcome = 'undone'", auto.Decisions[0].ID); n != 1 {
		t.Error("the undone decision does not have the outcome undone")
	}

	// Rejected High outcomes push precision below 0.95: auto-apply turns off.
	w.seedOutcomes(t, 10, 0, 0.97)
	if n := w.env.Count("SELECT count(*) FROM calibration WHERE kind = 'same_entity' AND auto_apply"); n != 0 {
		t.Error("auto-apply stays on below 0.95 precision")
	}
	if n := w.env.Count("SELECT count(*) FROM audit_log WHERE action = 'resolve.auto_apply' AND details->>'enabled' = 'false'"); n != 1 {
		t.Errorf("%d audit rows for the automatic stop", n)
	}
}
