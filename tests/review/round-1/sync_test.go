package round1

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync"
	"testing"
)

// The connection test route uses the stored token of a connection when the
// body has none. It must not send that token to another host than the one
// of the connection. Here admin2 points the stored token of the owner's
// restricted connection at a host that records the header.
func TestRVW_1_08_ConnectionTestSendsStoredTokenToAnyHost(t *testing.T) {
	w := start(t)
	var mu sync.Mutex
	var got []string
	other := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		mu.Lock()
		got = append(got, r.Header.Get("Authorization"))
		mu.Unlock()
		http.NotFound(rw, r)
	}))
	t.Cleanup(other.Close)

	w.admin2.Do(http.MethodPost, "/api/v1/connections/test", map[string]any{
		"kind": "confluence", "baseUrl": other.URL + "/confluence", "connectionId": w.restricted,
	})
	// A change of the base URL without a new token must not keep the token.
	w.admin2.Must(http.StatusUnprocessableEntity, http.MethodPut, fmt.Sprintf("/api/v1/connections/%d", w.restricted), map[string]any{
		"kind": "confluence", "baseUrl": other.URL + "/confluence", "name": "acme-restricted", "scope": map[string]any{"spaces": []string{"RST"}},
	})
	mu.Lock()
	defer mu.Unlock()
	for _, h := range got {
		if h != "" {
			t.Errorf("another host received the stored token of connection %d: %q", w.restricted, h)
			return
		}
	}
}

// Purge deletes all data of a connection (REQ-SYN-009). Entities whose only
// source was the connection, and their profiles, must go too.
func TestRVW_1_09_PurgeLeavesEntitiesAndProfiles(t *testing.T) {
	w := start(t)
	if _, err := w.env.Server.Resolve().Profiles(context.Background()); err != nil {
		t.Fatal(err)
	}
	before := w.env.Count(`SELECT count(*) FROM entities e WHERE EXISTS (SELECT 1 FROM entity_sources s
		WHERE s.entity_id = e.id AND s.connection_id = $1)
		AND NOT EXISTS (SELECT 1 FROM entity_sources s WHERE s.entity_id = e.id AND s.connection_id <> $1)`, w.restricted)
	if before == 0 {
		t.Fatal("acme-restricted gives no entity")
	}
	w.owner.Must(http.StatusNoContent, http.MethodDelete, fmt.Sprintf("/api/v1/connections/%d", w.restricted), nil)

	orphans := w.env.Count(`SELECT count(*) FROM entities e WHERE NOT EXISTS (SELECT 1 FROM entity_sources s WHERE s.entity_id = e.id)`)
	profiles := w.env.Count(`SELECT count(*) FROM entity_profiles p WHERE NOT EXISTS (SELECT 1 FROM entity_sources s WHERE s.entity_id = p.entity_id)`)
	canaries := w.env.Count(`SELECT count(*) FROM entity_profiles WHERE text ILIKE '%canary-7q4z%' OR text ILIKE '%falcon%'`)
	if orphans != 0 || profiles != 0 || canaries != 0 {
		t.Errorf("after purge: %d entities without a source, %d profiles of them, %d profiles with purged text", orphans, profiles, canaries)
	}
}

// A tombstone removes an item that no longer exists in its source. The
// decision entity that only this item gave must not stay readable with the
// title of the deleted page.
func TestRVW_1_10_TombstoneLeavesEntityOfDeletedPage(t *testing.T) {
	w := start(t)
	adr := w.key(t, w.public, "1003")
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(adr), nil)

	w.src.DeletePage("1003")
	if r := w.env.RunNow(w.owner, w.public, "reconcile"); r.Status != "succeeded" || r.Deleted != 1 {
		t.Fatalf("reconcile: %+v", r)
	}
	if r := w.member.Do(http.MethodGet, "/api/v1/entities/"+url.PathEscape(adr), nil); r.Status != http.StatusNotFound {
		t.Errorf("member reads the entity of a deleted page: %d %.300s", r.Status, r.Body)
	}
}
