package users_test

import (
	"net/http"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alternayte/auth-all/ratelimit"

	"github.com/alternayte/assay/internal/testkit"
)

type user struct {
	ID     string `json:"id"`
	Email  string `json:"email"`
	Role   string `json:"role"`
	Status string `json:"status"`
}

func TestSCN_USR_004_ExpiredInviteFails(t *testing.T) {
	var offset atomic.Int64
	clock := func() time.Time { return time.Now().Add(time.Duration(offset.Load())) }
	env := testkit.Start(t, testkit.WithClock(clock))
	admin := env.Setup("owner@acme.test")

	var inv struct {
		URL string `json:"url"`
	}
	admin.Must(http.StatusCreated, http.MethodPost, "/api/v1/invites", map[string]string{"email": "late@acme.test", "role": "member"}).JSON(t, &inv)
	token := strings.TrimPrefix(inv.URL, testkit.BaseURL+"/invite#")

	offset.Store(int64(8 * 24 * time.Hour))
	anon := env.Client()
	r := anon.Must(http.StatusNotFound, http.MethodPost, "/api/v1/invites/lookup", map[string]string{"token": token})
	if r.Code() != "invite_invalid" {
		t.Fatalf("lookup code %q", r.Code())
	}
	r = anon.Must(http.StatusNotFound, http.MethodPost, "/api/v1/invites/accept", map[string]string{"token": token, "name": "Late", "password": "late-password-1"})
	if r.Code() != "invite_invalid" {
		t.Fatalf("accept code %q", r.Code())
	}
	anon.Must(http.StatusUnauthorized, http.MethodPost, "/api/auth/sign-in/email", map[string]string{"email": "late@acme.test", "password": "late-password-1"})
}

func TestSCN_USR_006_ResetLinkAndSessionRevoke(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	member, id := env.Invite(admin, "member@acme.test", "member", "old-password-1")

	var link struct {
		URL string `json:"url"`
	}
	admin.Must(http.StatusCreated, http.MethodPost, "/api/v1/users/"+id+"/reset-link", nil).JSON(t, &link)
	token := strings.TrimPrefix(link.URL, testkit.BaseURL+"/reset-password#")

	anon := env.Client()
	anon.Must(http.StatusNoContent, http.MethodPost, "/api/v1/password-resets/accept", map[string]string{"token": token, "password": "new-password-1"})
	r := anon.Must(http.StatusNotFound, http.MethodPost, "/api/v1/password-resets/accept", map[string]string{"token": token, "password": "other-password-1"})
	if r.Code() != "reset_invalid" {
		t.Fatalf("second use code %q", r.Code())
	}
	// The reset ends the old session.
	member.Must(http.StatusUnauthorized, http.MethodGet, "/api/v1/me", nil)

	env.Client().Must(http.StatusUnauthorized, http.MethodPost, "/api/auth/sign-in/email", map[string]string{"email": "member@acme.test", "password": "old-password-1"})
	a := env.Client()
	a.Must(http.StatusOK, http.MethodPost, "/api/auth/sign-in/email", map[string]string{"email": "member@acme.test", "password": "new-password-1"})
	b := env.Client()
	b.Must(http.StatusOK, http.MethodPost, "/api/auth/sign-in/email", map[string]string{"email": "member@acme.test", "password": "new-password-1"})

	// The user revokes the session of b from a.
	var sessions struct {
		Sessions []struct {
			ID string `json:"id"`
		} `json:"sessions"`
	}
	a.Must(http.StatusOK, http.MethodGet, "/api/auth/sessions", nil).JSON(t, &sessions)
	if len(sessions.Sessions) != 2 {
		t.Fatalf("want 2 sessions, got %d", len(sessions.Sessions))
	}
	// The newest session is b.
	a.Must(http.StatusOK, http.MethodDelete, "/api/auth/sessions/"+sessions.Sessions[0].ID, nil)
	b.Must(http.StatusUnauthorized, http.MethodGet, "/api/v1/me", nil)
	a.Must(http.StatusOK, http.MethodGet, "/api/v1/me", nil)
}

func TestSCN_USR_007_LastAdminGuard(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	admin2, id2 := env.Invite(admin, "admin2@acme.test", "admin", "admin2-password-1")

	var me user
	admin.Must(http.StatusOK, http.MethodGet, "/api/v1/me", nil).JSON(t, &me)

	// With two admins, one admin can lose the role.
	admin.Must(http.StatusOK, http.MethodPut, "/api/v1/users/"+id2+"/role", map[string]string{"role": "member"})

	r := admin.Must(http.StatusConflict, http.MethodPut, "/api/v1/users/"+me.ID+"/role", map[string]string{"role": "reviewer"})
	if r.Code() != "last_admin" {
		t.Fatalf("demote code %q", r.Code())
	}
	admin2.Must(http.StatusForbidden, http.MethodPost, "/api/v1/users/"+me.ID+"/deactivate", nil)

	admin.Must(http.StatusOK, http.MethodPut, "/api/v1/users/"+id2+"/role", map[string]string{"role": "admin"})
	admin2 = env.Client()
	admin2.SignIn("admin2@acme.test", "admin2-password-1")
	admin2.Must(http.StatusOK, http.MethodPost, "/api/v1/users/"+me.ID+"/deactivate", nil)
	r = admin2.Must(http.StatusForbidden, http.MethodPost, "/api/v1/users/"+id2+"/deactivate", nil)
	if r.Code() != "own_account" {
		t.Fatalf("self deactivate code %q", r.Code())
	}
	admin2.Must(http.StatusOK, http.MethodPost, "/api/v1/users/"+me.ID+"/reactivate", nil)
	// Now deactivating the other admin works, and the remaining one is last.
	admin2.Must(http.StatusOK, http.MethodPost, "/api/v1/users/"+me.ID+"/deactivate", nil)
	r = admin2.Must(http.StatusConflict, http.MethodPut, "/api/v1/users/"+id2+"/role", map[string]string{"role": "member"})
	if r.Code() != "last_admin" {
		t.Fatalf("last admin demote code %q", r.Code())
	}
}

func TestSCN_USR_010_SignInRateLimit(t *testing.T) {
	env := testkit.Start(t, testkit.WithSignInRules([]ratelimit.Rule{
		{Operation: ratelimit.OpSignIn, Scope: ratelimit.ScopeEmail, Limit: 3, Window: time.Minute},
	}))
	env.Setup("owner@acme.test")
	c := env.Client()
	for i := range 3 {
		if r := c.SignIn("guess@acme.test", "wrong-password"); r.Status != http.StatusUnauthorized {
			t.Fatalf("attempt %d: status %d", i+1, r.Status)
		}
	}
	r := c.SignIn("guess@acme.test", "wrong-password")
	if r.Status != http.StatusTooManyRequests || r.Header.Get("Retry-After") == "" {
		t.Fatalf("want 429 with Retry-After, got %d %v", r.Status, r.Header)
	}
}

func TestSCN_USR_011_CookieFlagsAndOriginCheck(t *testing.T) {
	env := testkit.Start(t, testkit.WithBaseURL("https://assay.test"))
	c := env.Client()
	c.Must(http.StatusCreated, http.MethodPost, "/api/v1/setup", map[string]string{
		"token": testkit.SetupToken, "email": "owner@acme.test", "name": "Owner", "password": testkit.AdminPassword,
	})
	r := c.Must(http.StatusOK, http.MethodPost, "/api/auth/sign-in/email", map[string]string{"email": "owner@acme.test", "password": testkit.AdminPassword})
	cookie := r.Header.Get("Set-Cookie")
	for _, want := range []string{"assay_session=", "HttpOnly", "Secure", "SameSite=Lax"} {
		if !strings.Contains(cookie, want) {
			t.Fatalf("cookie %q lacks %q", cookie, want)
		}
	}
	session := strings.SplitN(strings.SplitN(cookie, ";", 2)[0], "=", 2)[1]

	// The test server is plain HTTP, so the jar keeps no Secure cookie. Send
	// the cookie by hand.
	send := func(origin string) int {
		req, _ := http.NewRequest(http.MethodPost, env.HTTP.URL+"/api/v1/groups", strings.NewReader(`{"name":"g"}`))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Cookie", "assay_session="+session)
		req.Header.Set("Origin", origin)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_ = resp.Body.Close()
		return resp.StatusCode
	}
	if got := send("https://evil.test"); got != http.StatusForbidden {
		t.Fatalf("foreign origin: status %d, want 403", got)
	}
	if got := send("https://assay.test"); got != http.StatusCreated {
		t.Fatalf("own origin: status %d, want 201", got)
	}
}

func TestSCN_USR_012_EveryoneGroup(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	_, memberID := env.Invite(admin, "member@acme.test", "member", "member-password-1")

	var groups struct {
		Groups []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Builtin     bool   `json:"builtin"`
			MemberCount int64  `json:"memberCount"`
		} `json:"groups"`
	}
	admin.Must(http.StatusOK, http.MethodGet, "/api/v1/groups", nil).JSON(t, &groups)
	everyone := groups.Groups[0]
	if everyone.Name != "Everyone" || !everyone.Builtin {
		t.Fatalf("first group %+v", everyone)
	}
	members := func() map[string]bool {
		var m struct {
			Members []struct {
				ID string `json:"id"`
			} `json:"members"`
		}
		admin.Must(http.StatusOK, http.MethodGet, "/api/v1/groups/"+itoa(everyone.ID)+"/members", nil).JSON(t, &m)
		out := map[string]bool{}
		for _, x := range m.Members {
			out[x.ID] = true
		}
		return out
	}
	if !members()[memberID] {
		t.Fatal("a new active user is not in Everyone")
	}
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/users/"+memberID+"/deactivate", nil)
	if members()[memberID] {
		t.Fatal("a deactivated user is in Everyone")
	}

	path := "/api/v1/groups/" + itoa(everyone.ID)
	for _, r := range []*testkit.Response{
		admin.Do(http.MethodPatch, path, map[string]string{"name": "All"}),
		admin.Do(http.MethodDelete, path, nil),
		admin.Do(http.MethodPut, path+"/members/"+memberID, nil),
	} {
		if r.Status != http.StatusForbidden || r.Code() != "builtin_group" {
			t.Fatalf("edit of Everyone: %d %s", r.Status, r.Body)
		}
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
