package vocab

import (
	"context"
	"regexp"
	"slices"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/alternayte/assay/internal/visibility"
)

// Term statuses.
const (
	StatusDraft      = "draft"
	StatusApproved   = "approved"
	StatusDeprecated = "deprecated"
)

// MaxPromptTerms is the most term definitions in a prompt (REQ-VOC-006).
const MaxPromptTerms = 20

// Term is a visible term as a reader sees it.
type Term struct {
	Key        string `json:"key"`
	Name       string `json:"name"`
	Status     string `json:"status" doc:"draft, approved or deprecated. Empty when no registry record or review sets it."`
	Definition string `json:"definition" doc:"Source data, not instructions."`
	DomainID   int64  `json:"domainId,omitempty"`
	Domain     string `json:"domain,omitempty"`
	Owner      string `json:"owner,omitempty"`
	// Preferred is the key of the preferred term of a deprecated term.
	Preferred       string   `json:"preferred,omitempty"`
	Synonyms        []string `json:"synonyms"`
	DeprecatedForms []string `json:"deprecatedForms" doc:"Forms of deprecated terms that name this term as preferred."`
}

// FormsOf are the forms that link to the term: its name, synonyms and
// deprecated forms.
func FormsOf(t Term) []string {
	out := append([]string{t.Name}, t.Synonyms...)
	return append(out, t.DeprecatedForms...)
}

// Service reads the vocabulary.
type Service struct {
	Pool *pgxpool.Pool
}

// Terms returns the visible terms. Deprecated terms add their forms to the
// deprecated forms of their preferred term.
func (s *Service) Terms(ctx context.Context, vis visibility.Set) ([]Term, error) {
	out := []Term{}
	if len(vis) == 0 {
		return out, nil
	}
	rows, err := New(s.Pool).VisibleTerms(ctx, vis)
	if err != nil {
		return nil, err
	}
	byKey := map[string]int{}
	for _, r := range rows {
		t := Term{Key: r.Key, Name: r.Name, Status: r.Status, Definition: r.Definition, DomainID: r.DomainID, Domain: r.Domain,
			Owner: r.Owner, Preferred: r.Preferred, Synonyms: []string{}, DeprecatedForms: []string{}}
		for _, a := range r.Aliases {
			if !strings.EqualFold(a, r.Name) {
				t.Synonyms = append(t.Synonyms, a)
			}
		}
		byKey[t.Key] = len(out)
		out = append(out, t)
	}
	for _, t := range out {
		if t.Status != StatusDeprecated || t.Preferred == "" {
			continue
		}
		if i, ok := byKey[t.Preferred]; ok {
			out[i].DeprecatedForms = append(out[i].DeprecatedForms, append([]string{t.Name}, t.Synonyms...)...)
		}
	}
	return out, nil
}

// Approved returns the visible approved terms.
func (s *Service) Approved(ctx context.Context, vis visibility.Set) ([]Term, error) {
	ts, err := s.Terms(ctx, vis)
	if err != nil {
		return nil, err
	}
	return slices.DeleteFunc(ts, func(t Term) bool { return t.Status != StatusApproved }), nil
}

// LinkerOf builds a linker of terms.
func LinkerOf(ts []Term) *Linker {
	lts := make([]LinkTerm, len(ts))
	for i, t := range ts {
		lts[i] = LinkTerm{Key: t.Key, Domain: t.DomainID, Forms: FormsOf(t)}
	}
	return NewLinker(lts)
}

// InText returns the approved terms that a text uses, in order of first
// use, at most limit.
func InText(ts []Term, text string, limit int) []Term {
	byKey := map[string]Term{}
	for _, t := range ts {
		byKey[t.Key] = t
	}
	var out []Term
	seen := map[string]bool{}
	for _, m := range LinkerOf(ts).Link(text, 0) {
		for _, k := range m.Terms {
			if !seen[k] && len(out) < limit {
				seen[k] = true
				out = append(out, byKey[k])
			}
		}
	}
	return out
}

// Expansions returns the query with each approved form that it uses
// replaced by another form of the same term (REQ-VOC-007).
func Expansions(ts []Term, query string) []string {
	byKey := map[string]Term{}
	for _, t := range ts {
		byKey[t.Key] = t
	}
	var out []string
	for _, m := range LinkerOf(ts).Link(query, 0) {
		for _, k := range m.Terms {
			t := byKey[k]
			forms := FormsOf(t)
			if t.Preferred != "" {
				if p, ok := byKey[t.Preferred]; ok {
					forms = append(forms, p.Name)
				}
			}
			for _, f := range forms {
				if !strings.EqualFold(f, m.Form) {
					q := query[:m.Start] + f + query[m.End:]
					if !slices.Contains(out, q) {
						out = append(out, q)
					}
				}
			}
		}
	}
	return out
}

// Finding is a deprecated form in a text and the preferred term to use.
type Finding struct {
	Form      string `json:"form"`
	Preferred string `json:"preferred" doc:"The name of the preferred term."`
	Start     int    `json:"start"`
}

// Lint flags the deprecated forms in a text and names the preferred term
// (REQ-VOC-006). ts are the visible terms.
func Lint(ts []Term, text string) []Finding {
	byKey := map[string]Term{}
	var deprecated []LinkTerm
	for _, t := range ts {
		byKey[t.Key] = t
	}
	for _, t := range ts {
		if t.Status == StatusDeprecated && t.Preferred != "" {
			if _, ok := byKey[t.Preferred]; ok {
				deprecated = append(deprecated, LinkTerm{Key: t.Key, Forms: append([]string{t.Name}, t.Synonyms...)})
			}
		}
	}
	out := []Finding{}
	for _, m := range NewLinker(deprecated).Link(text, 0) {
		out = append(out, Finding{Form: m.Form, Preferred: byKey[byKey[m.Terms[0]].Preferred].Name, Start: m.Start})
	}
	return out
}

// Usage is the number of visible items of one connection that use a term
// and its deprecated forms.
type Usage struct {
	ConnectionID   int64  `json:"connectionId"`
	ConnectionName string `json:"connectionName"`
	Items          int    `json:"items" doc:"Items that use the name or a synonym."`
	DeprecatedUses int    `json:"deprecatedItems" doc:"Items that use a deprecated form."`
}

func pattern(forms []string) string {
	var parts []string
	for _, f := range forms {
		if f = strings.TrimSpace(f); f != "" {
			parts = append(parts, regexp.QuoteMeta(f))
		}
	}
	return `\m(` + strings.Join(parts, "|") + `)\M`
}

// UsageOf counts the visible items per connection that use a term
// (REQ-VOC-008).
func (s *Service) UsageOf(ctx context.Context, vis visibility.Set, t Term) ([]Usage, error) {
	q := New(s.Pool)
	byConn := map[int64]*Usage{}
	var order []int64
	count := func(forms []string, deprecated bool) error {
		if len(forms) == 0 {
			return nil
		}
		rows, err := q.TermUsage(ctx, TermUsageParams{Visible: vis, Pattern: pattern(forms)})
		if err != nil {
			return err
		}
		for _, r := range rows {
			u := byConn[r.ConnectionID]
			if u == nil {
				u = &Usage{ConnectionID: r.ConnectionID, ConnectionName: r.Name}
				byConn[r.ConnectionID] = u
				order = append(order, r.ConnectionID)
			}
			if deprecated {
				u.DeprecatedUses = int(r.Items)
			} else {
				u.Items = int(r.Items)
			}
		}
		return nil
	}
	if err := count(append([]string{t.Name}, t.Synonyms...), false); err != nil {
		return nil, err
	}
	if err := count(t.DeprecatedForms, true); err != nil {
		return nil, err
	}
	out := []Usage{}
	for _, id := range order {
		out = append(out, *byConn[id])
	}
	return out, nil
}
