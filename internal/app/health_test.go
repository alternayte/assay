package app_test

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_OPS_001_HealthReadyAndMetrics(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	_, base := testkit.FakeSources(t)
	id := testkit.CreateConnection(t, admin, testkit.Connection{Name: "acme-jira", Kind: "jira", BaseURL: base,
		Scope: map[string]any{"projects": []string{"PAY"}}})
	if r := env.RunNow(admin, id, "sync"); r.Status != "succeeded" {
		t.Fatalf("sync: %+v", r)
	}
	ctx := context.Background()
	if _, err := env.Server.Pool().Exec(ctx, `
INSERT INTO decisions (kind, subject_refs, input_hash, probs, raw_p, band, connections)
VALUES ('same_entity', ARRAY['k:a', 'k:b'], '\x01', '{}', 0.95, 'high', '{}')`); err != nil {
		t.Fatal(err)
	}
	if _, err := env.Server.Pool().Exec(ctx, `
INSERT INTO model_usage (function, model, tokens_in, tokens_out, cost_usd, ref) VALUES ('decide', 'fake-decide', 120, 0, 0.001, 'same_entity')`); err != nil {
		t.Fatal(err)
	}

	get := func(h http.Handler, path string) (int, string) {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		b, _ := io.ReadAll(rec.Body)
		return rec.Code, string(b)
	}
	h := env.Server.Handler()
	if code, _ := get(h, "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d", code)
	}
	if code, _ := get(h, "/readyz"); code != http.StatusOK {
		t.Errorf("/readyz = %d with the database up", code)
	}
	_, body := get(h, "/metrics")
	for _, want := range []string{
		`assay_http_requests_total{`,
		`assay_river_jobs{`,
		`assay_sync_runs_total{`,
		`assay_sync_items_total{`,
		`assay_decisions{band="high",kind="same_entity",outcome="open"} 1`,
		`assay_model_usage_tokens{direction="in",function="decide"} 120`,
		`assay_model_usage_cost_usd{function="decide"} 0.001`,
	} {
		if !strings.Contains(body, want) {
			t.Errorf("/metrics has no %s", want)
		}
	}

	// A second server loses its database: /readyz is 503, /healthz stays 200.
	dbURL := testkit.DB(t)
	s, err := app.New(ctx, testConfig(dbURL), app.NewLogger(io.Discard, 0), app.Options{})
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if code, _ := get(s.Handler(), "/readyz"); code != http.StatusOK {
		t.Fatalf("/readyz = %d before the database goes", code)
	}
	u, _ := url.Parse(dbURL)
	name := strings.TrimPrefix(u.Path, "/")
	u.Path = "/postgres"
	conn, err := pgx.Connect(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = conn.Close(ctx) }()
	if _, err := conn.Exec(ctx, "DROP DATABASE "+pgx.Identifier{name}.Sanitize()+" WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if code, _ := get(s.Handler(), "/readyz"); code != http.StatusServiceUnavailable {
		t.Errorf("/readyz = %d with the database down, want 503", code)
	}
	if code, _ := get(s.Handler(), "/healthz"); code != http.StatusOK {
		t.Errorf("/healthz = %d with the database down", code)
	}
}
