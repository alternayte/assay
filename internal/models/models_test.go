package models_test

import (
	"context"
	"errors"
	"math"
	"net/http"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/models"
	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_MOD_001_EndpointTest(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	_, openaiURL, decisionsURL := testkit.FakeModels(t)

	good := testkit.CreateEndpoint(t, admin, "Fake", "openai", openaiURL, fixtures.FakeKey, nil)
	var res struct {
		OK      bool     `json:"ok"`
		Models  []string `json:"models"`
		Message string   `json:"message"`
	}
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/models/endpoints/"+itoa(good)+"/test", map[string]any{}).JSON(t, &res)
	if !res.OK || len(res.Models) != len(fixtures.FakeModelIDs) {
		t.Fatalf("good endpoint: %+v", res)
	}

	dec := testkit.CreateEndpoint(t, admin, "Fake decisions", "decisions", decisionsURL, fixtures.FakeKey, nil)
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/models/endpoints/"+itoa(dec)+"/test", map[string]any{"model": "fake-decide"}).JSON(t, &res)
	if !res.OK {
		t.Fatalf("decisions endpoint: %+v", res)
	}

	bad := testkit.CreateEndpoint(t, admin, "Wrong key", "openai", openaiURL, "wrong-key", nil)
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/models/endpoints/"+itoa(bad)+"/test", map[string]any{}).JSON(t, &res)
	if res.OK || !strings.Contains(res.Message, "Invalid API key") {
		t.Fatalf("wrong key: %+v", res)
	}

	// The key never comes back.
	r := admin.Must(http.StatusOK, http.MethodGet, "/api/v1/models/endpoints", nil)
	if strings.Contains(string(r.Body), fixtures.FakeKey) || strings.Contains(string(r.Body), "wrong-key") {
		t.Fatal("the endpoint list contains a key")
	}
}

func TestSCN_MOD_002_ParametersHeadersAndBody(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	fake, openaiURL, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, admin, "Fake", "openai", openaiURL, fixtures.FakeKey, map[string]any{
		"headers": map[string]string{"X-Title": "Assay", "HTTP-Referer": "http://assay.test"},
		"body":    map[string]any{"provider": map[string]any{"order": []string{"acme"}}, "temperature": 0.9},
	})
	testkit.SetFunction(t, admin, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat", Params: map[string]any{"temperature": 0.2, "top_p": 0.5}})

	_, err := env.Server.Models().Chat(context.Background(), models.Answer, models.ChatRequest{Messages: []models.Message{{Role: "user", Content: "Hello."}}})
	if err != nil {
		t.Fatal(err)
	}
	reqs := fake.RequestsTo("/v1/chat/completions")
	if len(reqs) != 1 {
		t.Fatalf("want 1 request, got %d", len(reqs))
	}
	r := reqs[0]
	if r.Body["model"] != "fake-chat" || r.Body["temperature"] != 0.2 || r.Body["top_p"] != 0.5 {
		t.Fatalf("model or parameters: %v", r.Body)
	}
	if r.Header.Get("X-Title") != "Assay" || r.Header.Get("HTTP-Referer") != "http://assay.test" {
		t.Fatalf("headers: %v", r.Header)
	}
	prov, _ := r.Body["provider"].(map[string]any)
	if prov == nil || prov["order"] == nil {
		t.Fatalf("body defaults: %v", r.Body)
	}
	if _, ok := r.Body["tools"]; ok {
		t.Fatal("the request carries tools")
	}
}

func TestSCN_MOD_003_BudgetRefusesBeforeSending(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	fake, openaiURL, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, admin, "Fake", "openai", openaiURL, fixtures.FakeKey, nil)
	testkit.SetFunction(t, admin, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat", PriceIn: 1, PriceOut: 1, Budget: 0.01})

	ctx := context.Background()
	if _, err := env.Server.Pool().Exec(ctx,
		"INSERT INTO model_usage (function, model, tokens_in, tokens_out, cost_usd) VALUES ('answer', 'fake-chat', 9900, 0, 0.0099)"); err != nil {
		t.Fatal(err)
	}
	_, err := env.Server.Models().Chat(ctx, models.Answer, models.ChatRequest{Messages: []models.Message{{Role: "user", Content: "Hello."}}, MaxTokens: 200})
	if !errors.Is(err, models.ErrBudgetExceeded) {
		t.Fatalf("want ErrBudgetExceeded, got %v", err)
	}
	if n := len(fake.Requests()); n != 0 {
		t.Fatalf("the fake received %d requests", n)
	}
	var st struct {
		OverBudget []string `json:"overBudget"`
	}
	admin.Must(http.StatusOK, http.MethodGet, "/api/v1/models/status", nil).JSON(t, &st)
	if len(st.OverBudget) != 1 || st.OverBudget[0] != "answer" {
		t.Fatalf("status %+v, want answer over budget", st)
	}
	// A higher budget clears the banner.
	testkit.SetFunction(t, admin, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat", PriceIn: 1, PriceOut: 1, Budget: 1})
	admin.Must(http.StatusOK, http.MethodGet, "/api/v1/models/status", nil).JSON(t, &st)
	if len(st.OverBudget) != 0 {
		t.Fatalf("status %+v after a higher budget", st)
	}
}

func TestSCN_MOD_004_CostFromTokensAndPrices(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	_, openaiURL, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, admin, "Fake", "openai", openaiURL, fixtures.FakeKey, nil)
	testkit.SetFunction(t, admin, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat", PriceIn: 3, PriceOut: 15})
	testkit.SetFunction(t, admin, "embed", testkit.Function{Endpoint: ep, Model: "fake-embed-64", PriceIn: 0.02})

	ctx := context.Background()
	g := env.Server.Models()
	var want float64
	for _, q := range []string{"What owns the payments API?", "Who decided the migration plan for the reminder service?"} {
		res, err := g.Chat(ctx, models.Answer, models.ChatRequest{Messages: []models.Message{{Role: "user", Content: q}}})
		if err != nil {
			t.Fatal(err)
		}
		want += models.Cost(res.TokensIn, res.TokensOut, 3, 15)
	}
	if _, err := g.Embed(ctx, []string{"one text", "another text here"}, "test"); err != nil {
		t.Fatal(err)
	}

	var rows []struct {
		In, Out int
		Cost    float64
	}
	r, err := env.Server.Pool().Query(ctx, "SELECT tokens_in, tokens_out, cost_usd::float8 FROM model_usage WHERE function = 'answer'")
	if err != nil {
		t.Fatal(err)
	}
	for r.Next() {
		var x struct {
			In, Out int
			Cost    float64
		}
		if err := r.Scan(&x.In, &x.Out, &x.Cost); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, x)
	}
	r.Close()
	if len(rows) != 2 {
		t.Fatalf("want 2 usage rows, got %d", len(rows))
	}
	for _, x := range rows {
		if math.Abs(x.Cost-models.Cost(x.In, x.Out, 3, 15)) > 1e-9 {
			t.Fatalf("cost %v for %d in and %d out", x.Cost, x.In, x.Out)
		}
	}

	var fns struct {
		Functions []struct {
			Function string  `json:"function"`
			Spent    float64 `json:"spentUsd"`
			Calls    int64   `json:"calls"`
		} `json:"functions"`
	}
	admin.Must(http.StatusOK, http.MethodGet, "/api/v1/models/functions", nil).JSON(t, &fns)
	for _, f := range fns.Functions {
		switch f.Function {
		case "answer":
			if f.Calls != 2 || math.Abs(f.Spent-want) > 1e-9 {
				t.Fatalf("answer spend %+v, want %v", f, want)
			}
		case "embed":
			if f.Calls != 1 || f.Spent <= 0 {
				t.Fatalf("embed spend %+v", f)
			}
		}
	}
}

func TestSCN_MOD_007_RetriesAndTimeout(t *testing.T) {
	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	fake, openaiURL, _ := testkit.FakeModels(t)
	ep := testkit.CreateEndpoint(t, admin, "Fake", "openai", openaiURL, fixtures.FakeKey, nil)
	testkit.SetFunction(t, admin, "answer", testkit.Function{Endpoint: ep, Model: "fake-chat"})
	g := env.Server.Models()
	g.Backoff = 10 * time.Millisecond
	ctx := context.Background()
	req := models.ChatRequest{Messages: []models.Message{{Role: "user", Content: "Hello."}}}

	busy := fixtures.Reply{Status: http.StatusTooManyRequests, Body: map[string]any{"error": map[string]any{"message": "slow down"}}}
	fake.Queue("/v1/chat/completions", busy, busy)
	if _, err := g.Chat(ctx, models.Answer, req); err != nil {
		t.Fatalf("two 429 then 200: %v", err)
	}
	if n := len(fake.RequestsTo("/v1/chat/completions")); n != 3 {
		t.Fatalf("want 3 attempts, got %d", n)
	}

	fake.Reset()
	fake.Queue("/v1/chat/completions", busy, busy, busy)
	var ce *models.CallError
	if _, err := g.Chat(ctx, models.Answer, req); !errors.As(err, &ce) || ce.Status != http.StatusTooManyRequests {
		t.Fatalf("three 429: %v", err)
	}
	if n := len(fake.RequestsTo("/v1/chat/completions")); n != 3 {
		t.Fatalf("want 3 attempts after three 429, got %d", n)
	}

	fake.Reset()
	g.Timeouts = map[models.Function]time.Duration{models.Answer: 300 * time.Millisecond}
	block := fixtures.Reply{Block: true}
	fake.Queue("/v1/chat/completions", block, block, block)
	start := time.Now()
	_, err := g.Chat(ctx, models.Answer, req)
	if err == nil || !strings.Contains(err.Error(), "did not answer within 300ms") {
		t.Fatalf("no response: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("the timeout took %v", d)
	}
}

func itoa(n int64) string { return strconv.FormatInt(n, 10) }
