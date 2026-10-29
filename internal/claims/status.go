package claims

import (
	"slices"
	"time"
)

// Status is the status of a claim for one reader, computed from the
// evidence that the reader can see (SDD section 10).
type Status string

// The statuses, in the order the rules apply.
const (
	Proposed    Status = "proposed"
	Superseded  Status = "superseded"
	Stale       Status = "stale"
	Conflicting Status = "conflicting"
	Supported   Status = "supported"
)

// Ev is one visible evidence row of a claim.
type Ev struct {
	SourceType string
	Stance     string // supports or disputes
	State      string // current or stale
	Kind       string // field, passage, human or proposal
	At         time.Time
}

// Fact is a claim as the status rules see it.
type Fact struct {
	ID         int64
	Subject    int64
	Predicate  string
	ObjectID   int64
	ObjectNorm string
	Scope      string
	ValidFrom  *time.Time
	Evidence   []Ev
}

// Resolution is a reviewer choice for a subject, a predicate and a scope.
type Resolution struct {
	Subject   int64
	Predicate string
	Scope     string
	Chosen    int64
	DecidedAt time.Time
	Reopened  bool
}

// Result is the status of one claim.
type Result struct {
	Status Status `json:"status" enum:"proposed,superseded,stale,conflicting,supported"`
	// ConflictNoted is set on the one claim of a conflict set that has the
	// highest-priority source type. It is Supported.
	ConflictNoted bool `json:"conflictNoted"`
	// Current is false for an older value of a time-scoped predicate. The
	// timeline shows it.
	Current bool `json:"current"`
	// ConflictWith lists the other claims of the conflict set.
	ConflictWith []int64 `json:"conflictWith,omitempty"`
}

// Priority orders the source types per predicate. "others" stands for the
// remaining types.
type Priority map[string][]string

// DefaultPriority is the priority of the predicate registry.
func DefaultPriority() Priority {
	p := Priority{}
	for name, pr := range Registry {
		p[name] = slices.Clone(pr.Priority)
	}
	return p
}

// rank is the position of a source type for a predicate. Lower is better.
func (p Priority) rank(predicate, sourceType string) int {
	order := p[predicate]
	if order == nil {
		order = Registry[predicate].Priority
	}
	others := len(order)
	for i, t := range order {
		if t == sourceType {
			return i
		}
		if t == "others" {
			others = i
		}
	}
	return others
}

type groupKey struct {
	subject   int64
	predicate string
	scope     string
}

// Equivalence holds pairs of normalized text values that mean the same
// (REQ-RES-008). Key a pair with Pair.
type Equivalence map[[2]string]bool

// Pair is the key of two text values in an Equivalence.
func Pair(a, b string) [2]string {
	if a > b {
		a, b = b, a
	}
	return [2]string{a, b}
}

// Evaluate computes the status of each claim. superseded holds the decision
// entities that a visible supersedes claim points to. Text values in eq do
// not conflict.
func Evaluate(facts []Fact, resolutions []Resolution, superseded map[int64]bool, priority Priority, eq Equivalence) map[int64]Result {
	out := make(map[int64]Result, len(facts))
	byID := map[int64]*Fact{}
	for i := range facts {
		byID[facts[i].ID] = &facts[i]
	}

	// Older values of time-scoped predicates are the timeline. They are not
	// current and do not conflict.
	newest := map[groupKey]time.Time{}
	for _, f := range facts {
		if Registry[f.Predicate].TimeScoped && f.ValidFrom != nil {
			k := groupKey{f.Subject, f.Predicate, f.Scope}
			if f.ValidFrom.After(newest[k]) {
				newest[k] = *f.ValidFrom
			}
		}
	}
	current := func(f *Fact) bool {
		if !Registry[f.Predicate].TimeScoped || f.ValidFrom == nil {
			return true
		}
		return !f.ValidFrom.Before(newest[groupKey{f.Subject, f.Predicate, f.Scope}])
	}

	res := map[groupKey]Resolution{}
	for _, r := range resolutions {
		if !r.Reopened {
			res[groupKey{r.Subject, r.Predicate, r.Scope}] = r
		}
	}

	// The first rules: Proposed, Superseded and Stale.
	open := map[int64]bool{}
	for i := range facts {
		f := &facts[i]
		r := Result{Status: Supported, Current: current(f)}
		supports, proposals, stale := 0, 0, 0
		for _, e := range f.Evidence {
			if e.Stance != "supports" {
				continue
			}
			supports++
			if e.Kind == "proposal" {
				proposals++
			}
			if e.State == "stale" {
				stale++
			}
		}
		switch {
		case supports > 0 && proposals == supports:
			r.Status = Proposed
		case supersededBy(f, res, superseded):
			r.Status = Superseded
		case supports > 0 && stale == supports:
			r.Status = Stale
		default:
			open[f.ID] = true
		}
		out[f.ID] = r
	}

	// Conflicting: a visible disputes row, or a conflict set.
	edges := map[int64][]int64{}
	for i := range facts {
		a := &facts[i]
		if !open[a.ID] {
			continue
		}
		for _, e := range a.Evidence {
			if e.Stance == "disputes" {
				r := out[a.ID]
				r.Status = Conflicting
				out[a.ID] = r
				break
			}
		}
		p, ok := Registry[a.Predicate]
		if !ok || p.Multi || p.NeverConflicts || !out[a.ID].Current {
			continue
		}
		for j := i + 1; j < len(facts); j++ {
			b := &facts[j]
			if !open[b.ID] || b.Subject != a.Subject || b.Predicate != a.Predicate || !out[b.ID].Current || !overlap(a.Scope, b.Scope) {
				continue
			}
			if a.ObjectID != b.ObjectID || (a.ObjectNorm != b.ObjectNorm && !eq[Pair(a.ObjectNorm, b.ObjectNorm)]) {
				edges[a.ID] = append(edges[a.ID], b.ID)
				edges[b.ID] = append(edges[b.ID], a.ID)
			}
		}
	}
	seen := map[int64]bool{}
	for i := range facts {
		start := facts[i].ID
		if seen[start] || len(edges[start]) == 0 {
			continue
		}
		set := component(start, edges, seen)
		best, bestRank, tie := int64(0), 1<<30, false
		for _, id := range set {
			f := byID[id]
			rk := 1 << 30
			for _, e := range f.Evidence {
				if e.Stance == "supports" {
					rk = min(rk, priority.rank(f.Predicate, e.SourceType))
				}
			}
			switch {
			case rk < bestRank:
				best, bestRank, tie = id, rk, false
			case rk == bestRank:
				tie = true
			}
		}
		for _, id := range set {
			r := out[id]
			r.ConflictWith = without(set, id)
			if !tie && id == best && !disputed(byID[id]) {
				r.Status, r.ConflictNoted = Supported, true
			} else {
				r.Status = Conflicting
			}
			out[id] = r
		}
	}
	return out
}

// supersededBy reports whether a resolution chose another claim with no
// newer visible support for this one, or a visible decision supersedes the
// subject.
func supersededBy(f *Fact, res map[groupKey]Resolution, superseded map[int64]bool) bool {
	if superseded[f.Subject] {
		return true
	}
	r, ok := res[groupKey{f.Subject, f.Predicate, f.Scope}]
	if !ok || r.Chosen == f.ID {
		return false
	}
	for _, e := range f.Evidence {
		if e.Stance == "supports" && e.At.After(r.DecidedAt) {
			return false
		}
	}
	return true
}

func disputed(f *Fact) bool {
	for _, e := range f.Evidence {
		if e.Stance == "disputes" {
			return true
		}
	}
	return false
}

func component(start int64, edges map[int64][]int64, seen map[int64]bool) []int64 {
	var out []int64
	stack := []int64{start}
	seen[start] = true
	for len(stack) > 0 {
		id := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		out = append(out, id)
		for _, n := range edges[id] {
			if !seen[n] {
				seen[n] = true
				stack = append(stack, n)
			}
		}
	}
	slices.Sort(out)
	return out
}

func without(ids []int64, id int64) []int64 {
	out := make([]int64, 0, len(ids)-1)
	for _, x := range ids {
		if x != id {
			out = append(out, x)
		}
	}
	return out
}

// overlap reports whether two environment scopes overlap: equal, or one is
// empty.
func overlap(a, b string) bool { return a == b || a == "" || b == "" }
