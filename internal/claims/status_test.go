package claims_test

import (
	"testing"
	"time"

	"github.com/alternayte/assay/internal/claims"
)

var t0 = time.Date(2026, 3, 1, 0, 0, 0, 0, time.UTC)

func ev(source, state string) claims.Ev {
	return claims.Ev{SourceType: source, Stance: "supports", State: state, Kind: "field", At: t0}
}

func at(d int) *time.Time {
	t := t0.AddDate(0, 0, d)
	return &t
}

func TestSCN_CLM_003_StatusTable(t *testing.T) {
	prio := claims.DefaultPriority()
	cases := []struct {
		name        string
		facts       []claims.Fact
		res         []claims.Resolution
		superseded  map[int64]bool
		want        map[int64]claims.Status
		noted       int64
		notCurrent  int64
		conflictSet []int64
	}{
		{name: "one owner is supported",
			facts: []claims.Fact{{ID: 1, Subject: 10, Predicate: "owned_by", ObjectID: 20, Evidence: []claims.Ev{ev("catalog", "current")}}},
			want:  map[int64]claims.Status{1: claims.Supported}},
		{name: "two owners from equal sources conflict",
			facts: []claims.Fact{
				{ID: 1, Subject: 10, Predicate: "owned_by", ObjectID: 20, Evidence: []claims.Ev{ev("confluence", "current")}},
				{ID: 2, Subject: 10, Predicate: "owned_by", ObjectID: 21, Evidence: []claims.Ev{ev("repository", "current")}}},
			want: map[int64]claims.Status{1: claims.Conflicting, 2: claims.Conflicting}, conflictSet: []int64{2}},
		{name: "the highest-priority source wins with conflict_noted",
			facts: []claims.Fact{
				{ID: 1, Subject: 10, Predicate: "owned_by", ObjectID: 20, Evidence: []claims.Ev{ev("catalog", "current")}},
				{ID: 2, Subject: 10, Predicate: "owned_by", ObjectID: 21, Evidence: []claims.Ev{ev("confluence", "current")}}},
			want: map[int64]claims.Status{1: claims.Supported, 2: claims.Conflicting}, noted: 1},
		{name: "a multi-valued predicate does not conflict",
			facts: []claims.Fact{
				{ID: 1, Subject: 10, Predicate: "depends_on", ObjectID: 20, Evidence: []claims.Ev{ev("catalog", "current")}},
				{ID: 2, Subject: 10, Predicate: "depends_on", ObjectID: 21, Evidence: []claims.Ev{ev("confluence", "current")}}},
			want: map[int64]claims.Status{1: claims.Supported, 2: claims.Supported}},
		{name: "another environment does not conflict",
			facts: []claims.Fact{
				{ID: 1, Subject: 10, Predicate: "owned_by", ObjectID: 20, Scope: "prod", Evidence: []claims.Ev{ev("confluence", "current")}},
				{ID: 2, Subject: 10, Predicate: "owned_by", ObjectID: 21, Scope: "test", Evidence: []claims.Ev{ev("confluence", "current")}}},
			want: map[int64]claims.Status{1: claims.Supported, 2: claims.Supported}},
		{name: "an empty environment overlaps",
			facts: []claims.Fact{
				{ID: 1, Subject: 10, Predicate: "owned_by", ObjectID: 20, Scope: "prod", Evidence: []claims.Ev{ev("confluence", "current")}},
				{ID: 2, Subject: 10, Predicate: "owned_by", ObjectID: 21, Evidence: []claims.Ev{ev("confluence", "current")}}},
			want: map[int64]claims.Status{1: claims.Conflicting, 2: claims.Conflicting}},
		{name: "separate validity: older time-scoped values are the timeline",
			facts: []claims.Fact{
				{ID: 1, Subject: 10, Predicate: "status", ObjectNorm: "in progress", ValidFrom: at(0), Evidence: []claims.Ev{ev("jira", "current")}},
				{ID: 2, Subject: 10, Predicate: "status", ObjectNorm: "done", ValidFrom: at(5), Evidence: []claims.Ev{ev("jira", "current")}}},
			want: map[int64]claims.Status{1: claims.Supported, 2: claims.Supported}, notCurrent: 1},
		{name: "all evidence stale is stale",
			facts: []claims.Fact{{ID: 1, Subject: 10, Predicate: "owned_by", ObjectID: 20,
				Evidence: []claims.Ev{ev("confluence", "stale"), ev("jira", "stale")}}},
			want: map[int64]claims.Status{1: claims.Stale}},
		{name: "one current row keeps it supported",
			facts: []claims.Fact{{ID: 1, Subject: 10, Predicate: "owned_by", ObjectID: 20,
				Evidence: []claims.Ev{ev("confluence", "stale"), ev("jira", "current")}}},
			want: map[int64]claims.Status{1: claims.Supported}},
		{name: "a visible dispute is conflicting",
			facts: []claims.Fact{{ID: 1, Subject: 10, Predicate: "rationale", ObjectNorm: "cost",
				Evidence: []claims.Ev{ev("confluence", "current"), {SourceType: "jira", Stance: "disputes", State: "current", Kind: "passage", At: t0}}}},
			want: map[int64]claims.Status{1: claims.Conflicting}},
		{name: "a resolution supersedes the loser",
			facts: []claims.Fact{
				{ID: 1, Subject: 10, Predicate: "owned_by", ObjectID: 20, Evidence: []claims.Ev{ev("confluence", "current")}},
				{ID: 2, Subject: 10, Predicate: "owned_by", ObjectID: 21, Evidence: []claims.Ev{ev("confluence", "current")}}},
			res:  []claims.Resolution{{Subject: 10, Predicate: "owned_by", Chosen: 2, DecidedAt: t0.Add(time.Hour)}},
			want: map[int64]claims.Status{1: claims.Superseded, 2: claims.Supported}},
		{name: "newer support for the loser undoes the supersede",
			facts: []claims.Fact{
				{ID: 1, Subject: 10, Predicate: "owned_by", ObjectID: 20,
					Evidence: []claims.Ev{{SourceType: "confluence", Stance: "supports", State: "current", Kind: "field", At: t0.Add(2 * time.Hour)}}},
				{ID: 2, Subject: 10, Predicate: "owned_by", ObjectID: 21, Evidence: []claims.Ev{ev("confluence", "current")}}},
			res:  []claims.Resolution{{Subject: 10, Predicate: "owned_by", Chosen: 2, DecidedAt: t0.Add(time.Hour)}},
			want: map[int64]claims.Status{1: claims.Conflicting, 2: claims.Conflicting}},
		{name: "a visible decision supersedes another decision",
			facts:      []claims.Fact{{ID: 1, Subject: 30, Predicate: "decided", ObjectNorm: "use mysql", Evidence: []claims.Ev{ev("confluence", "current")}}},
			superseded: map[int64]bool{30: true},
			want:       map[int64]claims.Status{1: claims.Superseded}},
		{name: "only an open proposal is proposed",
			facts: []claims.Fact{{ID: 1, Subject: 10, Predicate: "owned_by", ObjectID: 20,
				Evidence: []claims.Ev{{SourceType: "review", Stance: "supports", State: "current", Kind: "proposal", At: t0}}}},
			want: map[int64]claims.Status{1: claims.Proposed}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := claims.Evaluate(c.facts, c.res, c.superseded, prio)
			for id, want := range c.want {
				if got[id].Status != want {
					t.Errorf("claim %d: %s, want %s", id, got[id].Status, want)
				}
				if got[id].ConflictNoted != (id == c.noted) {
					t.Errorf("claim %d: conflict noted %v", id, got[id].ConflictNoted)
				}
				if got[id].Current == (id == c.notCurrent) {
					t.Errorf("claim %d: current %v", id, got[id].Current)
				}
			}
			if c.conflictSet != nil && (len(got[1].ConflictWith) != 1 || got[1].ConflictWith[0] != c.conflictSet[0]) {
				t.Errorf("conflict set of claim 1: %v", got[1].ConflictWith)
			}
		})
	}

	// A priority change applies on the next read.
	facts := []claims.Fact{
		{ID: 1, Subject: 10, Predicate: "owned_by", ObjectID: 20, Evidence: []claims.Ev{ev("catalog", "current")}},
		{ID: 2, Subject: 10, Predicate: "owned_by", ObjectID: 21, Evidence: []claims.Ev{ev("confluence", "current")}}}
	prio["owned_by"] = []string{"confluence", "others"}
	got := claims.Evaluate(facts, nil, nil, prio)
	if got[2].Status != claims.Supported || !got[2].ConflictNoted || got[1].Status != claims.Conflicting {
		t.Fatalf("with confluence first: %+v", got)
	}
}
