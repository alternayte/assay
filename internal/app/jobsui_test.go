package app_test

import (
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_OPS_009_JobQueueIsForAdmins(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	member, _ := env.Invite(admin, "member@acme.test", "member", "member-password-1")
	const jobs = "/admin/jobs/api/jobs?state=available"

	admin.Must(http.StatusOK, http.MethodGet, jobs, nil)
	if r := admin.Must(http.StatusOK, http.MethodGet, "/admin/jobs/", nil); !strings.Contains(string(r.Body), "config__json") {
		t.Fatalf("the page is not River UI: %.200s", r.Body)
	}
	member.Must(http.StatusForbidden, http.MethodGet, jobs, nil)
	env.Client().Must(http.StatusUnauthorized, http.MethodGet, jobs, nil)

	var tok struct {
		Value string `json:"value"`
	}
	admin.Must(http.StatusCreated, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "agent"}).JSON(t, &tok)
	bearer := env.Client()
	bearer.Bearer = tok.Value
	bearer.Must(http.StatusForbidden, http.MethodGet, jobs, nil)

	// A browser without a session goes to sign-in and comes back.
	c := &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	resp, err := c.Get(env.HTTP.URL + "/admin/jobs/")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	if loc := resp.Header.Get("Location"); resp.StatusCode != http.StatusSeeOther || loc != "/sign-in?next=%2Fadmin%2Fjobs%2F" {
		t.Fatalf("anonymous page: %d %q", resp.StatusCode, loc)
	}
}
