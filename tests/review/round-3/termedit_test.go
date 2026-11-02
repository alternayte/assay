package round3

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
)

// An accepted term_edit card saved the whole record of the proposal. A
// reviewer edit that came after the proposal was lost, and a term that a
// reviewer deprecated after the proposal became approved again. A field that
// changed both in the proposal and after it, to another value, must stop the
// accept.
func TestRVW_3_04_OldTermEditOverwritesNewerEdits(t *testing.T) {
	w := start(t)
	term := map[string]any{"name": "Settlement Window", "synonyms": []string{"Cut-off"}, "status": "approved",
		"definition": "The time in which Acme sends card payments to the bank."}
	var created struct {
		Key string `json:"key"`
	}
	w.reviewer.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "term", "term": term}).JSON(t, &created)
	path := "/api/v1/registry/records/" + url.PathEscape(created.Key)

	propose := func(def string) int64 {
		t.Helper()
		edit := map[string]any{"name": "Settlement Window", "synonyms": []string{"Cut-off"}, "definition": def}
		var saved struct {
			Proposal int64 `json:"proposal"`
		}
		w.member.Must(http.StatusOK, http.MethodPut, path, map[string]any{"kind": "term", "term": edit}).JSON(t, &saved)
		if saved.Proposal == 0 {
			t.Fatal("the member edit of an approved definition gave no proposal")
		}
		return saved.Proposal
	}
	first := propose("The daily time in which Acme sends card payments to the bank.")
	second := propose("The hour in which Acme sends refunds to the bank.")

	// The reviewer deprecates the term and adds a synonym after the proposals.
	term["status"] = "deprecated"
	term["synonyms"] = []string{"Cut-off", "Payment window"}
	w.reviewer.Must(http.StatusOK, http.MethodPut, path, map[string]any{"kind": "term", "term": term})

	w.reviewer.Must(http.StatusNoContent, http.MethodPost, "/api/v1/cards/"+strconv.FormatInt(first, 10)+"/accept", map[string]any{})
	var got struct {
		Term struct {
			Definition string   `json:"definition"`
			Status     string   `json:"status"`
			Synonyms   []string `json:"synonyms"`
		} `json:"term"`
	}
	w.member.Must(http.StatusOK, http.MethodGet, path, nil).JSON(t, &got)
	if got.Term.Status != "deprecated" {
		t.Errorf("after the accept the status is %q, want deprecated", got.Term.Status)
	}
	if len(got.Term.Synonyms) != 2 || got.Term.Synonyms[1] != "Payment window" {
		t.Errorf("after the accept the synonyms are %v, want the newer edit", got.Term.Synonyms)
	}
	if got.Term.Definition != "The daily time in which Acme sends card payments to the bank." {
		t.Errorf("after the accept the definition is %q, want the proposal", got.Term.Definition)
	}

	// The second proposal changes the definition that the first accept changed.
	r := w.reviewer.Do(http.MethodPost, "/api/v1/cards/"+strconv.FormatInt(second, 10)+"/accept", map[string]any{})
	if r.Status != http.StatusConflict || !strings.Contains(string(r.Body), `"stale"`) {
		t.Errorf("the accept of a stale edit gives %d %s, want 409 stale", r.Status, r.Body)
	}
	w.member.Must(http.StatusOK, http.MethodGet, path, nil).JSON(t, &got)
	if got.Term.Definition != "The daily time in which Acme sends card payments to the bank." {
		t.Errorf("after the stale accept the definition is %q", got.Term.Definition)
	}
	if n := w.env.Count(`SELECT count(*) FROM proposals WHERE id = $1 AND status = 'open'`, second); n != 1 {
		t.Error("the stale proposal is not open")
	}
}
