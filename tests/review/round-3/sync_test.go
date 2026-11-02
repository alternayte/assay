package round3

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"sync"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/testkit"
)

// gate is a proxy to the fixture server that holds each request while it is
// closed.
type gate struct {
	mu      sync.Mutex
	open    chan struct{}
	arrived chan struct{}
}

func newGate(t *testing.T, base string) (*gate, string) {
	t.Helper()
	g := &gate{open: make(chan struct{}), arrived: make(chan struct{}, 64)}
	close(g.open)
	target, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	srv := httptest.NewServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		open := g.open
		g.mu.Unlock()
		select {
		case <-open:
		default:
			g.arrived <- struct{}{}
			<-open
		}
		proxy.ServeHTTP(rw, r)
	}))
	t.Cleanup(srv.Close)
	return g, srv.URL
}

func (g *gate) close() {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.open = make(chan struct{})
}

func (g *gate) release() {
	g.mu.Lock()
	defer g.mu.Unlock()
	close(g.open)
}

// A reconciliation that started before a pause holds the permission check
// of the time before the pause. After a resume, only a reconciliation that
// starts after the resume may show the content again (REQ-SYN-002).
func TestRVW_3_01_StaleReconcileActivatesResumedConnection(t *testing.T) {
	w := start(t)
	g, base := newGate(t, w.base)
	id := testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-gated", Kind: "confluence", BaseURL: base,
		Path: "/confluence", Scope: map[string]any{"spaces": []string{"ENG"}}})
	if r := w.env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	var q struct {
		RunID int64 `json:"runId"`
	}
	w.owner.Must(http.StatusAccepted, http.MethodPost, fmt.Sprintf("/api/v1/connections/%d/reconcile", id), nil).JSON(t, &q)
	g.close()
	done := make(chan error, 1)
	go func() { done <- w.env.Server.Sync().Run(context.Background(), q.RunID) }()
	select {
	case <-g.arrived:
	case <-time.After(30 * time.Second):
		t.Fatal("the reconciliation sent no request")
	}
	path := fmt.Sprintf("/api/v1/connections/%d/paused", id)
	w.owner.Must(http.StatusNoContent, http.MethodPut, path, map[string]any{"paused": true})
	w.owner.Must(http.StatusNoContent, http.MethodPut, path, map[string]any{"paused": false})
	g.release()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if st := w.scalar(t, `SELECT status FROM connections WHERE id = $1`, id); st != "resuming" {
		t.Errorf("a reconciliation that started before the pause set the status to %v, want resuming", st)
	}
	if r := w.env.RunNow(w.owner, id, "reconcile"); r.Status != "succeeded" {
		t.Fatalf("reconcile after resume: %+v", r)
	}
	if st := w.scalar(t, `SELECT status FROM connections WHERE id = $1`, id); st != "active" {
		t.Errorf("a reconciliation after the resume left the status %v, want active", st)
	}
}

// A scope change removes spaces from a connection. The content of the
// removed spaces must not stay readable until the next reconciliation.
func TestRVW_3_02_ScopeChangeKeepsOldContentVisible(t *testing.T) {
	w := start(t)
	adr := w.key(t, w.public, "1003")
	w.member.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+url.PathEscape(adr), nil)

	w.owner.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/connections/%d", w.public), map[string]any{
		"kind": "confluence", "baseUrl": w.base + "/confluence", "name": "acme-eng", "scope": map[string]any{"spaces": []string{"RST"}},
		"syncMinutes": 30, "reconcileHours": 24, "rateConcurrency": 4, "ratePerSecond": 100})
	if r := w.member.Do(http.MethodGet, "/api/v1/entities/"+url.PathEscape(adr), nil); r.Status != http.StatusNotFound {
		t.Errorf("member reads an entity of a removed space after the scope change: %d", r.Status)
	}
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
