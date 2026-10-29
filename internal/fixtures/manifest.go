package fixtures

import "net/http"

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

// Manifest describes the Acme corpus: its connections, their item counts
// and the canary of restricted content.
type Manifest struct {
	Canary      string               `json:"canary"`
	Connections []ManifestConnection `json:"connections"`
}

// Manifest returns the manifest of the current corpus.
func (s *Sources) Manifest() Manifest {
	return Manifest{Canary: Canary, Connections: []ManifestConnection{
		{Name: "acme-public", Kind: "confluence", Path: "/confluence", Scope: map[string]any{"spaces": []string{"ENG"}}, Shared: true, Items: s.Count("ENG")},
		{Name: "acme-jira", Kind: "jira", Path: "/jira", Scope: map[string]any{"projects": []string{"PAY", "REM"}}, Shared: true,
			Items: s.Count("PAY") + s.Count("REM")},
		{Name: "acme-restricted", Kind: "confluence", Path: "/confluence", Scope: map[string]any{"spaces": []string{"RST"}},
			Owner: "owner@acme.test", Items: s.Count("RST")},
	}}
}

func (s *Sources) serveManifest(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.Manifest())
}
