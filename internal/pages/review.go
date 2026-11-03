package pages

import (
	"context"
	"encoding/json"
	"errors"
	"strings"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/resolve"
	"github.com/alternayte/assay/internal/settings"
	"github.com/alternayte/assay/internal/visibility"
)

// ApplyEdit accepts a needs_edit card with the summary that the reviewer
// corrected (REQ-REV-002, REQ-PAG-004). The summary must pass the same
// checks as a compiled draft: each sentence cites claims of the page with
// the markers of the draft, and the lint passes. The page then shows the
// summary as fresh for its visibility key.
func (s *Service) ApplyEdit(ctx context.Context, tx pgx.Tx, vis visibility.Set, payload []byte, _, _ string, edits resolve.Edits) error {
	text := strings.TrimSpace(edits.Summary)
	if text == "" {
		return &resolve.InvalidEdit{Code: "summary", Message: "Enter the corrected summary."}
	}
	var card struct {
		EntityKey     string `json:"entityKey"`
		VisibilityKey string `json:"visibilityKey"`
	}
	if err := json.Unmarshal(payload, &card); err != nil {
		return err
	}
	q := New(tx)
	row, err := q.PageForEdit(ctx, PageForEditParams{Key: card.EntityKey, Vkey: card.VisibilityKey, Visible: vis})
	if errors.Is(err, pgx.ErrNoRows) {
		return &resolve.InvalidEdit{Code: "page_changed", Message: "The page has no draft to correct. Compile the page again."}
	}
	if err != nil {
		return err
	}
	conns := visibility.Set(row.Connections)
	p, err := s.build(ctx, conns, card.EntityKey)
	if err != nil {
		return err
	}
	var markers map[string]int64
	_ = json.Unmarshal(row.Citations, &markers)
	input := map[int64]bool{}
	ids := make([]int64, 0, len(p.input))
	for _, c := range p.input {
		input[c.ID] = true
		ids = append(ids, c.ID)
	}
	known := map[string]bool{}
	for m, id := range markers {
		if input[id] {
			known[m] = true
		}
	}
	rules, err := settings.Read[PageRules](ctx, tx, Setting)
	if err != nil {
		return err
	}
	terms, err := s.Vocab.Terms(ctx, conns)
	if err != nil {
		return err
	}
	summary, failures := check(text, known, rules, terms)
	if len(failures) > 0 {
		return &resolve.InvalidEdit{Code: "summary", Message: "The summary fails these checks: " + strings.Join(messagesOf(failures), " ")}
	}
	return q.SaveEditedSummary(ctx, SaveEditedSummaryParams{ID: row.ID, SummaryMd: summary, ClaimSetHash: p.hash, ClaimIds: ids,
		Model: "reviewer", CompiledAt: s.now()})
}

// Invalidate drops the cached pages of an entity in tx after an edit, so
// the next open compiles the page again.
func (s *Service) Invalidate(ctx context.Context, tx pgx.Tx, entityID int64) error {
	return New(tx).InvalidatePages(ctx, entityID)
}
