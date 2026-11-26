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
	Name string
	Kind string // confluence, jira, github, azdo, gitlab, bitbucket, backstage, web, teams or slack
	// BaseURL is the fixture server URL; Path is added. For teams it is the
	// fake Entra ID tenant of FakeIdentity.
	BaseURL string
	// Path is the API path on the fixture server; empty is /<kind>, or the
	// Slack and Graph API paths.
	Path  string
	Scope map[string]any
	// Options are the connection options, such as the deployment and email
	// of an Atlassian Cloud connection, or historyMonths.
	Options map[string]any
	// Token empty uses the fixture token, the fixture auth header for web or
	// the client secret of the fake tenant for teams.
	Token string
	Rate  float64
	// Code are the code globs. Nil indexes no source code: parsing code is
	// slow under the race detector, and most tests do not read it.
	Code []string
}

// CreateConnection creates a connection as admin and returns its ID.
func CreateConnection(t testing.TB, admin *Client, c Connection) int64 {
	t.Helper()
	if c.Token == "" {
		c.Token = fixtures.SourceToken
		switch c.Kind {
		case "web":
			c.Token = fixtures.WebAuthHeader
		case "teams":
			c.Token = fixtures.IdentityClientSecret
		}
	}
	if c.Rate == 0 {
		c.Rate = 100
	}
	if c.Path == "" {
		c.Path = "/" + c.Kind
		switch c.Kind {
		case "slack":
			c.Path = "/slack/api"
		case "teams":
			c.Path = "/graph/v1.0"
		}
	}
	if c.Kind == "teams" {
		opts := map[string]any{"tenant": fixtures.IdentityTenant, "clientId": fixtures.IdentityClientID}
		for k, v := range c.Options {
			opts[k] = v
		}
		c.Options = opts
	}
	var out struct {
		ID int64 `json:"id"`
	}
	admin.Must(http.StatusCreated, http.MethodPost, "/api/v1/connections", map[string]any{
		"name": c.Name, "kind": c.Kind, "baseUrl": c.BaseURL + c.Path, "token": c.Token, "scope": c.Scope, "options": orObject(c.Options),
		"syncMinutes": 30, "reconcileHours": 24, "rateConcurrency": 4, "ratePerSecond": c.Rate, "codeGlobs": orEmpty(c.Code),
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

// ShareWithEveryone shares a connection with the Everyone group.
func ShareWithEveryone(t testing.TB, env *Env, admin *Client, connectionID int64) {
	t.Helper()
	var everyone int64
	if err := env.Server.Pool().QueryRow(context.Background(), "SELECT id FROM groups WHERE builtin").Scan(&everyone); err != nil {
		t.Fatal(err)
	}
	admin.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/connections/%d/visibility", connectionID),
		map[string]any{"groupIds": []int64{everyone}, "confirm": true})
}

func orObject(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}
