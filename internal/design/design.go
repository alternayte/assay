// Package design writes the design pages of repositories, systems and
// services from indexed source code. A repository page has an overview, an
// architecture diagram, one section per component and a data section. A
// build after each sync writes a digest per component from its code and
// then the sections from the digests and the documents of the repository.
// Every sentence, diagram node and diagram edge cites a passage or a claim.
// People correct a page section by section: a user version is pinned, and
// regeneration never replaces it.
package design

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"log/slog"
	"slices"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/riverqueue/river"

	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/models"
	"github.com/alternayte/assay/internal/resolve"
	"github.com/alternayte/assay/internal/settings"
	"github.com/alternayte/assay/internal/vocab"
)

// Rules are the design settings.
type Rules struct {
	TokenBudget  int `json:"tokenBudget" minimum:"1000" doc:"The most model tokens that one build of one repository uses. A build stops at the budget and keeps what it wrote."`
	MaxSections  int `json:"maxSections" minimum:"1" maximum:"40" doc:"The most component sections of a design page. Small components merge into their parent."`
	SectionWords int `json:"sectionWords" minimum:"40" maximum:"600" doc:"The most words in one section."`
}

// Setting is the design setting.
var Setting = settings.Define("design", "Limits of the design pages that Assay writes from source code.",
	Rules{TokenBudget: 300000, MaxSections: 12, SectionWords: 200},
	func(r Rules) error {
		if r.TokenBudget < 1000 || r.MaxSections < 1 || r.MaxSections > 40 || r.SectionWords < 40 || r.SectionWords > 600 {
			return errors.New("the budget must be 1000 tokens or more, the sections 1 to 40, and the words 40 to 600")
		}
		return nil
	})

// Service builds, reads and corrects design pages.
type Service struct {
	Pool     *pgxpool.Pool
	Models   *models.Gateway
	Entities *entities.Service
	Vocab    *vocab.Service
	Resolve  *resolve.Service
	River    *river.Client[pgx.Tx]
	Log      *slog.Logger
	Now      func() time.Time
}

func (s *Service) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Workers adds the design workers.
func (s *Service) Workers(ws *river.Workers) {
	river.AddWorker(ws, &BuildWorker{S: s})
	river.AddWorker(ws, &SectionWorker{S: s})
}

// textOf is the text of a version for a card.
type textOf struct {
	Markdown string `json:"markdown"`
	Diagram  string `json:"diagram"`
}

// updatePayload is the payload of a design_update card: the pinned user
// version and the model draft for the changed code.
type updatePayload struct {
	EntityKey string `json:"entityKey"`
	SectionID int64  `json:"sectionId"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	Pinned    int64  `json:"pinnedVersionId"`
	Draft     int64  `json:"draftVersionId"`
	InputHash string `json:"inputHash"`
	Current   textOf `json:"current"`
	Proposed  textOf `json:"proposed"`
}

// checkPinned opens a design_update card when the section has a pinned
// user version and the code under it changed: a cited passage is gone, or
// the input of the section differs from the input that the user version
// corrected.
func (s *Service) checkPinned(ctx context.Context, sec DesignSection, input []byte, draft DesignVersion, conns []int64) error {
	q := New(s.Pool)
	user, err := q.LatestUserVersion(ctx, sec.ID)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	stale, err := s.staleCites(ctx, user)
	if err != nil {
		return err
	}
	if !stale && bytes.Equal(user.InputHash, input) {
		return nil
	}
	open, err := q.OpenSectionCard(ctx, OpenSectionCardParams{Kind: "design_update", SectionID: sec.ID, InputHash: hexOf(input)})
	if err != nil || open {
		return err
	}
	row, err := q.SectionByID(ctx, sec.ID)
	if err != nil {
		return err
	}
	p := updatePayload{EntityKey: row.EntityKey, SectionID: sec.ID, Slug: sec.Slug, Title: sec.Title, Pinned: user.ID, Draft: draft.ID,
		InputHash: hexOf(input), Current: textOf{user.Markdown, user.Diagram}, Proposed: textOf{draft.Markdown, draft.Diagram}}
	payload, _ := json.Marshal(p)
	cs := append(slices.Clone(user.Connections), conns...)
	slices.Sort(cs)
	_, err = q.InsertCard(ctx, InsertCardParams{Kind: "design_update", Target: "entity:" + row.EntityKey, Payload: payload, Via: "system",
		Connections: slices.Compact(cs)})
	return err
}

// staleCites reports whether a passage that a version cites is gone.
func (s *Service) staleCites(ctx context.Context, v DesignVersion) (bool, error) {
	var set citeSet
	_ = json.Unmarshal(v.Cites, &set)
	gone, err := s.goneCites(ctx, v.Connections, set.Markers)
	return len(gone) > 0, err
}

// goneCites returns the markers whose passage no longer exists.
func (s *Service) goneCites(ctx context.Context, conns []int64, cs map[string]Cite) (map[string]bool, error) {
	var items []int64
	for _, c := range cs {
		if c.Kind == "passage" {
			items = append(items, c.ItemID)
		}
	}
	out := map[string]bool{}
	if len(items) == 0 {
		return out, nil
	}
	rows, err := New(s.Pool).CurrentPassages(ctx, CurrentPassagesParams{Items: items, Visible: conns})
	if err != nil {
		return nil, err
	}
	have := map[string]bool{}
	for _, r := range rows {
		have[hexKey(r.ItemID, r.Hash)] = true
	}
	for m, c := range cs {
		if c.Kind == "passage" && !have[hexKeyStr(c.ItemID, c.Hash)] {
			out[m] = true
		}
	}
	return out, nil
}

func hexKey(item int64, hash []byte) string { return hexKeyStr(item, hex.EncodeToString(hash)) }

func hexKeyStr(item int64, hash string) string {
	b, _ := json.Marshal([]any{item, hash})
	return string(b)
}
