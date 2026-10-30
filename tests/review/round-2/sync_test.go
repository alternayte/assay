package round2

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
)

// A tombstone removes an item that no longer exists in its source. The
// fix for the entity of a deleted page drops only the entity of the item.
// The terms that the glossary table of a deleted page gives keep their
// source and their names, so a member still reads them in the Glossary and
// on their pages, with no evidence left.
func TestRVW_2_06_TombstoneLeavesTermsOfDeletedGlossaryPage(t *testing.T) {
	w := start(t)
	term := w.scalar(t, `SELECT e.key FROM entities e JOIN entity_aliases a ON a.entity_id = e.id
		WHERE e.type = 'term' AND a.connection_id = $1 AND a.alias = 'Article Processing Charge'`, w.public).(string)
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(term), nil)

	w.src.DeletePage("1008")
	if r := w.env.RunNow(w.owner, w.public, "reconcile"); r.Status != "succeeded" || r.Deleted != 1 {
		t.Fatalf("reconcile: %+v", r)
	}
	if r := w.member.Do(http.MethodGet, "/api/v1/entities/"+url.PathEscape(term), nil); r.Status != http.StatusNotFound {
		t.Errorf("member reads the term of a deleted glossary page: %d %.300s", r.Status, r.Body)
	}
	if r := w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/terms", nil); strings.Contains(string(r.Body), "Article Processing Charge") {
		t.Errorf("the Glossary lists a term of a deleted glossary page")
	}
}
