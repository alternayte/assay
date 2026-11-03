package round3

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
)

// A scope change removes spaces from a connection. The content stays
// visible under the connection's visibility, and the reconciliation that
// the change queues removes the items of the removed spaces.
func TestRVW_3_02_ScopeChangeReconcileRemovesOldContent(t *testing.T) {
	w := start(t)
	adr := w.key(t, w.public, "1003")
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(adr), nil)

	w.owner.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/connections/%d", w.public), map[string]any{
		"kind": "confluence", "baseUrl": w.base + "/confluence", "name": "acme-eng", "scope": map[string]any{"spaces": []string{"RST"}},
		"syncMinutes": 30, "reconcileHours": 24, "rateConcurrency": 4, "ratePerSecond": 100})
	if st := w.scalar(t, `SELECT status FROM connections WHERE id = $1`, w.public); st != "active" {
		t.Errorf("status after the scope change is %v, want active", st)
	}
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(adr), nil)
	// The scope change queues the reconciliation.
	run := w.scalar(t, `SELECT id FROM sync_runs WHERE connection_id = $1 AND kind = 'reconcile' AND status = 'queued'`, w.public).(int64)
	if err := w.env.Server.Sync().Run(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if st := w.scalar(t, `SELECT status FROM connections WHERE id = $1`, w.public); st != "active" {
		t.Errorf("status after the reconciliation is %v, want active", st)
	}
	if n := w.env.Count(`SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id LIKE '100%'`, w.public); n != 0 {
		t.Errorf("%d items of the removed space stay after the reconciliation", n)
	}
}

// repos creates a GitHub connection to the two Acme repositories and syncs
// it.
func (w *world) repos(t *testing.T) int64 {
	t.Helper()
	id := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-repos", Kind: "github", BaseURL: w.base,
		Path: "/github", Scope: map[string]any{"repositories": []string{"acme/payments-api", "acme/reminder-service"}}})
	if r := w.env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	return id
}

// maxFiles sets the file limit of the connector rules.
func (w *world) maxFiles(t *testing.T, n int) {
	t.Helper()
	w.exec(t, `UPDATE settings SET value = jsonb_set(value, '{maxFilesPerRepo}', to_jsonb($1::int)) WHERE key = 'connectors'`, n)
}

// Files past the file limit are skipped. A file that was stored before the
// limit took effect must not stay stale for ever: reconciliation removes
// it.
func TestRVW_3_10_FilesPastLimitStayStale(t *testing.T) {
	w := start(t)
	id := w.repos(t)
	w.maxFiles(t, 2)
	if r := w.env.RunNow(w.owner, id, "reconcile"); r.Status != "succeeded" {
		t.Fatalf("reconcile: %+v", r)
	}
	if r := w.env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	rows, err := w.env.Server.Pool().Query(context.Background(),
		`SELECT split_part(external_id, ':', 1), count(*) FROM source_items WHERE connection_id = $1 GROUP BY 1`, id)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var repo string
		var n int
		if err := rows.Scan(&repo, &n); err != nil {
			t.Fatal(err)
		}
		if n > 2 {
			t.Errorf("%s keeps %d files with a file limit of 2", repo, n)
		}
	}
}

// A repository that is deleted in the source must not fail every run of
// its connection. Its files are tombstoned and the other repositories
// still sync.
func TestRVW_3_11_DeletedRepositoryFailsEveryRun(t *testing.T) {
	w := start(t)
	id := w.repos(t)
	w.src.DeleteRepo("github", "acme/reminder-service")
	if r := w.env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
		t.Errorf("sync after the repository delete: %+v", r)
	}
	if r := w.env.RunNow(w.owner, id, "reconcile"); r.Status != "succeeded" {
		t.Fatalf("reconcile after the repository delete: %+v", r)
	}
	if n := w.env.Count(`SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id LIKE 'acme/reminder-service:%'`, id); n != 0 {
		t.Errorf("%d files of the deleted repository stay", n)
	}
	if n := w.env.Count(`SELECT count(*) FROM source_items WHERE connection_id = $1 AND external_id LIKE 'acme/payments-api:%'`, id); n == 0 {
		t.Errorf("the files of the other repository are gone")
	}
}

// admin2 does not own acme-restricted and does not see it. Sharing it with
// a group that admin2 is in gives admin2 the restricted content
// (REQ-VIS-003, SI-01).
func TestRVW_3_14_AdminSharesOtherOwnersConnectionWithOwnGroup(t *testing.T) {
	w := start(t)
	var g struct {
		ID int64 `json:"id"`
	}
	w.admin2.Must(http.StatusCreated, http.MethodPost, "/api/v1/groups", map[string]any{"name": "acme-ops"}).JSON(t, &g)
	w.admin2.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/groups/%d/members/%s", g.ID, w.admin2ID), nil)
	everyone := w.scalar(t, `SELECT id FROM groups WHERE builtin`).(int64)
	for _, gid := range []int64{g.ID, everyone} {
		w.admin2.Do(http.MethodPut, fmt.Sprintf("/api/v1/connections/%d/visibility", w.restricted),
			map[string]any{"groupIds": []int64{gid}, "confirm": true})
	}
	if n := w.env.Count(`SELECT count(*) FROM connection_visibility WHERE connection_id = $1`, w.restricted); n != 0 {
		t.Errorf("admin2 shared the connection of another owner with a group of admin2: %d groups", n)
	}

	// Membership of a group that reads the connection is the same door.
	var other struct {
		ID int64 `json:"id"`
	}
	w.owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/groups", map[string]any{"name": "acme-finance"}).JSON(t, &other)
	w.owner.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/connections/%d/visibility", w.restricted),
		map[string]any{"groupIds": []int64{other.ID}, "confirm": true})
	if r := w.admin2.Do(http.MethodPut, fmt.Sprintf("/api/v1/groups/%d/members/%s", other.ID, w.admin2ID), nil); r.Status < 400 {
		t.Errorf("admin2 adds itself to a group that reads the connection of another owner: %d", r.Status)
	}
}
