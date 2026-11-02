package fixtures

import (
	"net/http"

	"github.com/alternayte/assay/internal/connectors/tree"
)

// ManifestConnection is one Acme connection of the demo.
type ManifestConnection struct {
	Name  string         `json:"name"`
	Kind  string         `json:"kind"`
	Path  string         `json:"path" doc:"The path of the fake API below the fixture server URL."`
	Scope map[string]any `json:"scope"`
	// Shared connections are shared with Everyone. Others belong to Owner.
	Shared bool   `json:"shared"`
	Owner  string `json:"owner,omitempty"`
	Items  int    `json:"items"`
}

// Manifest describes the Acme corpus: its connections, their item counts,
// the canary of restricted content and the labels.
type Manifest struct {
	Canary      string               `json:"canary"`
	Connections []ManifestConnection `json:"connections"`
	Labels      *Labels              `json:"labels,omitempty"`
}

// Manifest returns the manifest of the current corpus.
func (s *Sources) Manifest() Manifest {
	return Manifest{Canary: Canary, Connections: []ManifestConnection{
		{Name: "acme-public", Kind: "confluence", Path: "/confluence", Scope: map[string]any{"spaces": []string{"ENG"}}, Shared: true, Items: s.Count("ENG")},
		{Name: "acme-jira", Kind: "jira", Path: "/jira", Scope: map[string]any{"projects": []string{"PAY", "REM"}}, Shared: true,
			Items: s.Count("PAY") + s.Count("REM")},
		{Name: "acme-github", Kind: "github", Path: "/github", Scope: map[string]any{"repositories": GitHubRepos},
			Shared: true, Items: s.githubItems()},
		{Name: "acme-azdo", Kind: "azdo", Path: "/azdo/" + AzdoCollection, Scope: map[string]any{"repositories": []string{"Platform/ledger-service"}},
			Shared: true, Items: s.RepoItems("azdo", "Platform/ledger-service") + s.RepoCodeItems("azdo", "Platform/ledger-service")},
		{Name: "acme-restricted", Kind: "confluence", Path: "/confluence", Scope: map[string]any{"spaces": []string{"RST"}},
			Owner: "owner@acme.test", Items: s.Count("RST")},
	}}
}

func (s *Sources) serveManifest(w http.ResponseWriter, r *http.Request) {
	m := s.Manifest()
	l := CorpusLabels(r.Host)
	m.Labels = &l
	writeJSON(w, http.StatusOK, m)
}

// GitHubRepos are the repositories of the acme-github connection of the
// demo. payments-worker, checkout-web and fraud-scoring have no catalog
// data.
var GitHubRepos = []string{"acme/payments-api", "acme/reminder-service", "acme/payments-worker", "acme/checkout-web", "acme/fraud-scoring"}

func (s *Sources) githubItems() int {
	n := 0
	for _, r := range GitHubRepos {
		n += s.RepoItems("github", r) + s.RepoCodeItems("github", r)
	}
	return n
}

// RepoItems is the number of files of a repository that match the default
// globs: the knowledge files.
func (s *Sources) RepoItems(host, repo string) int {
	g := tree.Globs{Include: tree.DefaultGlobs}
	n := 0
	for _, p := range s.RepoFiles(host, repo) {
		if g.Match(p) {
			n++
		}
	}
	return n
}

// RepoCodeItems is the number of source files of a repository that the
// default code globs select.
func (s *Sources) RepoCodeItems(host, repo string) int {
	g := tree.Globs{Include: tree.DefaultGlobs, Code: tree.DefaultCodeGlobs, Skip: tree.DefaultSkipGlobs}
	n := 0
	for _, p := range s.RepoFiles(host, repo) {
		if g.MatchCode(p) {
			n++
		}
	}
	return n
}
