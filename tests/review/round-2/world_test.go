// Package round2 holds the failing tests of review round 2.
package round2

import (
	"context"
	"net/http"
	"strings"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

const canary = "canary-7q4z"

// world has the owner of acme-restricted, a second admin, a reviewer and a
// member. acme-eng is shared with Everyone; acme-restricted is owner-only.
type world struct {
	env                             *testkit.Env
	src                             *fixtures.Sources
	owner, admin2, reviewer, member *testkit.Client
	restricted, public              int64
	admin2ID, reviewerID, mID       string
}

func start(t *testing.T) *world {
	t.Helper()
	env := testkit.Start(t)
	w := &world{env: env, owner: env.Setup("owner@acme.test")}
	w.admin2, w.admin2ID = env.Invite(w.owner, "admin2@acme.test", "admin", "admin2-password-1")
	w.reviewer, w.reviewerID = env.Invite(w.owner, "reviewer@acme.test", "reviewer", "reviewer-password-1")
	w.member, w.mID = env.Invite(w.owner, "member@acme.test", "member", "member-password-1")
	src, base := testkit.FakeSources(t)
	w.src = src
	w.public = testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-eng", Kind: "confluence", BaseURL: base,
		Path: "/confluence", Scope: map[string]any{"spaces": []string{"ENG"}}})
	testkit.ShareWithEveryone(t, env, w.owner, w.public)
	w.restricted = testkit.CreateConnection(t, w.owner, testkit.Connection{Name: "acme-restricted", Kind: "confluence", BaseURL: base,
		Path: "/confluence", Scope: map[string]any{"spaces": []string{"RST"}}})
	for _, id := range []int64{w.public, w.restricted} {
		if r := env.RunNow(w.owner, id, "sync"); r.Status != "succeeded" {
			t.Fatalf("sync %d: %+v", id, r)
		}
	}
	return w
}

func (w *world) scalar(t *testing.T, sql string, args ...any) any {
	t.Helper()
	var v any
	if err := w.env.Server.Pool().QueryRow(context.Background(), sql, args...).Scan(&v); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
	return v
}

func (w *world) exec(t *testing.T, sql string, args ...any) {
	t.Helper()
	if _, err := w.env.Server.Pool().Exec(context.Background(), sql, args...); err != nil {
		t.Fatalf("%s: %v", sql, err)
	}
}

// token creates an access token for the client with the scopes.
func token(t *testing.T, cl *testkit.Client, scopes ...string) string {
	t.Helper()
	var out struct {
		Value string `json:"value"`
	}
	body := map[string]any{"name": "agent"}
	if len(scopes) > 0 {
		body["scopes"] = scopes
	}
	cl.Must(http.StatusCreated, http.MethodPost, "/api/v1/tokens", body).JSON(t, &out)
	return out.Value
}

type bearer string

func (b bearer) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+string(b))
	return http.DefaultTransport.RoundTrip(r)
}

func (w *world) mcp(t *testing.T, value string) *mcp.ClientSession {
	t.Helper()
	c := mcp.NewClient(&mcp.Implementation{Name: "review", Version: "1"}, nil)
	s, err := c.Connect(context.Background(), &mcp.StreamableClientTransport{Endpoint: w.env.HTTP.URL + "/mcp",
		HTTPClient: &http.Client{Transport: bearer(value)}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func hasCanary(body []byte) bool { return strings.Contains(strings.ToLower(string(body)), canary) }

func excerpt(body []byte) string {
	s := string(body)
	i := strings.Index(strings.ToLower(s), canary)
	if i < 0 {
		return ""
	}
	return s[max(0, i-120):min(len(s), i+60)]
}

// key returns the key of the entity of a connection whose key ends with
// :suffix, such as a page ID.
func (w *world) key(t *testing.T, conn int64, suffix string) string {
	t.Helper()
	return w.scalar(t, `SELECT e.key FROM entities e JOIN entity_sources s ON s.entity_id = e.id
		WHERE s.connection_id = $1 AND e.key LIKE '%:' || $2::text`, conn, suffix).(string)
}

// older makes the entity of key older than the entities that the test
// creates, so that it wins a merge.
func (w *world) older(t *testing.T, key string, days int) {
	t.Helper()
	w.exec(t, `UPDATE entities SET created_at = now() - make_interval(days => $2) WHERE key = $1`, key, days)
}
