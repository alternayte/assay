package sync_test

import (
	"context"
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/testkit"
)

func TestDemoCreatesAcmeConnections(t *testing.T) {
	_, base := testkit.FakeSources(t)
	env := testkit.Start(t, func(c *app.Config, _ *app.Options) { c.Demo, c.DemoURL = true, base })
	owner := env.Setup("owner@acme.test")
	var list struct {
		Connections []struct {
			ID         int64  `json:"id"`
			Kind       string `json:"kind"`
			Name       string `json:"name"`
			Visibility []struct {
				Name string `json:"name"`
			} `json:"visibility"`
		} `json:"connections"`
	}
	owner.Must(http.StatusOK, http.MethodGet, "/api/v1/connections", nil).JSON(t, &list)
	var names []string
	for _, c := range list.Connections {
		if c.Kind == "registry" {
			continue
		}
		names = append(names, c.Name)
		shared := len(c.Visibility) > 0
		if shared == (c.Name == "acme-restricted") {
			t.Errorf("%s: visibility %+v", c.Name, c.Visibility)
		}
		// Setup queued the first sync. The test works it.
		var runs struct {
			Runs []testkit.Run `json:"runs"`
		}
		owner.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/connections/%d/runs", c.ID), nil).JSON(t, &runs)
		if len(runs.Runs) != 1 || runs.Runs[0].Kind != "sync" {
			t.Fatalf("%s runs: %+v", c.Name, runs.Runs)
		}
		if err := env.Server.Sync().Run(context.Background(), runs.Runs[0].ID); err != nil {
			t.Fatalf("sync %s: %v", c.Name, err)
		}
		owner.Must(http.StatusOK, http.MethodGet, fmt.Sprintf("/api/v1/connections/%d/runs", c.ID), nil).JSON(t, &runs)
		if r := runs.Runs[0]; r.Status != "succeeded" || r.Created == 0 {
			t.Errorf("sync %s: %+v", c.Name, r)
		}
	}
	slices.Sort(names)
	want := []string{"acme-azdo", "acme-engineering", "acme-github", "acme-jira", "acme-restricted"}
	if !slices.Equal(names, want) {
		t.Fatalf("connections %v, want %v", names, want)
	}
}
