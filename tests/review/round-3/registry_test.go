package round3

import (
	"net/http"
	"strings"
	"testing"
)

// TestRVW_3_17_RegistryRecordNamesRestrictedEntity checks that a registry
// record cannot name an entity whose only source is a restricted
// connection. The registry is shared with Everyone, so a registry source
// would show the key of the entity to every member.
func TestRVW_3_17_RegistryRecordNamesRestrictedEntity(t *testing.T) {
	w := start(t)
	hidden := w.key(t, w.restricted, "2003")
	r := w.owner.Do(http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "thing",
		"thing": map[string]any{"type": "decision", "name": "Audit export", "relations": []map[string]any{{"predicate": "supersedes", "object": hidden}}}})
	if r.Status != http.StatusUnprocessableEntity || !strings.Contains(string(r.Body), "everyone can read") {
		t.Fatalf("record with a restricted relation: %d %s", r.Status, r.Body)
	}
	if n := w.env.Count(`SELECT count(*) FROM entity_sources s JOIN entities e ON e.id = s.entity_id JOIN connections c ON c.id = s.connection_id
		WHERE e.key = $1 AND c.kind = 'registry'`, hidden); n != 0 {
		t.Fatalf("the restricted entity has %d registry sources", n)
	}
	w.member.Must(http.StatusNotFound, http.MethodGet, "/api/v1/entities/"+hidden, nil)
	public := w.scalar(t, `SELECT e.key FROM entities e JOIN entity_sources s ON s.entity_id = e.id
		WHERE s.connection_id = $1 AND e.type = 'decision' ORDER BY e.id LIMIT 1`, w.public).(string)
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/registry/records", map[string]any{"kind": "thing",
		"thing": map[string]any{"type": "decision", "name": "Audit export", "relations": []map[string]any{{"predicate": "supersedes", "object": public}}}})
}
