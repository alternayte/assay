package round3

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

// quote returns the item ID and the start of the first passage of an item.
func (w *world) quote(t *testing.T, conn int64, external string) (int64, string) {
	t.Helper()
	item := w.scalar(t, `SELECT id FROM source_items WHERE connection_id = $1 AND external_id = $2`, conn, external).(int64)
	q := w.scalar(t, `SELECT left(p.text, 30) FROM passages p WHERE p.item_id = $1 ORDER BY p.ordinal LIMIT 1`, item).(string)
	return item, q
}

// accepted proposes a finding as the owner and accepts it. It returns the
// claim.
func (w *world) accepted(t *testing.T, subject, predicate, text string, conn int64, external string) int64 {
	t.Helper()
	item, q := w.quote(t, conn, external)
	var f struct {
		Proposal int64 `json:"proposal"`
		Claim    int64 `json:"claim"`
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/findings", map[string]any{
		"subject": subject, "predicate": predicate, "object": map[string]any{"text": text},
		"evidence": []map[string]any{{"itemId": item, "quote": q}}}).JSON(t, &f)
	w.owner.Must(http.StatusNoContent, http.MethodPost, fmt.Sprintf("/api/v1/cards/%d/accept", f.Proposal), map[string]any{})
	return f.Claim
}

type card struct {
	ID      int64          `json:"id"`
	Kind    string         `json:"kind"`
	Payload map[string]any `json:"payload"`
	Claims  []struct {
		ID int64 `json:"id"`
	} `json:"claims"`
}

func cards(t *testing.T, cl *testkit.Client, kind string) []card {
	t.Helper()
	var out struct {
		Cards []card `json:"cards"`
	}
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/cards?kind="+kind, nil).JSON(t, &out)
	return out.Cards
}

// statuses returns the status of each claim of an entity for a reader.
func statuses(t *testing.T, cl *testkit.Client, key string) map[int64]string {
	t.Helper()
	var d struct {
		Claims []struct {
			ID     int64  `json:"id"`
			Status string `json:"status"`
		} `json:"claims"`
	}
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(key), nil).JSON(t, &d)
	out := map[int64]string{}
	for _, c := range d.Claims {
		out[c.ID] = c.Status
	}
	return out
}

// Two accepted findings give the decision two different decided values: a
// conflict. Resolution must open a conflict card for it, and a reviewer
// must accept the card by picking the winner. A needs_edit page card must
// accept a corrected summary. Before, only a reopened resolution opened a
// conflict card, and accept of a conflict or needs_edit card gave 422
// (REQ-REV-002, REQ-CLM-006).
func TestRVW_3_03_ConflictAndNeedsEditCardsCannotBeAccepted(t *testing.T) {
	w := start(t)
	d := w.key(t, w.public, "1003")
	a := w.accepted(t, d, "decided", "Keep the hosted queue.", w.public, "1003")
	b := w.accepted(t, d, "decided", "Move to the self-hosted queue.", w.public, "1004")
	if err := w.env.Server.Resolve().Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var conflict *card
	for _, c := range cards(t, w.reviewer, "conflict") {
		ids := []int64{}
		for _, cl := range c.Claims {
			ids = append(ids, cl.ID)
		}
		if slices.Contains(ids, a) && slices.Contains(ids, b) {
			conflict = &c
		}
	}
	if conflict == nil {
		t.Fatalf("no conflict card for claims %d and %d", a, b)
	}
	accept := fmt.Sprintf("/api/v1/cards/%d/accept", conflict.ID)
	w.reviewer.Must(http.StatusUnprocessableEntity, http.MethodPost, accept, map[string]any{})
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, accept, map[string]any{"winner": a})
	if st := statuses(t, w.member, d); st[a] != "supported" || st[b] != "superseded" {
		t.Errorf("after the reviewer picks %d: statuses %v", a, st)
	}

	// A page that fails the lint twice gets a needs_edit card.
	repos := w.repos(t)
	testkit.ShareWithEveryone(t, w.env, w.owner, repos)
	fake, openai, _ := testkit.FakeModels(t)
	endpoint := testkit.CreateEndpoint(t, w.owner, "fake", "openai", openai, fixtures.FakeKey, nil)
	testkit.SetFunction(t, w.owner, "compile", testkit.Function{Endpoint: endpoint, Model: "fake-chat"})
	fake.OnChat(func(map[string]any) string { return "The team will leverage the owner [C1]. The system is robust [C2]." })
	pay := w.scalar(t, `SELECT e.key FROM entities e JOIN entity_sources s ON s.entity_id = e.id
		WHERE s.connection_id = $1 AND e.type = 'service' AND e.key LIKE '%payments-api' LIMIT 1`, repos).(string)
	w.reviewer.Must(http.StatusOK, http.MethodPost, "/api/v1/entities/"+url.PathEscape(pay)+"/page/compile", nil)
	edits := cards(t, w.reviewer, "needs_edit")
	if len(edits) != 1 {
		t.Fatalf("%d needs_edit cards", len(edits))
	}
	accept = fmt.Sprintf("/api/v1/cards/%d/accept", edits[0].ID)
	w.reviewer.Must(http.StatusUnprocessableEntity, http.MethodPost, accept, map[string]any{})
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, accept, map[string]any{"summary": "Payments API has an owner [C1]."})
	var p struct {
		Summary struct {
			State    string `json:"state"`
			Markdown string `json:"markdown"`
		} `json:"summary"`
	}
	w.reviewer.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(pay)+"/page", nil).JSON(t, &p)
	if p.Summary.State != "fresh" || p.Summary.Markdown != "Payments API has an owner [C1]." {
		t.Errorf("after accept of the corrected summary the page is %s: %q", p.Summary.State, p.Summary.Markdown)
	}
}

// A reviewer chose claim a over claim b. Then a passage of acme-restricted
// supports b. The resolution must stay for the member, who does not see
// that support: b stays Superseded for the member (REQ-CLM-006, SI-02).
func TestRVW_3_05_HiddenSupportReopensResolutionForEveryone(t *testing.T) {
	w := start(t)
	d := w.key(t, w.public, "1003")
	a := w.accepted(t, d, "decided", "Keep the hosted queue.", w.public, "1003")
	b := w.accepted(t, d, "decided", "Move to the self-hosted queue.", w.public, "1004")
	w.reviewer.Must(http.StatusCreated, http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/resolution", a), nil)
	if st := statuses(t, w.member, d); st[b] != "superseded" {
		t.Fatalf("after the resolution: %v", st)
	}

	item := w.scalar(t, `SELECT id FROM source_items WHERE connection_id = $1 AND external_id = '2003'`, w.restricted).(int64)
	var hash []byte
	var norm string
	if err := w.env.Server.Pool().QueryRow(context.Background(), `SELECT hash, norm FROM passages WHERE item_id = $1 ORDER BY ordinal LIMIT 1`,
		item).Scan(&hash, &norm); err != nil {
		t.Fatal(err)
	}
	subject := w.scalar(t, `SELECT id FROM entities WHERE key = $1`, d).(int64)
	err := pgx.BeginFunc(context.Background(), w.env.Server.Pool(), func(tx pgx.Tx) error {
		return content.AddExtracted(context.Background(), tx, w.restricted, item, hash, time.Now(), []content.Extracted{{Subject: subject,
			Predicate: "decided", Text: "Move to the self-hosted queue.", Stance: "supports", Quote: norm[:20]}},
			slog.New(slog.NewTextHandler(io.Discard, nil)))
	})
	if err != nil {
		t.Fatal(err)
	}
	if st := statuses(t, w.member, d); st[a] != "supported" || st[b] != "superseded" {
		t.Errorf("support that the member cannot see changed the statuses for the member: %v", st)
	}
	if len(cards(t, w.owner, "conflict")) == 0 {
		t.Errorf("the owner, who sees the new support, gets no conflict card")
	}
	if n := len(cards(t, w.reviewer, "conflict")); n != 0 {
		t.Errorf("the reviewer gets %d conflict cards with restricted evidence", n)
	}
}

// pair stores a High same_entity decision for two keys with a merge card.
func (w *world) pair(t *testing.T, a, b string) {
	t.Helper()
	w.exec(t, `WITH d AS (INSERT INTO decisions (kind, subject_refs, input_hash, raw_p, band, connections)
		VALUES ('same_entity', ARRAY[$1, $2]::text[], sha256(($1 || $2)::bytea), 0.99, 'high', ARRAY[$3]::bigint[]) RETURNING id)
		INSERT INTO proposals (kind, target, payload, via, connections, decision_id)
		SELECT 'merge', 'pair:' || $1 || ':' || $2, '{}', 'system', ARRAY[$3]::bigint[], d.id FROM d`, a, b, w.public)
}

// A merge card names two entities that a reviewer merged already. Accept
// must not fail with 500, and accept of the band must go on with the next
// cards (REQ-REV-009, REQ-REV-011).
func TestRVW_3_08_MergeCardOfMergedEntitiesFails(t *testing.T) {
	w := start(t)
	pick := func(n int) string {
		return w.scalar(t, `SELECT e.key FROM entities e JOIN entity_sources s ON s.entity_id = e.id
			WHERE s.connection_id = $1 AND e.merged_into IS NULL ORDER BY e.id OFFSET $2 LIMIT 1`, w.public, n).(string)
	}
	a, b, c, d := pick(0), pick(1), pick(2), pick(3)
	w.pair(t, a, b)
	w.pair(t, c, d)
	w.reviewer.Must(http.StatusCreated, http.MethodPost, "/api/v1/merges", map[string]any{"keys": []string{a, b}})

	var out struct {
		Accepted []int64 `json:"accepted"`
	}
	r := w.reviewer.Do(http.MethodPost, "/api/v1/cards/accept-band", map[string]any{"band": "high", "kind": "merge"})
	if r.Status != http.StatusOK {
		t.Fatalf("accept-band: %d %s", r.Status, r.Body)
	}
	r.JSON(t, &out)
	if len(out.Accepted) != 2 {
		t.Errorf("accept-band accepted %v, want both cards", out.Accepted)
	}
	if into := w.scalar(t, `SELECT count(*) FROM entities WHERE key = ANY($1) AND merged_into IS NOT NULL`, []string{c, d}); into != int64(1) {
		t.Errorf("the second card did not merge its entities: %v merged", into)
	}
}

// Purge removes acme-restricted. A claim about a restricted entity that
// rests on a public quote must stay, and a merge with one restricted loser
// and one public loser must still split (REQ-SYN-009, REQ-ENT-004).
func TestRVW_3_15_PurgeDeletesClaimsAndBlocksSplit(t *testing.T) {
	w := start(t)
	hidden := w.key(t, w.restricted, "2003")
	claim := w.accepted(t, hidden, "decided", "Keep the hosted queue.", w.public, "1003")
	at := time.Date(2026, 3, 2, 9, 0, 0, 0, time.UTC)
	w.src.PutPage(fixtures.Page{ID: "2004", Space: "RST", Title: "ADR-010 Keep the print queue", Labels: []string{"adr"}, Author: "alex.acme",
		Created: at, Updated: at, Version: 1, Body: "<h2>Decision</h2><p>Acme keeps the print queue.</p>"})
	if r := w.env.RunNow(w.owner, w.restricted, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	public, loser := w.key(t, w.public, "1004"), w.key(t, w.restricted, "2004")
	w.older(t, public, 3)
	var m struct {
		ID int64 `json:"id"`
	}
	other := w.scalar(t, `SELECT e.key FROM entities e JOIN entity_sources s ON s.entity_id = e.id
		WHERE s.connection_id = $1 AND e.key <> $2 AND e.type = 'decision' ORDER BY e.id LIMIT 1`, w.public, public).(string)
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/merges", map[string]any{"keys": []string{public, other, loser}}).JSON(t, &m)

	w.owner.Must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("/api/v1/connections/%d", w.restricted), nil)
	if n := w.env.Count(`SELECT count(*) FROM claims WHERE id = $1`, claim); n != 1 {
		t.Errorf("purge deleted claim %d, which rests on a public quote", claim)
	}
	if r := w.owner.Do(http.MethodPost, fmt.Sprintf("/api/v1/merges/%d/split", m.ID), nil); r.Status >= 300 {
		t.Errorf("split of a merge with a purged loser: %d %s", r.Status, r.Body)
	}
	if n := w.env.Count(`SELECT count(*) FROM entities WHERE key = $1 AND merged_into IS NULL`, other); n != 1 {
		t.Errorf("after the split, %s is still merged", other)
	}
}

// The owner accepts a finding about an entity that only the owner sees,
// with a public quote. The reviewer does not see the subject, so the
// reviewer must not resolve the claim (SI-02).
func TestRVW_3_16_ReviewerResolvesClaimOfHiddenSubject(t *testing.T) {
	w := start(t)
	hidden := w.key(t, w.restricted, "2003")
	claim := w.accepted(t, hidden, "decided", "Keep the hosted queue.", w.public, "1003")
	if r := w.reviewer.Do(http.MethodPost, fmt.Sprintf("/api/v1/claims/%d/resolution", claim), nil); r.Status != http.StatusNotFound {
		t.Errorf("reviewer resolves a claim of a hidden subject: %d %s", r.Status, r.Body)
	}
	if strings.Contains(string(w.reviewer.Must(http.StatusOK, http.MethodGet, "/api/v1/cards?kind=conflict", nil).Body), hidden) {
		t.Errorf("reviewer reads a conflict card about a hidden subject")
	}
}
