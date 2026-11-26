package ask

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"time"

	"github.com/alternayte/assay/internal/entities"
	"github.com/alternayte/assay/internal/visibility"
)

// Limits of the questions to improve.
const (
	ImproveDays = 90
	ImproveMax  = 500
)

// ImproveQuestion is one question to improve. It never names the asker and
// holds no answer text.
type ImproveQuestion struct {
	ID            int64     `json:"id"`
	Text          string    `json:"text"`
	AskedAt       time.Time `json:"askedAt"`
	NoEvidence    bool      `json:"noEvidence"`
	Rating        string    `json:"rating" enum:",up,down" doc:"Empty without a mark."`
	Reason        string    `json:"reason"`
	WrongCitation bool      `json:"wrongCitation"`
}

// ImproveGroup holds the questions that name the same thing first. An empty
// key is the group of questions that name no thing.
type ImproveGroup struct {
	EntityKey  string            `json:"entityKey"`
	EntityName string            `json:"entityName"`
	Questions  []ImproveQuestion `json:"questions"`
}

// ToImprove lists the questions of all users of the last 90 days with no
// evidence or a not-helpful mark, whose connections are all visible to the
// reader, grouped by the first visible thing that each names. Larger
// groups come first. The group of questions that name no thing is last.
func (s *Service) ToImprove(ctx context.Context, vis visibility.Set) ([]ImproveGroup, error) {
	rows, err := New(s.Pool).QuestionsToImprove(ctx, QuestionsToImproveParams{Since: s.now().AddDate(0, 0, -ImproveDays),
		Visible: vis, Lim: ImproveMax})
	if err != nil {
		return nil, err
	}
	norms := make([]string, len(rows))
	for i, r := range rows {
		norms[i] = entities.NormalizeAlias(r.Text)
	}
	named, err := New(s.Pool).FirstMentioned(ctx, FirstMentionedParams{Visible: vis, Norms: norms})
	if err != nil {
		return nil, err
	}
	byRow := map[int]FirstMentionedRow{}
	for _, m := range named {
		byRow[int(m.Ord)-1] = m
	}
	groups := []ImproveGroup{}
	index := map[string]int{}
	for i, r := range rows {
		m := byRow[i]
		g, ok := index[m.Key]
		if !ok {
			g = len(groups)
			index[m.Key] = g
			groups = append(groups, ImproveGroup{EntityKey: m.Key, EntityName: m.Name, Questions: []ImproveQuestion{}})
		}
		groups[g].Questions = append(groups[g].Questions, ImproveQuestion{ID: r.ID, Text: r.Text, AskedAt: r.CreatedAt,
			NoEvidence: r.NoEvidence, Rating: r.Rating, Reason: r.Reason, WrongCitation: r.WrongCitation})
	}
	slices.SortStableFunc(groups, func(a, b ImproveGroup) int {
		switch {
		case (a.EntityKey == "") != (b.EntityKey == ""):
			if a.EntityKey == "" {
				return 1
			}
			return -1
		case len(a.Questions) != len(b.Questions):
			return len(b.Questions) - len(a.Questions)
		}
		return strings.Compare(strings.ToLower(a.EntityName), strings.ToLower(b.EntityName))
	})
	return groups, nil
}

// RatedQuestion is one rated question for an evaluation set. It never
// names the asker.
type RatedQuestion struct {
	ID         int64    `json:"id"`
	Question   string   `json:"question"`
	Rating     string   `json:"rating" enum:"up,down"`
	NoEvidence bool     `json:"noEvidence"`
	Sources    []string `json:"sources" doc:"The titles of the cited items of an up-marked answer, as the reader sees them."`
}

// Rated lists the questions of all users with a mark, whose connections are
// all visible to the reader.
func (s *Service) Rated(ctx context.Context, vis visibility.Set) ([]RatedQuestion, error) {
	rows, err := New(s.Pool).RatedQuestions(ctx, vis)
	if err != nil {
		return nil, err
	}
	out := []RatedQuestion{}
	for _, r := range rows {
		q := RatedQuestion{ID: r.ID, Question: r.Text, Rating: r.Rating, NoEvidence: r.NoEvidence, Sources: []string{}}
		if r.Rating == "up" && !r.NoEvidence {
			var a Answer
			if err := json.Unmarshal(r.Citations, &a); err != nil {
				return nil, err
			}
			var items []int64
			add := func(id int64) {
				if !slices.Contains(items, id) {
					items = append(items, id)
				}
			}
			for _, id := range a.Cited {
				for _, src := range a.Sources {
					if src.ID == id {
						add(src.ItemID)
					}
				}
				for _, cl := range a.Claims {
					if cl.ID == id {
						for _, e := range cl.Evidence {
							add(e.ItemID)
						}
					}
				}
			}
			titles, err := New(s.Pool).ItemTitles(ctx, ItemTitlesParams{Ids: orEmpty(items), Visible: vis})
			if err != nil {
				return nil, err
			}
			for _, id := range items {
				for _, t := range titles {
					if t.ID == id && !slices.Contains(q.Sources, t.Title) {
						q.Sources = append(q.Sources, t.Title)
					}
				}
			}
		}
		out = append(out, q)
	}
	return out, nil
}
