package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"net/http"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/secrets"
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

func TestSCN_SET_003_Rekey(t *testing.T) {
	db := testkit.DB(t)
	keyA := bytes.Repeat([]byte{7}, 32)
	keyB := bytes.Repeat([]byte{9}, 32)
	srv := testkit.StartOn(t, db)
	pool := srv.Server.Pool()
	boxA, _ := secrets.NewBox(keyA)
	boxB, _ := secrets.NewBox(keyB)
	storeA := &secrets.Store{Pool: pool, Box: boxA}
	storeB := &secrets.Store{Pool: pool, Box: boxB}
	ctx := context.Background()
	for _, n := range []string{"one", "two", "three"} {
		if _, err := storeA.Put(ctx, pool, n, []byte("value-"+n)); err != nil {
			t.Fatal(err)
		}
	}

	env := []string{
		"ASSAY_DATABASE_URL=" + db,
		"ASSAY_SECRET_KEY=" + base64.StdEncoding.EncodeToString(keyA),
		"ASSAY_SECRET_KEY_NEW=" + base64.StdEncoding.EncodeToString(keyB),
	}
	var out, errOut bytes.Buffer
	if code := run([]string{"secrets", "rekey"}, env, &out, &errOut); code != 0 {
		t.Fatalf("rekey: exit %d: %s", code, errOut.String())
	}
	if !strings.Contains(out.String(), "Encrypted 3 secrets") {
		t.Fatalf("output %q", out.String())
	}
	for _, n := range []string{"one", "two", "three"} {
		v, err := storeB.Get(ctx, n)
		if err != nil || string(v) != "value-"+n {
			t.Fatalf("%s with key B: %q %v", n, v, err)
		}
		if _, err := storeA.Get(ctx, n); err == nil {
			t.Fatalf("%s still decrypts with key A", n)
		}
	}
}

func TestSCN_INS_007_Doctor(t *testing.T) {
	db := testkit.DB(t)
	srv := testkit.StartOn(t, db)
	admin := srv.Setup("owner@acme.test")
	_, openaiURL, _ := testkit.FakeModels(t)
	testkit.CreateEndpoint(t, admin, "Reachable", "openai", openaiURL, fixtures.FakeKey, nil)
	testkit.CreateEndpoint(t, admin, "Unreachable", "openai", "http://127.0.0.1:1/v1", "", nil)

	env := []string{
		"ASSAY_DATABASE_URL=" + db,
		"ASSAY_SECRET_KEY=" + base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{7}, 32)),
	}
	var out, errOut bytes.Buffer
	code := run([]string{"doctor"}, env, &out, &errOut)
	if code == 0 {
		t.Fatalf("doctor exits 0 with an unreachable endpoint:\n%s", out.String())
	}
	text := out.String()
	for _, want := range []string{"PASS  Database version", "PASS  Database extensions", "PASS  Secret key", "PASS  Endpoint Reachable", "FAIL  Endpoint Unreachable"} {
		if !strings.Contains(text, want) {
			t.Fatalf("doctor output lacks %q:\n%s", want, text)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "FAIL  Endpoint Unreachable") && !strings.Contains(line, "connection refused") {
			t.Fatalf("the failure names no cause: %s", line)
		}
	}

	var api struct {
		OK     bool `json:"ok"`
		Checks []struct {
			Name string `json:"name"`
			OK   bool   `json:"ok"`
		} `json:"checks"`
	}
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/diagnostics", nil).JSON(t, &api)
	if api.OK || len(api.Checks) < 6 {
		t.Fatalf("diagnostics route: %+v", api)
	}
}
