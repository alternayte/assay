package visibility_test

import (
	"fmt"
	"net/http"
	"slices"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
	"github.com/alternayte/assay/internal/visibility"
)

// TestVisibleSetRule checks the rule of SDD section 9 on each path: owner,
// a shared group, Everyone, a paused connection, a deactivated user and an
// admin without a share.
func TestVisibleSetRule(t *testing.T) {
	env := testkit.Start(t)
	owner := env.Setup("owner@acme.test")
	_, admin2ID := env.Invite(owner, "admin2@acme.test", "admin", "admin2-password-1")
	_, memberID := env.Invite(owner, "member@acme.test", "member", "member-password-1")
	_, otherID := env.Invite(owner, "other@acme.test", "member", "other-password-1")
	var me struct {
		ID string `json:"id"`
	}
	owner.Must(http.StatusOK, http.MethodGet, "/api/v1/me", nil).JSON(t, &me)
	_, base := testkit.FakeSources(t)
	mk := func(name string) int64 {
		return testkit.CreateConnection(t, owner, testkit.Connection{Name: name, Kind: "confluence", BaseURL: base,
			Scope: map[string]any{"spaces": []string{"ENG"}}})
	}
	restricted, team, public, paused := mk("restricted"), mk("team"), mk("public"), mk("paused")

	var g struct {
		ID int64 `json:"id"`
	}
	owner.Must(http.StatusCreated, http.MethodPost, "/api/v1/groups", map[string]string{"name": "Payments"}).JSON(t, &g)
	owner.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/groups/%d/members/%s", g.ID, memberID), nil)
	var everyone int64
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT id FROM groups WHERE builtin").Scan(&everyone); err != nil {
		t.Fatal(err)
	}
	share := func(conn int64, groups ...int64) {
		owner.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/connections/%d/visibility", conn),
			map[string]any{"groupIds": groups, "confirm": true})
	}
	share(team, g.ID)
	share(public, everyone)
	share(paused, everyone)
	owner.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/connections/%d/paused", paused), map[string]any{"paused": true})
	var registry int64
	if err := env.Server.Pool().QueryRow(t.Context(), "SELECT id FROM connections WHERE kind = 'registry'").Scan(&registry); err != nil {
		t.Fatal(err)
	}

	set := func(user string) visibility.Set {
		s, err := visibility.For(t.Context(), env.Server.Pool(), user)
		if err != nil {
			t.Fatal(err)
		}
		return s
	}
	for _, c := range []struct {
		name string
		user string
		want []int64
	}{
		{"owner", me.ID, []int64{registry, restricted, team, public}},
		{"group member", memberID, []int64{registry, team, public}},
		{"other member", otherID, []int64{registry, public}},
		{"admin without a share", admin2ID, []int64{registry, public}},
		{"unknown user", "00000000-0000-0000-0000-000000000000", []int64{}},
	} {
		got := set(c.user)
		slices.Sort(c.want)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: %v, want %v", c.name, got, c.want)
		}
	}
	owner.Must(http.StatusOK, http.MethodPost, "/api/v1/users/"+memberID+"/deactivate", nil)
	if got := set(memberID); len(got) != 0 {
		t.Errorf("deactivated member: %v", got)
	}
}
