package resolve

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/alternayte/assay/internal/entities"
)

// LabelPair is a labelled pair of entity keys.
type LabelPair struct{ A, B string }

// LabelMention is a labelled mention in an item: the entity that it means,
// or no entity for an unnamed thing.
type LabelMention struct {
	Item    string // external ID
	Mention string
	Entity  string
}

// EvalLabels are the corpus answers that the report scores against.
type EvalLabels struct {
	Same, Different []LabelPair
	Mentions        []LabelMention
}

// BandResult is the precision and recall of one band of one kind
// (REQ-EVL-005). Precision is the share of labelled decisions in the band
// that are right. Recall is the share of positive labels that a right
// decision in the band finds.
type BandResult struct {
	Kind      string  `json:"kind"`
	Band      string  `json:"band"`
	Decisions int     `json:"decisions"`
	Correct   int     `json:"correct"`
	Found     int     `json:"found"`
	Positives int     `json:"positives"`
	Precision float64 `json:"precision"`
	Recall    float64 `json:"recall"`
}

// ReportKinds are the decision kinds of the report.
var ReportKinds = []string{KindSameEntity, KindMentionLink, KindDescribes}

type scored struct {
	kind, band string
	correct    bool
	label      string // the positive label that a right decision finds
}

// Report scores the stored decisions of the report kinds against labels.
// Unlabelled decisions do not count.
func (s *Service) Report(ctx context.Context, l EvalLabels) ([]BandResult, error) {
	q := New(s.Pool)
	rows, err := q.EvalDecisions(ctx, ReportKinds)
	if err != nil {
		return nil, err
	}
	var items []int64
	for _, r := range rows {
		for _, ref := range r.SubjectRefs {
			if id, ok := passageItem(ref); ok {
				items = append(items, id)
			}
		}
	}
	ext := map[int64]string{}
	if len(items) > 0 {
		xs, err := q.ItemExternalIDs(ctx, items)
		if err != nil {
			return nil, err
		}
		for _, x := range xs {
			ext[x.ID] = x.ExternalID
		}
	}
	same, diff := map[string]bool{}, map[string]bool{}
	pairKey := func(a, b string) string {
		if a > b {
			a, b = b, a
		}
		return a + "|" + b
	}
	for _, p := range l.Same {
		same[pairKey(p.A, p.B)] = true
	}
	for _, p := range l.Different {
		diff[pairKey(p.A, p.B)] = true
	}
	mentions := map[string]LabelMention{}
	positives := map[string]int{KindSameEntity: len(l.Same)}
	for _, m := range l.Mentions {
		mentions[m.Item+"|"+entities.NormalizeAlias(m.Mention)] = m
		if m.Entity != "" {
			positives[KindMentionLink]++
			positives[KindDescribes]++
		}
	}
	var out []scored
	for _, r := range rows {
		switch r.Kind {
		case KindSameEntity:
			if len(r.SubjectRefs) != 2 {
				continue
			}
			k := pairKey(r.SubjectRefs[0], r.SubjectRefs[1])
			if same[k] || diff[k] {
				out = append(out, scored{r.Kind, r.Band, same[k], "pair:" + k})
			}
		case KindDescribes, KindMentionLink:
			item, mention, cands := "", "", []string{}
			for _, ref := range r.SubjectRefs {
				switch {
				case strings.HasPrefix(ref, "passage:"):
					id, _ := passageItem(ref)
					item = ext[id]
				case strings.HasPrefix(ref, "mention:"):
					mention = strings.TrimPrefix(ref, "mention:")
				default:
					cands = append(cands, ref)
				}
			}
			m, ok := mentions[item+"|"+mention]
			if !ok || len(cands) == 0 {
				continue
			}
			chosen := cands[0]
			if r.Kind == KindMentionLink {
				chosen = choice(r.Probs, cands)
			}
			label := "mention:" + item + "|" + mention
			out = append(out, scored{r.Kind, r.Band, chosen == m.Entity, label})
		}
	}
	return tally(out, positives), nil
}

// choice is the candidate of the most likely option c1, c2 and so on; none
// and new give an empty candidate.
func choice(probs []byte, cands []string) string {
	var ps map[string]float64
	_ = json.Unmarshal(probs, &ps)
	best, bp := "", -1.0
	for o, p := range ps {
		if p > bp || (p == bp && o < best) {
			best, bp = o, p
		}
	}
	if i, err := strconv.Atoi(strings.TrimPrefix(best, "c")); err == nil && strings.HasPrefix(best, "c") && i >= 1 && i <= len(cands) {
		return cands[i-1]
	}
	return ""
}

func passageItem(ref string) (int64, bool) {
	parts := strings.Split(ref, ":")
	if len(parts) != 3 || parts[0] != "passage" {
		return 0, false
	}
	id, err := strconv.ParseInt(parts[1], 10, 64)
	return id, err == nil
}

func tally(ss []scored, positives map[string]int) []BandResult {
	type key struct{ kind, band string }
	res := map[key]*BandResult{}
	found := map[key]map[string]bool{}
	for _, s := range ss {
		k := key{s.kind, s.band}
		r := res[k]
		if r == nil {
			r = &BandResult{Kind: s.kind, Band: s.band, Positives: positives[s.kind]}
			res[k] = r
			found[k] = map[string]bool{}
		}
		r.Decisions++
		if s.correct {
			r.Correct++
			if !found[k][s.label] {
				found[k][s.label] = true
				r.Found++
			}
		}
	}
	var out []BandResult
	for _, kind := range ReportKinds {
		for _, band := range []string{BandHigh, BandMedium, BandLow} {
			r, ok := res[key{kind, band}]
			if !ok {
				continue
			}
			r.Precision = float64(r.Correct) / float64(r.Decisions)
			if r.Positives > 0 {
				r.Recall = float64(r.Found) / float64(r.Positives)
			}
			out = append(out, *r)
		}
	}
	return out
}

// ReportMarkdown renders the report as a Markdown table.
func ReportMarkdown(rs []BandResult) string {
	if len(rs) == 0 {
		return "No labelled decisions.\n"
	}
	var b strings.Builder
	b.WriteString("| Kind | Band | Decisions | Precision | Recall |\n|---|---|---|---|---|\n")
	for _, r := range rs {
		fmt.Fprintf(&b, "| %s | %s | %d | %.2f | %.2f |\n", r.Kind, r.Band, r.Decisions, r.Precision, r.Recall)
	}
	return b.String()
}
