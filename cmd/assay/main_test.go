package main

import (
	"bytes"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_USR_009_AdminCommands(t *testing.T) {
	db := testkit.DB(t)
	env := []string{
		"ASSAY_DATABASE_URL=" + db,
		"ASSAY_SECRET_KEY=" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
		"ASSAY_BASE_URL=" + testkit.BaseURL,
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"admin", "create", "--email", "ops@acme.test", "--name", "Ops"}, env, &out, &errOut); code != 0 {
		t.Fatalf("admin create: exit %d: %s", code, errOut.String())
	}
	first := generatedPassword(t, out.String())

	srv := testkit.StartOn(t, db)
	c := srv.Client()
	c.Must(http.StatusOK, http.MethodPost, "/api/auth/sign-in/email", map[string]string{"email": "ops@acme.test", "password": first})
	var me struct {
		Role string `json:"role"`
	}
	c.Must(http.StatusOK, http.MethodGet, "/api/v1/me", nil).JSON(t, &me)
	if me.Role != "admin" {
		t.Fatalf("role %q", me.Role)
	}

	out.Reset()
	if code := run([]string{"admin", "reset-password", "--email", "ops@acme.test"}, env, &out, &errOut); code != 0 {
		t.Fatalf("admin reset-password: exit %d: %s", code, errOut.String())
	}
	second := generatedPassword(t, out.String())
	c.Must(http.StatusUnauthorized, http.MethodGet, "/api/v1/me", nil)
	srv.Client().Must(http.StatusUnauthorized, http.MethodPost, "/api/auth/sign-in/email", map[string]string{"email": "ops@acme.test", "password": first})
	srv.Client().Must(http.StatusOK, http.MethodPost, "/api/auth/sign-in/email", map[string]string{"email": "ops@acme.test", "password": second})
}

func generatedPassword(t *testing.T, out string) string {
	t.Helper()
	_, p, ok := strings.Cut(strings.TrimSpace(out), "): ")
	if !ok || p == "" {
		t.Fatalf("no generated password in %q", out)
	}
	return p
}
