package testkit

import (
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
