package resolve

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/entities"
)

// A card stands for one question. Its dedupe key names the question, and
// the database holds at most one open card per key. The keys use entity ids,
// not keys, because a merge changes the visible key and not the id.

func mergeKey(a, b int64) string {
	return fmt.Sprintf("merge:%d:%d", min(a, b), max(a, b))
}

// mentionPrefix is the start of the keys of a mention in a connection. The
// candidate follows it.
func mentionPrefix(connectionID int64, mention string) string {
	return fmt.Sprintf("mention_link:%d:%s:", connectionID, entities.NormalizeAlias(mention))
}

func mentionKey(connectionID int64, mention string, candidate int64) string {
	return fmt.Sprintf("%s%d", mentionPrefix(connectionID, mention), candidate)
}

func conflictKey(subject int64, predicate, scope string) string {
	return fmt.Sprintf("conflict:%d:%s:%s", subject, predicate, scope)
}

func termKey(norm string) string { return "new_term:" + norm }

// likePrefix escapes a key prefix for LIKE.
func likePrefix(prefix string) string {
	r := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`)
	return r.Replace(prefix) + "%"
}

// occurrence is one place where the question of a card was found.
type occurrence struct {
	ItemID      int64             `json:"itemId"`
	PassageHash string            `json:"passageHash"`
	Index       int               `json:"index"`
	Quote       string            `json:"quote"`
	At          time.Time         `json:"at"`
	Side        string            `json:"side"`
	Claim       content.Extracted `json:"claim"`
}

func (o occurrence) same(x occurrence) bool {
	return o.ItemID == x.ItemID && o.PassageHash == x.PassageHash && o.Index == x.Index
}

// occurrenceOf is the occurrence of a mention payload.
func occurrenceOf(pl mentionPayload) occurrence {
	return occurrence{ItemID: pl.ItemID, PassageHash: pl.PassageHash, Index: pl.Index, Quote: pl.Claim.Quote, At: pl.At, Side: pl.Side,
		Claim: pl.Claim}
}

// all returns a payload for each occurrence of a card. A card from before
// occurrences has one.
func (pl mentionPayload) all() []mentionPayload {
	if len(pl.Occurrences) == 0 {
		return []mentionPayload{pl}
	}
	out := make([]mentionPayload, len(pl.Occurrences))
	for i, o := range pl.Occurrences {
		c := pl
		c.ItemID, c.PassageHash, c.Index, c.At, c.Side, c.Claim, c.Occurrences = o.ItemID, o.PassageHash, o.Index, o.At, o.Side, o.Claim, nil
		out[i] = c
	}
	return out
}

// addOccurrence adds an occurrence to the open card id, unless the card
// has it already.
func (s *Service) addOccurrence(ctx context.Context, id int64, raw []byte, o occurrence) error {
	var pl mentionPayload
	if err := json.Unmarshal(raw, &pl); err != nil {
		return err
	}
	if len(pl.Occurrences) == 0 {
		pl.Occurrences = []occurrence{occurrenceOf(pl)}
	}
	for _, x := range pl.Occurrences {
		if x.same(o) {
			return nil
		}
	}
	pl.Occurrences = append(pl.Occurrences, o)
	payload, err := json.Marshal(pl)
	if err != nil {
		return err
	}
	return New(s.Pool).SetCardPayload(ctx, SetCardPayloadParams{ID: id, Payload: payload})
}

// knownMention handles a mention whose question has a card already. An
// open card gets the occurrence. An accepted card links the occurrence to
// its candidate. A rejected card drops it. It reports whether a card
// answered the mention, so that no model is asked.
func (s *Service) knownMention(ctx context.Context, pl mentionPayload) (bool, error) {
	prefix := likePrefix(mentionPrefix(pl.ConnectionID, pl.Mention.Value))
	rows, err := New(s.Pool).CardsByKeyPrefix(ctx, CardsByKeyPrefixParams{Kind: "mention_link", Prefix: &prefix})
	if err != nil || len(rows) == 0 {
		return false, err
	}
	var open, accepted, rejected *CardsByKeyPrefixRow
	for i := range rows {
		switch rows[i].Status {
		case "open":
			open = &rows[i]
		case "accepted":
			accepted = &rows[i]
		case "rejected":
			rejected = &rows[i]
		}
	}
	switch {
	case accepted != nil:
		var prev mentionPayload
		if err := json.Unmarshal(accepted.Payload, &prev); err != nil {
			return false, err
		}
		pl.Candidate = prev.Candidate
		payload, err := json.Marshal(pl)
		if err != nil {
			return false, err
		}
		err = pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error { return s.acceptMention(ctx, tx, payload) })
		if errors.Is(err, ErrNotFound) {
			return true, nil
		}
		return true, err
	case open != nil:
		return true, s.addOccurrence(ctx, open.ID, open.Payload, occurrenceOf(pl))
	case rejected != nil:
		return true, nil
	}
	return false, nil
}

// closeOpen closes an open card whose question no longer applies.
func (s *Service) closeOpen(ctx context.Context, tx pgx.Tx, id int64, conns []int64, reason string) error {
	now := s.now()
	if err := New(tx).CloseCard(ctx, CloseCardParams{ID: id, Reason: reason, DecidedAt: &now}); err != nil {
		return err
	}
	return audit.Write(ctx, tx, audit.Entry{Action: "cards.closed", Target: fmt.Sprintf("card:%d", id),
		Details: map[string]any{"reason": reason, "automatic": true}, Connections: append([]int64{}, conns...)})
}

// Reasons of the cards that the sweep closes.
const (
	reasonSourceGone   = "The source no longer says this."
	reasonMerged       = "The entities are merged."
	reasonNoConflict   = "The conflict no longer exists."
	reasonEntityGone   = "The entity no longer exists."
	reasonSameQuestion = "Same question as card %d."
)

// SweepCards keeps the open cards true to their questions. It drops the
// occurrences whose passage is no longer current, closes the cards whose
// question no longer applies, sets the key of each card, and closes the
// later cards of a key that an earlier card holds. The occurrences of a
// closed duplicate move to the kept card.
func (s *Service) SweepCards(ctx context.Context) error {
	rows, err := New(s.Pool).SweepCards(ctx)
	if err != nil {
		return err
	}
	type keptCard struct {
		id      int64
		payload []byte
	}
	kept := map[string]*keptCard{}
	for _, r := range rows {
		key, payload, reason, err := s.sweepKey(ctx, r)
		if err != nil {
			return err
		}
		if reason != "" {
			err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
				if r.Kind == "unnamed" {
					if err := s.rejectUnnamed(ctx, tx, r.Payload); err != nil {
						return err
					}
				}
				return s.closeOpen(ctx, tx, r.ID, r.Connections, reason)
			})
			if err != nil {
				return err
			}
			continue
		}
		if k, ok := kept[r.Kind+"\x00"+key]; ok {
			if r.Kind == "mention_link" {
				var pl mentionPayload
				if err := json.Unmarshal(payload, &pl); err != nil {
					return err
				}
				for _, o := range pl.all() {
					if err := s.addOccurrence(ctx, k.id, k.payload, occurrenceOf(o)); err != nil {
						return err
					}
					fresh, err := New(s.Pool).CardsByKey(ctx, CardsByKeyParams{Kind: r.Kind, Key: &key})
					if err != nil {
						return err
					}
					for _, c := range fresh {
						if c.ID == k.id {
							k.payload = c.Payload
						}
					}
				}
			}
			err := pgx.BeginFunc(ctx, s.Pool, func(tx pgx.Tx) error {
				if r.Kind == "unnamed" {
					if err := s.rejectUnnamed(ctx, tx, r.Payload); err != nil {
						return err
					}
				}
				return s.closeOpen(ctx, tx, r.ID, r.Connections, fmt.Sprintf(reasonSameQuestion, k.id))
			})
			if err != nil {
				return err
			}
			continue
		}
		kept[r.Kind+"\x00"+key] = &keptCard{id: r.ID, payload: payload}
		if !slices.Equal(payload, r.Payload) {
			if err := New(s.Pool).SetCardPayload(ctx, SetCardPayloadParams{ID: r.ID, Payload: payload}); err != nil {
				return err
			}
		}
		if r.DedupeKey == nil || *r.DedupeKey != key {
			if err := New(s.Pool).SetCardKey(ctx, SetCardKeyParams{ID: r.ID, DedupeKey: &key}); err != nil {
				return err
			}
		}
	}
	return nil
}

// sweepKey returns the key and the payload of an open card, with the
// occurrences that are still current, or the reason to close it.
func (s *Service) sweepKey(ctx context.Context, r SweepCardsRow) (key string, payload []byte, reason string, err error) {
	q := New(s.Pool)
	root := func(k string) (int64, bool, error) {
		id, err := q.EntityRoot(ctx, k)
		if errors.Is(err, pgx.ErrNoRows) {
			return 0, false, nil
		}
		return id, err == nil, err
	}
	payload = r.Payload
	switch r.Kind {
	case "merge":
		if len(r.SubjectRefs) != 2 {
			return r.Target, payload, "", nil
		}
		a, okA, err := root(r.SubjectRefs[0])
		if err != nil {
			return "", nil, "", err
		}
		b, okB, err := root(r.SubjectRefs[1])
		if err != nil {
			return "", nil, "", err
		}
		switch {
		case !okA || !okB:
			return "", nil, reasonEntityGone, nil
		case a == b:
			return "", nil, reasonMerged, nil
		}
		return mergeKey(a, b), payload, "", nil
	case "mention_link", "unnamed":
		var pl mentionPayload
		if err := json.Unmarshal(r.Payload, &pl); err != nil {
			return "", nil, "", err
		}
		var current []occurrence
		for _, o := range pl.all() {
			hash, err := hex.DecodeString(o.PassageHash)
			if err != nil {
				continue
			}
			ok, err := q.PassageCurrent(ctx, PassageCurrentParams{ItemID: o.ItemID, Hash: hash})
			if err != nil {
				return "", nil, "", err
			}
			if ok {
				current = append(current, occurrenceOf(o))
			}
		}
		if len(current) == 0 {
			return "", nil, reasonSourceGone, nil
		}
		id, ok, err := root(pl.Candidate)
		if err != nil {
			return "", nil, "", err
		}
		if !ok {
			return "", nil, reasonEntityGone, nil
		}
		if len(pl.Occurrences) > 0 || len(current) != 1 {
			pl.Occurrences = current
		}
		if payload, err = json.Marshal(pl); err != nil {
			return "", nil, "", err
		}
		if r.Kind == "unnamed" {
			return r.Target, payload, "", nil
		}
		return mentionKey(pl.ConnectionID, pl.Mention.Value, id), payload, "", nil
	case "conflict":
		var pl struct {
			Claims []int64 `json:"claims"`
		}
		if err := json.Unmarshal(r.Payload, &pl); err != nil {
			return "", nil, "", err
		}
		if len(pl.Claims) == 0 {
			return "", nil, reasonNoConflict, nil
		}
		n, err := q.CurrentClaims(ctx, pl.Claims)
		if err != nil {
			return "", nil, "", err
		}
		if n < 2 {
			return "", nil, reasonNoConflict, nil
		}
		c, err := q.ClaimIdentity(ctx, pl.Claims[0])
		if errors.Is(err, pgx.ErrNoRows) {
			return "", nil, reasonNoConflict, nil
		}
		if err != nil {
			return "", nil, "", err
		}
		return conflictKey(c.SubjectID, c.Predicate, c.ScopeEnv), payload, "", nil
	case "new_term":
		return termKey(strings.TrimPrefix(r.Target, "phrase:")), payload, "", nil
	}
	return r.Target, payload, "", nil
}
