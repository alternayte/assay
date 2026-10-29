package extract

import (
	"context"
	"encoding/hex"
	"fmt"
	"slices"

	"github.com/alternayte/assay/internal/models"
)

// Queue queues one job for the passages that have no cached result. It
// returns false when all are cached.
func (s *Service) Queue(ctx context.Context, vis []int64, trigger string, items []int64, hashes []string) (bool, error) {
	raw := make([][]byte, len(hashes))
	for i, h := range hashes {
		b, err := hex.DecodeString(h)
		if err != nil {
			return false, fmt.Errorf("extract: bad passage hash %q", h)
		}
		raw[i] = b
	}
	cached, err := New(s.Pool).Cached(ctx, CachedParams{Items: items, Hashes: raw, Version: Version()})
	if err != nil {
		return false, err
	}
	done := map[string]bool{}
	for _, c := range cached {
		done[fmt.Sprintf("%d:%x", c.ItemID, c.PassageHash)] = true
	}
	a := Args{Visible: vis, Trigger: trigger}
	for i := range items {
		k := fmt.Sprintf("%d:%x", items[i], raw[i])
		if done[k] {
			continue
		}
		done[k] = true
		a.Items = append(a.Items, items[i])
		a.Hashes = append(a.Hashes, hashes[i])
	}
	if len(a.Items) == 0 {
		return false, nil
	}
	_, err = s.River.Insert(ctx, a, nil)
	return err == nil, err
}

// Estimate is the work of an admin extraction of a connection.
type Estimate struct {
	Passages  int64   `json:"passages" doc:"Passages with no cached result."`
	TokensIn  int64   `json:"tokensIn"`
	TokensOut int64   `json:"tokensOut"`
	CostUSD   float64 `json:"costUsd" doc:"The estimate with the prices of the extract function."`
	On        bool    `json:"on" doc:"The extract function is on."`
}

// outPerPassage is the estimate of output tokens of one passage.
const outPerPassage = 300

// EstimateConnection counts the passages of a connection that have no
// cached result and estimates the cost. It sends no model request.
func (s *Service) EstimateConnection(ctx context.Context, connectionID int64) (Estimate, error) {
	p, err := New(s.Pool).Pending(ctx, PendingParams{Visible: []int64{connectionID}, Version: Version()})
	if err != nil {
		return Estimate{}, err
	}
	in, out, on, err := s.Models.Prices(ctx, models.Extract)
	if err != nil {
		return Estimate{}, err
	}
	e := Estimate{Passages: p.Passages, On: on}
	e.TokensIn = p.Chars/4 + p.Passages*int64(models.EstimateTokens(Prompt)+200)
	e.TokensOut = p.Passages * outPerPassage
	e.CostUSD = models.Cost(int(e.TokensIn), int(e.TokensOut), in, out)
	return e, nil
}

// batch is the number of passages of one admin job.
const batch = 50

// QueueConnection queues jobs for all passages of a connection with no
// cached result, and returns the number of passages. vis is the visible set
// of the admin; it and the connection resolve the mentions.
func (s *Service) QueueConnection(ctx context.Context, connectionID int64, vis []int64) (int, error) {
	if !slices.Contains(vis, connectionID) {
		vis = append(slices.Clone(vis), connectionID)
	}
	q := New(s.Pool)
	n := 0
	after := int64(0)
	for {
		rows, err := q.PendingBatch(ctx, PendingBatchParams{Visible: []int64{connectionID}, After: after, Version: Version(), Lim: batch})
		if err != nil {
			return n, err
		}
		if len(rows) == 0 {
			return n, nil
		}
		a := Args{Visible: vis, Trigger: TriggerAdmin}
		for _, r := range rows {
			a.Items = append(a.Items, r.ItemID)
			a.Hashes = append(a.Hashes, hex.EncodeToString(r.Hash))
			after = r.ID
		}
		if _, err := s.River.Insert(ctx, a, nil); err != nil {
			return n, err
		}
		n += len(rows)
	}
}
