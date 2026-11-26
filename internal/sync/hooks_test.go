package sync_test

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

const hookRepo = "acme/reminder-service"

// hookConn is a GitHub connection on one repository after its first sync,
// with a webhook URL path and secret.
func hookConn(t *testing.T) (env *testkit.Env, admin *testkit.Client, src *fixtures.Sources, conn int64, path, secret string) {
	t.Helper()
	env = testkit.Start(t)
	admin = env.Setup("owner@acme.test")
	src, base := testkit.FakeSources(t)
	conn = testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-github", Kind: "github", BaseURL: base,
		Scope: map[string]any{"repositories": []string{hookRepo}}, Options: map[string]any{"historyMonths": 0}})
	if r := env.RunNow(admin, conn, "sync"); r.Status != "succeeded" {
		t.Fatalf("first sync %+v", r)
	}
	var w struct {
		URL    string `json:"url"`
		Secret string `json:"secret"`
	}
	admin.Must(http.StatusOK, http.MethodPost, fmt.Sprintf("/api/v1/connections/%d/webhook", conn), nil).JSON(t, &w)
	u, err := url.Parse(w.URL)
	if err != nil || w.Secret == "" || !strings.HasPrefix(u.Path, fmt.Sprintf("/hooks/%d/", conn)) {
		t.Fatalf("webhook %+v", w)
	}
	return env, admin, src, conn, u.Path, w.Secret
}

// post sends a webhook body with headers and returns the status.
func post(t *testing.T, env *testkit.Env, path string, body []byte, headers map[string]string) int {
	t.Helper()
	req, err := http.NewRequestWithContext(t.Context(), http.MethodPost, env.HTTP.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	return resp.StatusCode
}

func sign(secret string, body []byte) string {
	m := hmac.New(sha256.New, []byte(secret))
	m.Write(body)
	return "sha256=" + hex.EncodeToString(m.Sum(nil))
}

// pushBody is a GitHub push to the default branch that modifies files.
func pushBody(files ...string) []byte {
	b, _ := json.Marshal(map[string]any{"ref": "refs/heads/main",
		"repository": map[string]any{"full_name": hookRepo, "default_branch": "main"},
		"commits":    []map[string]any{{"added": []string{}, "removed": []string{}, "modified": files}}})
	return b
}

// itemJobs returns the item IDs of the sync_items jobs of a connection.
func itemJobs(t *testing.T, env *testkit.Env, conn int64) [][]string {
	t.Helper()
	rows, err := env.Server.Pool().Query(t.Context(),
		"SELECT args FROM river_job WHERE kind = 'sync_items' AND queue = 'sync' AND (args->>'connection_id')::bigint = $1 ORDER BY id", conn)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var out [][]string
	for rows.Next() {
		var args struct {
			IDs []string `json:"ids"`
		}
		var b []byte
		if err := rows.Scan(&b); err != nil {
			t.Fatal(err)
		}
		_ = json.Unmarshal(b, &args)
		out = append(out, args.IDs)
	}
	return out
}

func TestSCN_SYN_027_WebhookWithoutValidSignatureIsRefused(t *testing.T) {
	env, _, _, conn, path, secret := hookConn(t)
	body := pushBody("README.md")
	for _, c := range []struct {
		name    string
		path    string
		headers map[string]string
		want    int
	}{
		{"no signature", path, map[string]string{"X-GitHub-Event": "push"}, http.StatusUnauthorized},
		{"wrong secret", path, map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": sign("other", body)}, http.StatusUnauthorized},
		{"no prefix", path, map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": strings.TrimPrefix(sign(secret, body), "sha256=")},
			http.StatusUnauthorized},
		{"wrong token", path + "x",
			map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": sign(secret, body)}, http.StatusNotFound},
		{"unknown connection", fmt.Sprintf("/hooks/%d/%s", conn+100, path[strings.LastIndex(path, "/")+1:]),
			map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": sign(secret, body)}, http.StatusNotFound},
	} {
		if got := post(t, env, c.path, body, c.headers); got != c.want {
			t.Fatalf("%s: status %d, want %d", c.name, got, c.want)
		}
	}
	if jobs := itemJobs(t, env, conn); len(jobs) != 0 {
		t.Fatalf("jobs %v after refused webhooks", jobs)
	}
	if n := env.Count("SELECT received::int FROM connection_webhooks WHERE connection_id = $1", conn); n != 0 {
		t.Fatalf("%d webhooks counted, want 0", n)
	}
}

func TestSCN_SYN_028_GitHubPushReadsOnlyTheNamedFiles(t *testing.T) {
	env, _, src, conn, path, secret := hookConn(t)
	src.PutFile("github", hookRepo, "README.md", []byte("# Reminder service\n\nThe reminder service sends the Acme payment reminders by email.\n"))
	// A change that the push does not name stays for polling.
	src.PutFile("github", hookRepo, "catalog-info.yaml", []byte("apiVersion: backstage.io/v1alpha1\nkind: Component\nmetadata:\n  name: reminder-service\n  description: Sends the late payment notices.\n"))
	body := pushBody("README.md")
	if got := post(t, env, path, body, map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": sign(secret, body)}); got != http.StatusAccepted {
		t.Fatalf("status %d, want 202", got)
	}
	// The same event again in the same minute makes no second job.
	post(t, env, path, body, map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": sign(secret, body)})
	jobs := itemJobs(t, env, conn)
	want := hookRepo + ":README.md"
	if len(jobs) != 1 || len(jobs[0]) != 1 || jobs[0][0] != want {
		t.Fatalf("jobs %v, want one job for %s", jobs, want)
	}
	mark := len(src.Hits())
	if err := env.Server.Sync().SyncItems(context.Background(), conn, jobs[0], nil); err != nil {
		t.Fatal(err)
	}
	blobs := 0
	for _, h := range src.Hits()[mark:] {
		if h.Method != http.MethodGet && h.Method != http.MethodHead {
			t.Fatalf("the job sent %s %s", h.Method, h.Path)
		}
		if strings.Contains(h.Path, "/git/blobs/") {
			blobs++
		}
	}
	if blobs != 1 {
		t.Fatalf("%d blob reads, want 1", blobs)
	}
	if n := env.Count(`SELECT count(*) FROM source_items i JOIN source_versions v ON v.id = i.version_id
		WHERE i.connection_id = $1 AND i.external_id = $2 AND v.body LIKE '%payment reminders by email%'`, conn, want); n != 1 {
		t.Fatal("the item does not have the new version")
	}
	if n := env.Count(`SELECT count(*) FROM source_items i JOIN source_versions v ON v.id = i.version_id
		WHERE i.connection_id = $1 AND i.external_id = $2 AND v.body LIKE '%late payment notices%'`, conn, hookRepo+":catalog-info.yaml"); n != 0 {
		t.Fatal("the job read a file that the push does not name")
	}
	if n := env.Count("SELECT received::int FROM connection_webhooks WHERE connection_id = $1", conn); n != 2 {
		t.Fatalf("%d webhooks counted, want 2", n)
	}
}

func TestSCN_SYN_029_PausedConnectionIgnoresWebhooks(t *testing.T) {
	env, admin, src, conn, path, secret := hookConn(t)
	admin.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/connections/%d/paused", conn), map[string]any{"paused": true})
	body := pushBody("README.md")
	if got := post(t, env, path, body, map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": sign(secret, body)}); got != http.StatusAccepted {
		t.Fatalf("status %d, want 202", got)
	}
	if jobs := itemJobs(t, env, conn); len(jobs) != 0 {
		t.Fatalf("jobs %v for a paused connection", jobs)
	}
	// A job that was queued before the pause reads nothing.
	mark := len(src.Hits())
	if err := env.Server.Sync().SyncItems(context.Background(), conn, []string{hookRepo + ":README.md"}, []string{hookRepo}); err != nil {
		t.Fatal(err)
	}
	if hits := src.Hits()[mark:]; len(hits) != 0 {
		t.Fatalf("a paused connection sent %d requests", len(hits))
	}
}

func TestSCN_SYN_030_WebhookOverTheSizeLimitIsRefused(t *testing.T) {
	env, _, _, conn, path, secret := hookConn(t)
	body := pushBody("README.md", strings.Repeat("a", 1<<20))
	if got := post(t, env, path, body, map[string]string{"X-GitHub-Event": "push", "X-Hub-Signature-256": sign(secret, body)}); got != http.StatusRequestEntityTooLarge {
		t.Fatalf("status %d, want 413", got)
	}
	if jobs := itemJobs(t, env, conn); len(jobs) != 0 {
		t.Fatalf("jobs %v after a refused body", jobs)
	}
}
