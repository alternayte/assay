package sso_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/testkit"
)

type world struct {
	env   *testkit.Env
	idp   *fixtures.Identity
	admin *testkit.Client
}

func start(t *testing.T, opts ...testkit.Option) *world {
	t.Helper()
	idp, base := testkit.FakeIdentity(t)
	env := testkit.Start(t, append([]testkit.Option{testkit.WithIdentity(base)}, opts...)...)
	return &world{env: env, idp: idp, admin: env.Setup("owner@acme.test")}
}

// group creates a group, maps it to an IdP group and returns its ID.
func (w *world) group(t *testing.T, name, externalID string) int64 {
	t.Helper()
	var g struct {
		ID int64 `json:"id"`
	}
	w.admin.Must(http.StatusCreated, http.MethodPost, "/api/v1/groups", map[string]string{"name": name}).JSON(t, &g)
	w.admin.Must(http.StatusNoContent, http.MethodPut, "/api/v1/groups/"+itoa(g.ID)+"/mapping",
		map[string]string{"externalId": externalID, "externalName": name})
	return g.ID
}

// members returns the emails of the members of a group.
func (w *world) members(t *testing.T, id int64) []string {
	t.Helper()
	var out struct {
		Members []struct {
			Email string `json:"email"`
		} `json:"members"`
	}
	w.admin.Must(http.StatusOK, http.MethodGet, "/api/v1/groups/"+itoa(id)+"/members", nil).JSON(t, &out)
	emails := []string{}
	for _, m := range out.Members {
		emails = append(emails, m.Email)
	}
	return emails
}

// setSignIn changes fields of the stored sign-in setting.
func (w *world) setSignIn(t *testing.T, change map[string]any) *testkit.Response {
	t.Helper()
	var list struct {
		Settings []struct {
			Key      string          `json:"key"`
			Value    json.RawMessage `json:"value"`
			Revision int32           `json:"revision"`
		} `json:"settings"`
	}
	w.admin.Must(http.StatusOK, http.MethodGet, "/api/v1/settings", nil).JSON(t, &list)
	for _, s := range list.Settings {
		if s.Key != "signin" {
			continue
		}
		v := map[string]any{}
		if err := json.Unmarshal(s.Value, &v); err != nil {
			t.Fatal(err)
		}
		for k, x := range change {
			v[k] = x
		}
		return w.admin.Do(http.MethodPut, "/api/v1/settings/signin", map[string]any{"value": v, "revision": s.Revision})
	}
	t.Fatal("no signin setting")
	return nil
}

func (w *world) poll(t *testing.T) {
	t.Helper()
	if err := w.env.Server.SSO().Sync(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	var st struct {
		Directory struct {
			LastError string `json:"lastError"`
		} `json:"directory"`
	}
	w.admin.Must(http.StatusOK, http.MethodGet, "/api/v1/sso", nil).JSON(t, &st)
	if st.Directory.LastError != "" {
		t.Fatalf("poll failed: %s", st.Directory.LastError)
	}
}

func userID(t *testing.T, c *testkit.Client) string {
	t.Helper()
	var me struct {
		ID   string `json:"id"`
		Role string `json:"role"`
	}
	c.Must(http.StatusOK, http.MethodGet, "/api/v1/me", nil).JSON(t, &me)
	return me.ID
}

func mcp(t *testing.T, env *testkit.Env, token string) int {
	t.Helper()
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodPost, env.HTTP.URL+"/mcp",
		bytes.NewBufferString(`{"jsonrpc":"2.0","id":1,"method":"tools/list"}`))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	r, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, r.Body)
	_ = r.Body.Close()
	return r.StatusCode
}

func TestSCN_USR_013_SSOSignInCreatesUserInMappedGroups(t *testing.T) {
	w := start(t)
	payments := w.group(t, "Payments", fixtures.GroupPayments)
	eng := w.group(t, "Engineering", fixtures.GroupEng)
	platform := w.group(t, "Platform", fixtures.GroupPlatform)
	if r := w.setSignIn(t, map[string]any{"accessGroup": fixtures.GroupStaff, "accessGroupName": "Acme Staff"}); r.Status != http.StatusOK {
		t.Fatalf("set the access group: %d %s", r.Status, r.Body)
	}

	ada, landing := w.env.SignInSSO("ada@acme.test", "/explore")
	if landing != "/explore" {
		t.Fatalf("ada lands on %q, want /explore", landing)
	}
	var me struct {
		Email string `json:"email"`
		Role  string `json:"role"`
	}
	ada.Must(http.StatusOK, http.MethodGet, "/api/v1/me", nil).JSON(t, &me)
	if me.Email != "ada@acme.test" || me.Role != "member" {
		t.Fatalf("me = %+v, want ada as member", me)
	}
	// Engineering holds Payments, so the groups claim names both.
	if got := w.members(t, payments); len(got) != 1 || got[0] != "ada@acme.test" {
		t.Fatalf("Payments members = %v", got)
	}
	if got := w.members(t, eng); len(got) != 1 {
		t.Fatalf("Engineering members = %v", got)
	}
	if got := w.members(t, platform); len(got) != 0 {
		t.Fatalf("Platform members = %v", got)
	}

	// A second sign-in links to the same user.
	again, _ := w.env.SignInSSO("ada@acme.test", "/explore")
	if userID(t, again) != userID(t, ada) {
		t.Fatal("the second sign-in made another user")
	}

	// Kim is not in Acme Staff: refused, and no user exists.
	_, landing = w.env.SignInSSO("kim@acme.test", "/explore")
	if !strings.Contains(landing, "error=SSO_ACCESS_DENIED") {
		t.Fatalf("kim lands on %q, want the access error", landing)
	}
	var users struct {
		Users []struct {
			Email string `json:"email"`
		} `json:"users"`
	}
	w.admin.Must(http.StatusOK, http.MethodGet, "/api/v1/users", nil).JSON(t, &users)
	for _, u := range users.Users {
		if u.Email == "kim@acme.test" {
			t.Fatal("a refused sign-in created a user")
		}
	}

	// The IdP sets the members of a mapped group. A manual change is refused.
	adaID := userID(t, ada)
	if r := w.admin.Do(http.MethodDelete, "/api/v1/groups/"+itoa(payments)+"/members/"+adaID, nil); r.Status != http.StatusConflict || r.Code() != "mapped_group" {
		t.Fatalf("remove from a mapped group: %d %s", r.Status, r.Body)
	}
	if r := w.admin.Do(http.MethodPut, "/api/v1/groups/"+itoa(platform)+"/members/"+adaID, nil); r.Status != http.StatusConflict || r.Code() != "mapped_group" {
		t.Fatalf("add to a mapped group: %d %s", r.Status, r.Body)
	}
}

func TestSCN_USR_014_DirectoryPollDisablesLeavers(t *testing.T) {
	w := start(t)
	payments := w.group(t, "Payments", fixtures.GroupPayments)
	ada, _ := w.env.SignInSSO("ada@acme.test", "/explore")
	grace, _ := w.env.SignInSSO("grace@acme.test", "/explore")
	linus, _ := w.env.SignInSSO("linus@acme.test", "/explore")
	linusID := userID(t, linus)
	var tok struct {
		Value string `json:"value"`
	}
	ada.Must(http.StatusCreated, http.MethodPost, "/api/v1/tokens", map[string]any{"name": "agent"}).JSON(t, &tok)
	w.poll(t)
	if code := mcp(t, w.env, tok.Value); code != http.StatusOK {
		t.Fatalf("MCP before the leave: %d", code)
	}

	// Ada leaves: the next poll disables her. Her session and her access
	// token stop working.
	w.idp.SetEnabled(fixtures.UserAda, false)
	w.poll(t)
	if r := ada.Do(http.MethodGet, "/api/v1/me", nil); r.Status != http.StatusUnauthorized {
		t.Fatalf("session after the leave: %d", r.Status)
	}
	if code := mcp(t, w.env, tok.Value); code != http.StatusUnauthorized {
		t.Fatalf("MCP after the leave: %d", code)
	}

	// Ada returns: the poll enables her again.
	w.idp.SetEnabled(fixtures.UserAda, true)
	w.poll(t)
	if _, landing := w.env.SignInSSO("ada@acme.test", "/explore"); landing != "/explore" {
		t.Fatalf("ada after the return lands on %q", landing)
	}

	// An admin disables Grace. A poll that sees her enabled keeps her
	// disabled.
	graceID := userID(t, grace)
	w.admin.Must(http.StatusOK, http.MethodPost, "/api/v1/users/"+graceID+"/deactivate", nil)
	w.idp.SetEnabled(fixtures.UserGrace, false)
	w.poll(t)
	w.idp.SetEnabled(fixtures.UserGrace, true)
	w.poll(t)
	if got := w.status(t, graceID); got != "deactivated" {
		t.Fatalf("grace after the polls: %q, want deactivated", got)
	}

	// Linus leaves Payments in the directory: the poll removes him.
	if got := w.members(t, payments); len(got) != 2 {
		t.Fatalf("Payments members = %v", got)
	}
	w.idp.SetMember(fixtures.GroupPayments, fixtures.UserLinus, false)
	w.poll(t)
	if got := w.members(t, payments); len(got) != 1 || got[0] != "ada@acme.test" {
		t.Fatalf("Payments members after the leave = %v", got)
	}

	// A deleted user is disabled too.
	w.idp.Delete(fixtures.UserLinus)
	w.poll(t)
	if got := w.status(t, linusID); got != "deactivated" {
		t.Fatalf("linus after the delete: %q, want deactivated", got)
	}
	if got := w.status(t, userID(t, w.admin)); got != "active" {
		t.Fatalf("the password admin after the polls: %q", got)
	}
}

// status returns the status of a user as admins see it.
func (w *world) status(t *testing.T, id string) string {
	t.Helper()
	var list struct {
		Users []struct {
			ID     string `json:"id"`
			Status string `json:"status"`
		} `json:"users"`
	}
	w.admin.Must(http.StatusOK, http.MethodGet, "/api/v1/users", nil).JSON(t, &list)
	for _, u := range list.Users {
		if u.ID == id {
			return u.Status
		}
	}
	t.Fatalf("no user %s", id)
	return ""
}

func itoa(n int64) string {
	b, _ := json.Marshal(n)
	return string(b)
}
