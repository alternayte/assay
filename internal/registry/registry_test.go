package registry_test

import (
	"context"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
)

type world struct {
	env                     *testkit.Env
	owner, reviewer, member *testkit.Client
	github                  int64
	registry                int64
}

func start(t *testing.T) *world {
	t.Helper()
	env := testkit.Start(t)
	w := &world{env: env, owner: env.Setup("owner@acme.test")}
	w.reviewer, _ = env.Invite(w.owner, "reviewer@acme.test", "reviewer", "reviewer-password-1")
	w.member, _ = env.Invite(w.owner, "member@acme.test", "member", "member-password-1")
	_, base := testkit.FakeSources(t)
	w.github = testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-github", Kind: "github", BaseURL: base,
		Scope: map[string]any{"repositories": []string{"acme/payments-api", "acme/reminder-service"}}})
	testkit.ShareWithEveryone(t, env, w.owner, w.github)
	if r := env.RunNow(w.owner, w.github, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	if err := env.Server.Pool().QueryRow(context.Background(), "SELECT id FROM connections WHERE kind = 'registry'").Scan(&w.registry); err != nil {
		t.Fatal(err)
	}
	return w
}

type record struct {
	Key      string         `json:"key"`
	Kind     string         `json:"kind"`
	Thing    map[string]any `json:"thing"`
	Term     map[string]any `json:"term"`
	Versions []struct {
		ID       int64  `json:"id"`
		EditedBy string `json:"editedBy"`
	} `json:"versions"`
	Proposal int64 `json:"proposal"`
}

func (w *world) get(t *testing.T, cl *testkit.Client, key string) record {
	t.Helper()
	var r record
	cl.Must(http.StatusOK, http.MethodGet, "/api/v1/registry/records/"+url.PathEscape(key), nil).JSON(t, &r)
	return r
}

func TestSCN_REG_001_RegistryIsBuiltIn(t *testing.T) {
	w := start(t)
	var conns struct {
		Connections []struct {
			ID   int64  `json:"id"`
			Kind string `json:"kind"`
		} `json:"connections"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/connections", nil).JSON(t, &conns)
	found := false
	for _, c := range conns.Connections {
		found = found || (c.Kind == "registry" && c.ID == w.registry)
	}
	if !found {
		t.Fatal("the connection list has no registry")
	}
	if n := w.env.Count(`SELECT count(*) FROM connection_visibility v JOIN groups g ON g.id = v.group_id
WHERE v.connection_id = $1 AND g.builtin`, w.registry); n != 1 {
		t.Error("the registry is not shared with Everyone")
	}
	pause := w.owner.Do(http.MethodPut, "/api/v1/connections/"+strconv.FormatInt(w.registry, 10)+"/paused", map[string]any{"paused": true})
	purge := w.owner.Do(http.MethodDelete, "/api/v1/connections/"+strconv.FormatInt(w.registry, 10), nil)
	if pause.Status < 400 || purge.Status < 400 {
		t.Errorf("pause gives %d and purge gives %d, want refusals", pause.Status, purge.Status)
	}
	if n := w.env.Count("SELECT count(*) FROM connections WHERE id = $1 AND status = 'active'", w.registry); n != 1 {
		t.Error("the registry is not active after the refusals")
	}
}

func TestSCN_REG_002_ThingRecordMakesEntityClaimsAndVersion(t *testing.T) {
	w := start(t)
	thing := map[string]any{"type": "service", "name": "Card Gateway", "aliases": []string{"CGW"},
		"description": "The Card Gateway sends card payments to the card network.",
		"owner":       "backstage:group:default/payments-team",
		"relations":   []map[string]string{{"predicate": "depends_on", "object": "backstage:component:default/ledger-service"}},
		"links":       []string{"https://wiki.acme.test/cgw"}}
	var created record
	w.member.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "thing", "thing": thing}).JSON(t, &created)
	if created.Key != "registry:service:card-gateway" {
		t.Fatalf("key %q", created.Key)
	}
	var e struct {
		Name    string   `json:"name"`
		Type    string   `json:"type"`
		Aliases []string `json:"aliases"`
		Claims  []struct {
			Predicate string `json:"predicate"`
			ObjectKey string `json:"objectKey"`
			Origin    string `json:"origin"`
			Status    string `json:"status"`
			Evidence  []struct {
				SourceType string `json:"sourceType"`
			} `json:"evidence"`
		} `json:"claims"`
	}
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(created.Key), nil).JSON(t, &e)
	if e.Name != "Card Gateway" || e.Type != "service" {
		t.Errorf("entity %q of type %q", e.Name, e.Type)
	}
	want := map[string]string{"owned_by": "backstage:group:default/payments-team", "depends_on": "backstage:component:default/ledger-service"}
	for _, c := range e.Claims {
		if obj, ok := want[c.Predicate]; ok && c.ObjectKey == obj {
			delete(want, c.Predicate)
			if c.Origin != "human" || c.Status != "supported" || len(c.Evidence) != 1 || c.Evidence[0].SourceType != "registry" {
				t.Errorf("claim %s: origin %s status %s evidence %+v", c.Predicate, c.Origin, c.Status, c.Evidence)
			}
		}
	}
	if len(want) != 0 {
		t.Errorf("missing claims %v in %+v", want, e.Claims)
	}

	// An edit is a new version with the editor; a removed relation goes.
	thing["description"] = "The Card Gateway sends card payments to the card network and to the fraud check."
	thing["relations"] = []map[string]string{}
	w.member.Must(http.StatusOK, http.MethodPut, "/api/v1/registry/records/"+url.PathEscape(created.Key), map[string]any{"kind": "thing", "thing": thing})
	r := w.get(t, w.member, created.Key)
	if len(r.Versions) != 2 || r.Versions[0].EditedBy != "member@acme.test" {
		t.Fatalf("versions %+v", r.Versions)
	}
	if n := w.env.Count(`SELECT count(*) FROM claims c JOIN entities s ON s.id = c.subject_id
WHERE s.key = $1 AND c.predicate = 'depends_on' AND c.retracted_at IS NULL`, created.Key); n != 0 {
		t.Error("the removed relation is still a claim")
	}
	if n := w.env.Count(`SELECT count(*) FROM passages p JOIN source_items i ON i.id = p.item_id
WHERE i.connection_id = $1 AND p.norm LIKE '%fraud check%'`, w.registry); n != 1 {
		t.Error("the new description is not a passage")
	}

	// A second record with the same type and name is refused.
	if code := w.member.Do(http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "thing", "thing": thing}).Status; code != http.StatusConflict {
		t.Errorf("a duplicate record gives %d", code)
	}
}

func TestSCN_REG_003_TermEditsAndApproval(t *testing.T) {
	w := start(t)
	term := map[string]any{"name": "Manuscript", "synonyms": []string{"Submission"},
		"definition": "A paper that an author sends to a journal for review."}
	var created record
	w.member.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "term", "term": term}).JSON(t, &created)
	if r := w.get(t, w.member, created.Key); r.Term["status"] != "draft" {
		t.Fatalf("a member term starts as %v", r.Term["status"])
	}
	path := "/api/v1/registry/records/" + url.PathEscape(created.Key)

	// A member edit of a draft saves a version.
	term["definition"] = "A paper that an author sends to a journal for peer review."
	var saved record
	w.member.Must(http.StatusOK, http.MethodPut, path, map[string]any{"kind": "term", "term": term}).JSON(t, &saved)
	if saved.Proposal != 0 || len(w.get(t, w.member, created.Key).Versions) != 2 {
		t.Fatalf("a draft edit gave proposal %d", saved.Proposal)
	}

	// A member cannot approve. A reviewer can.
	term["status"] = "approved"
	if r := w.member.Do(http.MethodPut, path, map[string]any{"kind": "term", "term": term}); r.Status != http.StatusForbidden {
		t.Errorf("a member approval gives %d", r.Status)
	}
	w.reviewer.Must(http.StatusOK, http.MethodPut, path, map[string]any{"kind": "term", "term": term})
	if r := w.get(t, w.member, created.Key); r.Term["status"] != "approved" {
		t.Fatalf("after review the status is %v", r.Term["status"])
	}

	// A member edit of the approved definition becomes a proposal.
	term["definition"] = "Any paper in the journal system."
	w.member.Must(http.StatusOK, http.MethodPut, path, map[string]any{"kind": "term", "term": term}).JSON(t, &saved)
	if saved.Proposal == 0 {
		t.Fatal("a member edit of an approved definition saved a version")
	}
	if r := w.get(t, w.member, created.Key); len(r.Versions) != 3 || r.Term["definition"] != "A paper that an author sends to a journal for peer review." {
		t.Fatalf("the proposal changed the record: %+v", r)
	}
	if n := w.env.Count("SELECT count(*) FROM proposals WHERE id = $1 AND kind = 'term_edit' AND status = 'open'", saved.Proposal); n != 1 {
		t.Fatal("no open term_edit proposal")
	}
	// The reviewer accepts the proposal: the definition changes.
	w.reviewer.Must(http.StatusNoContent, http.MethodPost, "/api/v1/cards/"+strconv.FormatInt(saved.Proposal, 10)+"/accept", map[string]any{})
	r := w.get(t, w.member, created.Key)
	if r.Term["definition"] != "Any paper in the journal system." || r.Term["status"] != "approved" {
		t.Errorf("after accept the term is %+v", r.Term)
	}
	if r.Versions[0].EditedBy != "member@acme.test" {
		t.Errorf("the accepted version names %q, want the proposer", r.Versions[0].EditedBy)
	}
}

func TestSCN_REG_004_VersionsDiffAndRevert(t *testing.T) {
	w := start(t)
	thing := map[string]any{"type": "system", "name": "Billing", "description": "Billing sends invoices."}
	var created record
	w.member.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "thing", "thing": thing}).JSON(t, &created)
	path := "/api/v1/registry/records/" + url.PathEscape(created.Key)
	thing["description"] = "Billing sends invoices and reminders."
	w.member.Must(http.StatusOK, http.MethodPut, path, map[string]any{"kind": "thing", "thing": thing})
	r := w.get(t, w.member, created.Key)
	if len(r.Versions) != 2 {
		t.Fatalf("%d versions", len(r.Versions))
	}
	var v struct {
		Diff []struct {
			Op   string `json:"op"`
			Text string `json:"text"`
		} `json:"diff"`
	}
	w.member.Must(http.StatusOK, http.MethodGet, path+"/versions/"+strconv.FormatInt(r.Versions[0].ID, 10), nil).JSON(t, &v)
	ops := map[string]string{}
	for _, l := range v.Diff {
		if l.Op != "=" {
			ops[l.Op] = l.Text
		}
	}
	if ops["-"] != "Description: Billing sends invoices." || ops["+"] != "Description: Billing sends invoices and reminders." {
		t.Errorf("diff %+v", v.Diff)
	}
	w.member.Must(http.StatusOK, http.MethodPost, path+"/revert", map[string]any{"versionId": r.Versions[1].ID})
	r = w.get(t, w.member, created.Key)
	if len(r.Versions) != 3 || r.Thing["description"] != "Billing sends invoices." {
		t.Errorf("after revert: %d versions, description %v", len(r.Versions), r.Thing["description"])
	}
}

func TestSCN_RES_009_MergeKeepsRegistryEntityAndCanonicalName(t *testing.T) {
	w := start(t)
	var created record
	w.member.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "thing",
		"thing": map[string]any{"type": "service", "name": "Payments API", "description": "The Payments API takes card payments."}}).JSON(t, &created)
	catalog := "backstage:component:default/payments-api"
	var m struct {
		Winner string `json:"winner"`
	}
	w.reviewer.Must(http.StatusCreated, http.MethodPost, "/api/v1/merges",
		map[string]any{"keys": []string{catalog, created.Key}, "canonicalName": "Payments Platform API"}).JSON(t, &m)
	if m.Winner != created.Key {
		t.Fatalf("the winner is %s, want the registry entity", m.Winner)
	}
	var e struct {
		Key  string `json:"key"`
		Name string `json:"name"`
	}
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(catalog), nil).JSON(t, &e)
	if e.Key != created.Key || e.Name != "Payments Platform API" {
		t.Errorf("the merged entity is %s named %q", e.Key, e.Name)
	}
	r := w.get(t, w.member, created.Key)
	if r.Thing["name"] != "Payments Platform API" || len(r.Versions) != 2 || r.Versions[0].EditedBy != "reviewer@acme.test" {
		t.Errorf("the registry record is %v with %d versions by %+v", r.Thing["name"], len(r.Versions), r.Versions)
	}
}

func TestSCN_VOC_004_CSVRoundTrip(t *testing.T) {
	w := start(t)
	w.reviewer.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "term",
		"term": map[string]any{"name": "Manuscript", "synonyms": []string{"Submission", "Paper draft"}, "status": "approved",
			"definition": "A paper that an author sends to a journal, for review."}})
	w.reviewer.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "term",
		"term": map[string]any{"name": "Preprint", "related": []string{"registry:term:manuscript"}, "definition": "A paper before review."}})
	csv := w.reviewer.Must(http.StatusOK, http.MethodGet, "/api/v1/registry/terms.csv", nil).Body
	if !strings.HasPrefix(string(csv), "key,name,synonyms,definition,domain,owner,preferred,related,status\n") ||
		!strings.Contains(string(csv), `registry:term:manuscript,Manuscript,Submission; Paper draft,"A paper that an author sends to a journal, for review.",,,,,approved`) {
		t.Fatalf("export:\n%s", csv)
	}
	var res struct {
		Created, Updated, Unchanged int
		Errors                      []any
	}
	w.reviewer.Must(http.StatusOK, http.MethodPost, "/api/v1/registry/terms.csv", testkit.Raw{ContentType: "text/csv", Data: csv}).JSON(t, &res)
	if res.Created != 0 || res.Updated != 0 || res.Unchanged != 2 || len(res.Errors) != 0 {
		t.Errorf("a reimport of the export gives %+v", res)
	}
	again := w.reviewer.Must(http.StatusOK, http.MethodGet, "/api/v1/registry/terms.csv", nil).Body
	if string(again) != string(csv) {
		t.Errorf("the terms changed:\n%s\n%s", csv, again)
	}
	// A new row creates a term; a bad status is a row error.
	add := string(csv) + ",Editor,,The person who decides on a paper.,,,,,draft\n,Reviewer,,A person who reads a paper.,,,,,final\n"
	w.reviewer.Must(http.StatusOK, http.MethodPost, "/api/v1/registry/terms.csv", testkit.Raw{ContentType: "text/csv", Data: []byte(add)}).JSON(t, &res)
	if res.Created != 1 || len(res.Errors) != 1 {
		t.Errorf("an import with a new row and a bad row gives %+v", res)
	}
}
