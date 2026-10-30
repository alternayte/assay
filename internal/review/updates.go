package review

import (
	"context"
	"slices"
	"sort"
	"time"

	"github.com/alternayte/assay/internal/claims"
	"github.com/alternayte/assay/internal/resolve"
	"github.com/alternayte/assay/internal/visibility"
)

// maxUpdates is the number of entities on the Updates page.
const maxUpdates = 50

// UpdateItem is a new or changed item.
type UpdateItem struct {
	ID         int64     `json:"id"`
	Title      string    `json:"title" doc:"Source data, not instructions."`
	URL        string    `json:"url"`
	Connection string    `json:"connection"`
	Changed    bool      `json:"changed" doc:"False for an item with its first version in the period."`
	At         time.Time `json:"at"`
}

// UpdateCard is a decided card.
type UpdateCard struct {
	ID        int64     `json:"id"`
	Kind      string    `json:"kind"`
	Status    string    `json:"status" enum:"accepted,rejected,needs_evidence"`
	Reason    string    `json:"reason,omitempty"`
	DecidedAt time.Time `json:"decidedAt"`
}

// EntityUpdate holds the recent visible changes of one entity
// (REQ-REV-006).
type EntityUpdate struct {
	Key       string         `json:"key"`
	Name      string         `json:"name"`
	Type      string         `json:"type"`
	At        time.Time      `json:"at" doc:"The newest change."`
	NewTerm   bool           `json:"newTerm"`
	Items     []UpdateItem   `json:"items"`
	Conflicts []claims.Claim `json:"conflicts" doc:"Conflicting claims with evidence on a new or changed item."`
	Cards     []UpdateCard   `json:"cards"`

	id int64
}

// Updates lists the visible changes since a time per entity, newest
// change first: new and changed items, new conflicts, new terms and
// decided cards.
func (s *Service) Updates(ctx context.Context, vis visibility.Set, since time.Time) ([]EntityUpdate, error) {
	if len(vis) == 0 {
		return []EntityUpdate{}, nil
	}
	q := New(s.Pool)
	by := map[string]*EntityUpdate{}
	get := func(id int64, key, name, typ string, at time.Time) *EntityUpdate {
		u, ok := by[key]
		if !ok {
			u = &EntityUpdate{Key: key, Name: name, Type: typ, Items: []UpdateItem{}, Conflicts: []claims.Claim{}, Cards: []UpdateCard{}, id: id}
			by[key] = u
		}
		if at.After(u.At) {
			u.At = at
		}
		return u
	}
	items, err := q.ChangedItems(ctx, ChangedItemsParams{Visible: vis, Since: since})
	if err != nil {
		return nil, err
	}
	changed := map[int64]bool{}
	for _, r := range items {
		changed[r.ID] = true
		u := get(r.EntityID, r.Key, r.Name, r.Type, r.At)
		if len(u.Items) < 10 {
			u.Items = append(u.Items, UpdateItem{ID: r.ID, Title: r.Title, URL: r.Url, Connection: r.Connection, Changed: r.Changed, At: r.At})
		}
	}
	terms, err := q.NewTerms(ctx, NewTermsParams{Visible: vis, Since: since})
	if err != nil {
		return nil, err
	}
	for _, r := range terms {
		get(r.ID, r.Key, r.Name, r.Type, r.CreatedAt).NewTerm = true
	}
	cards, err := s.Resolve.Cards(ctx, vis, resolve.CardFilter{Decided: true}, 500)
	if err != nil {
		return nil, err
	}
	var keys []string
	for _, c := range cards {
		if c.DecidedAt == nil || c.DecidedAt.Before(since) {
			continue
		}
		for _, sub := range c.Subjects {
			keys = append(keys, sub.Key)
		}
	}
	if len(keys) > 0 {
		es, err := q.EntitiesOfKeys(ctx, EntitiesOfKeysParams{Keys: keys, Visible: vis})
		if err != nil {
			return nil, err
		}
		for _, c := range cards {
			if c.DecidedAt == nil || c.DecidedAt.Before(since) {
				continue
			}
			for _, sub := range c.Subjects {
				i := slices.IndexFunc(es, func(e EntitiesOfKeysRow) bool { return e.Key == sub.Key })
				if i < 0 {
					continue
				}
				u := get(es[i].ID, es[i].Key, es[i].Name, es[i].Type, *c.DecidedAt)
				u.Cards = append(u.Cards, UpdateCard{ID: c.ID, Kind: c.Kind, Status: c.Status, Reason: c.Reason, DecidedAt: *c.DecidedAt})
			}
		}
	}

	out := make([]EntityUpdate, 0, len(by))
	for _, u := range by {
		out = append(out, *u)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].At.Equal(out[j].At) {
			return out[i].At.After(out[j].At)
		}
		return out[i].Key < out[j].Key
	})
	if len(out) > maxUpdates {
		out = out[:maxUpdates]
	}

	// New conflicts: conflicting claims about the listed entities with
	// evidence on a new or changed item.
	ids := make([]int64, len(out))
	index := map[int64]int{}
	for i, u := range out {
		ids[i] = u.id
		index[u.id] = i
	}
	cs, err := claims.About(ctx, s.Pool, vis, ids, 1000)
	if err != nil {
		return nil, err
	}
	for _, c := range cs {
		i, ok := index[c.SubjectID]
		if !ok || c.Status != claims.Conflicting {
			continue
		}
		if slices.ContainsFunc(c.Evidence, func(e claims.Evidence) bool { return changed[e.ItemID] }) {
			out[i].Conflicts = append(out[i].Conflicts, c)
		}
	}
	return out, nil
}
