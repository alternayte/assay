package testkit

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/outbound"
)

// FakeSources starts the fake source APIs and returns them with the base
// URL of the fixture server.
func FakeSources(t testing.TB) (*fixtures.Sources, string) {
	t.Helper()
	src := fixtures.NewSources()
	srv := httptest.NewServer(src)
	t.Cleanup(srv.Close)
	return src, srv.URL
}

// SourceClient returns a connector client for a fake source API.
func SourceClient(t testing.TB, baseURL string) *connectors.Client {
	t.Helper()
	c, err := connectors.NewClient(outbound.Settings{}, connectors.ClientOptions{BaseURL: baseURL,
		Authorization: "Bearer " + fixtures.SourceToken, Concurrency: 4, PerSecond: 1000})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

// DefaultRules are the default connector rules.
var DefaultRules = connectors.Rules{InitiativeTypes: []string{"Epic", "Initiative"}, BlockLinkType: "Blocks",
	DecisionLabels: []string{"decision", "adr"}, GlossaryLabels: []string{"glossary"}}

// Connection is a connection to create in a test.
type Connection struct {
	Name    string
	Kind    string // confluence, jira, github or azdo
	BaseURL string // the fixture server URL; Path is added
	Path    string // the API path on the fixture server; empty is /<kind>
	Scope   map[string]any
	Token   string // empty uses the fixture token
	Rate    float64
}

// CreateConnection creates a connection as admin and returns its ID.
func CreateConnection(t testing.TB, admin *Client, c Connection) int64 {
	t.Helper()
	if c.Token == "" {
		c.Token = fixtures.SourceToken
	}
	if c.Rate == 0 {
		c.Rate = 100
	}
	if c.Path == "" {
		c.Path = "/" + c.Kind
	}
	var out struct {
		ID int64 `json:"id"`
	}
	admin.Must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{
		"name": c.Name, "kind": c.Kind, "baseUrl": c.BaseURL + c.Path, "token": c.Token, "scope": c.Scope,
		"syncMinutes": 30, "reconcileHours": 24, "rateConcurrency": 4, "ratePerSecond": c.Rate,
	}).JSON(t, &out)
	return out.ID
}

// Run is a finished sync run as the API shows it.
type Run struct {
	ID        int64  `json:"id"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
	Seen      int    `json:"seen"`
	Created   int    `json:"created"`
	Updated   int    `json:"updated"`
	Unchanged int    `json:"unchanged"`
	Deleted   int    `json:"deleted"`
	Skipped   []struct {
		ID     string `json:"id"`
		Reason string `json:"reason"`
	} `json:"skipped"`
	Error string `json:"error"`
}

// RunNow queues a run of kind (sync or reconcile) through the API, works it
// in the test and returns it.
func (e *Env) RunNow(admin *Client, connectionID int64, kind string) Run {
	e.t.Helper()
	var q struct {
		RunID int64 `json:"runId"`
	}
	admin.Must(http.StatusAccepted, http.MethodPost, fmt.Sprintf("/api/v1/connections/%d/%s", connectionID, kind), nil).JSON(e.t, &q)
	if err := e.Server.Sync().Run(context.Background(), q.RunID); err != nil {
		e.t.Fatalf("run %d: %v", q.RunID, err)
	}
	var runs struct {
		Runs []Run `json:"runs"`
	}
	admin.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/connections/%d/runs", connectionID), nil).JSON(e.t, &runs)
	for _, r := range runs.Runs {
		if r.ID == q.RunID {
			return r
		}
	}
	e.t.Fatalf("run %d is not in the list", q.RunID)
	return Run{}
}

// Count returns the result of a count query.
func (e *Env) Count(query string, args ...any) int {
	e.t.Helper()
	var n int
	if err := e.Server.Pool().QueryRow(context.Background(), query, args...).Scan(&n); err != nil {
		e.t.Fatalf("%s: %v", query, err)
	}
	return n
}
