package models

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/danielgtaylor/huma/v2"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/alternayte/assay/internal/api"
	"github.com/alternayte/assay/internal/audit"
	"github.com/alternayte/assay/internal/auth"
	"github.com/alternayte/assay/internal/outbound"
)

// EndpointRecord is an endpoint as the API shows it. The key never leaves
// the server.
type EndpointRecord struct {
	ID        int64     `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind" enum:"openai,decisions"`
	BaseURL   string    `json:"baseUrl"`
	HasKey    bool      `json:"hasKey"`
	Extra     Extra     `json:"extra"`
	UpdatedAt time.Time `json:"updatedAt"`
}

// FunctionRecord is a function as the API shows it.
type FunctionRecord struct {
	Function     string         `json:"function" enum:"answer,extract,compile,embed,rerank,decide"`
	EndpointID   *int64         `json:"endpointId"`
	EndpointName string         `json:"endpointName"`
	Model        string         `json:"model"`
	Params       map[string]any `json:"params"`
	PriceIn      float64        `json:"priceInPerMTok" doc:"USD per million input tokens."`
	PriceOut     float64        `json:"priceOutPerMTok" doc:"USD per million output tokens."`
	Budget       float64        `json:"monthlyBudgetUsd"`
	Enabled      bool           `json:"enabled"`
	SpentUSD     float64        `json:"spentUsd" doc:"Spend in the current month."`
	Calls        int64          `json:"calls" doc:"Calls in the current month."`
	TokensIn     int64          `json:"tokensIn"`
	TokensOut    int64          `json:"tokensOut"`
	OverBudget   bool           `json:"overBudget" doc:"True when the budget refused a call after the last change of the function."`
}

type endpointBody struct {
	Name     string `json:"name" minLength:"1" maxLength:"100"`
	Kind     string `json:"kind" enum:"openai,decisions"`
	BaseURL  string `json:"baseUrl" format:"uri" doc:"For openai, the URL before /chat/completions, such as https://openrouter.ai/api/v1. For decisions, the URL before /alpha/decisions, such as https://openrouter.ai/api."`
	APIKey   string `json:"apiKey,omitempty" maxLength:"4096" doc:"Write-only. Empty keeps the current key."`
	ClearKey bool   `json:"clearKey,omitempty" doc:"Remove the stored key."`
	Extra    Extra  `json:"extra"`
}

func (b endpointBody) check() error {
	u, err := url.Parse(b.BaseURL)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return api.Problem(http.StatusUnprocessableEntity, "invalid_url", "The base URL must be an http or https URL.")
	}
	for k := range b.Extra.Headers {
		if strings.EqualFold(k, "Authorization") {
			return api.Problem(http.StatusUnprocessableEntity, "invalid_header", "Set the key in the API key field, not as a header.")
		}
	}
	return nil
}

// Register adds the model routes.
func (g *Gateway) Register(a huma.API) {
	admins := huma.Middlewares{api.Require(auth.RoleAdmin)}

	type listEndpoints struct {
		Body struct {
			Endpoints []EndpointRecord `json:"endpoints"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listModelEndpoints", Method: http.MethodGet, Path: "/api/v1/models/endpoints", Tags: []string{"models"},
		Summary: "List model endpoints", Middlewares: admins,
	}, func(ctx context.Context, _ *struct{}) (*listEndpoints, error) {
		rows, err := New(g.Pool).ListEndpoints(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listEndpoints{}
		out.Body.Endpoints = []EndpointRecord{}
		for _, r := range rows {
			rec := EndpointRecord{ID: r.ID, Name: r.Name, Kind: r.Kind, BaseURL: r.BaseUrl, HasKey: r.HasKey, UpdatedAt: r.UpdatedAt}
			_ = json.Unmarshal(r.Extra, &rec.Extra)
			out.Body.Endpoints = append(out.Body.Endpoints, rec)
		}
		return out, nil
	})

	type idOut struct {
		Body struct {
			ID int64 `json:"id"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "createModelEndpoint", Method: http.MethodPost, Path: "/api/v1/models/endpoints", Tags: []string{"models"},
		Summary: "Create a model endpoint", DefaultStatus: http.StatusCreated, Middlewares: admins,
	}, func(ctx context.Context, in *struct{ Body endpointBody }) (*idOut, error) {
		if err := in.Body.check(); err != nil {
			return nil, err
		}
		out := &idOut{}
		err := pgx.BeginFunc(ctx, g.Pool, func(tx pgx.Tx) error {
			extra, _ := json.Marshal(in.Body.Extra)
			id, err := New(tx).InsertEndpoint(ctx, InsertEndpointParams{Name: strings.TrimSpace(in.Body.Name), Kind: in.Body.Kind,
				BaseUrl: strings.TrimRight(in.Body.BaseURL, "/"), Extra: extra})
			if err != nil {
				return uniqueName(err)
			}
			out.Body.ID = id
			if in.Body.APIKey != "" {
				if err := g.setKey(ctx, tx, id, in.Body.APIKey); err != nil {
					return err
				}
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "models.endpoint_create", Target: fmt.Sprintf("endpoint:%d", id),
				Details: map[string]any{"name": in.Body.Name, "kind": in.Body.Kind, "baseUrl": in.Body.BaseURL}})
		})
		return out, wrap(err)
	})

	type updateIn struct {
		ID   int64 `path:"id"`
		Body endpointBody
	}
	huma.Register(a, huma.Operation{
		OperationID: "updateModelEndpoint", Method: http.MethodPut, Path: "/api/v1/models/endpoints/{id}", Tags: []string{"models"},
		Summary: "Change a model endpoint", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *updateIn) (*struct{}, error) {
		if err := in.Body.check(); err != nil {
			return nil, err
		}
		err := pgx.BeginFunc(ctx, g.Pool, func(tx pgx.Tx) error {
			q := New(tx)
			extra, _ := json.Marshal(in.Body.Extra)
			n, err := q.UpdateEndpoint(ctx, UpdateEndpointParams{ID: in.ID, Name: strings.TrimSpace(in.Body.Name), Kind: in.Body.Kind,
				BaseUrl: strings.TrimRight(in.Body.BaseURL, "/"), Extra: extra})
			if err != nil {
				return uniqueName(err)
			}
			if n == 0 {
				return api.ErrNotFound
			}
			switch {
			case in.Body.ClearKey:
				if err := q.SetEndpointSecret(ctx, SetEndpointSecretParams{ID: in.ID}); err != nil {
					return err
				}
				if err := g.Secrets.Delete(ctx, tx, keyName(in.ID)); err != nil {
					return err
				}
			case in.Body.APIKey != "":
				if err := g.setKey(ctx, tx, in.ID, in.Body.APIKey); err != nil {
					return err
				}
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "models.endpoint_update", Target: fmt.Sprintf("endpoint:%d", in.ID),
				Details: map[string]any{"name": in.Body.Name, "kind": in.Body.Kind, "baseUrl": in.Body.BaseURL,
					"keyChanged": in.Body.APIKey != "" || in.Body.ClearKey}})
		})
		return nil, wrap(err)
	})

	type idIn struct {
		ID int64 `path:"id"`
	}
	huma.Register(a, huma.Operation{
		OperationID: "deleteModelEndpoint", Method: http.MethodDelete, Path: "/api/v1/models/endpoints/{id}", Tags: []string{"models"},
		Summary: "Delete a model endpoint", Description: "Functions that use it lose their endpoint.", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *idIn) (*struct{}, error) {
		err := pgx.BeginFunc(ctx, g.Pool, func(tx pgx.Tx) error {
			n, err := New(tx).DeleteEndpoint(ctx, in.ID)
			if err != nil {
				return err
			}
			if n == 0 {
				return api.ErrNotFound
			}
			if err := g.Secrets.Delete(ctx, tx, keyName(in.ID)); err != nil {
				return err
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "models.endpoint_delete", Target: fmt.Sprintf("endpoint:%d", in.ID)})
		})
		return nil, wrap(err)
	})

	type testIn struct {
		ID   int64 `path:"id"`
		Body struct {
			Model string `json:"model,omitempty" doc:"The model for the minimal call. Empty uses the first listed model."`
		}
	}
	type testOut struct {
		Body TestResult
	}
	huma.Register(a, huma.Operation{
		OperationID: "testModelEndpoint", Method: http.MethodPost, Path: "/api/v1/models/endpoints/{id}/test", Tags: []string{"models"},
		Summary: "Test a model endpoint", Description: "Lists the models and makes one minimal call.", Middlewares: admins,
	}, func(ctx context.Context, in *testIn) (*testOut, error) {
		res, err := g.TestEndpoint(ctx, in.ID, in.Body.Model)
		if err != nil {
			return nil, wrap(err)
		}
		return &testOut{Body: *res}, nil
	})

	type listFunctions struct {
		Body struct {
			Functions []FunctionRecord `json:"functions"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "listModelFunctions", Method: http.MethodGet, Path: "/api/v1/models/functions", Tags: []string{"models"},
		Summary: "List model functions with the spend of the month", Middlewares: admins,
	}, func(ctx context.Context, _ *struct{}) (*listFunctions, error) {
		recs, err := g.functions(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		out := &listFunctions{}
		out.Body.Functions = recs
		return out, nil
	})

	type fnIn struct {
		Function string `path:"function" enum:"answer,extract,compile,embed,rerank,decide"`
		Body     struct {
			EndpointID *int64         `json:"endpointId"`
			Model      string         `json:"model" maxLength:"200"`
			Params     map[string]any `json:"params"`
			PriceIn    float64        `json:"priceInPerMTok" minimum:"0"`
			PriceOut   float64        `json:"priceOutPerMTok" minimum:"0"`
			Budget     float64        `json:"monthlyBudgetUsd" minimum:"0"`
			Enabled    bool           `json:"enabled"`
		}
	}
	huma.Register(a, huma.Operation{
		OperationID: "updateModelFunction", Method: http.MethodPut, Path: "/api/v1/models/functions/{function}", Tags: []string{"models"},
		Summary: "Change a model function", DefaultStatus: http.StatusNoContent, Middlewares: admins,
	}, func(ctx context.Context, in *fnIn) (*struct{}, error) {
		if in.Body.Enabled && (in.Body.EndpointID == nil || strings.TrimSpace(in.Body.Model) == "") {
			return nil, api.Problem(http.StatusUnprocessableEntity, "incomplete", "Set an endpoint and a model before you turn the function on.")
		}
		for _, k := range []string{"tools", "tool_choice", "functions", "model", "messages", "input"} {
			if _, ok := in.Body.Params[k]; ok {
				return nil, api.Problem(http.StatusUnprocessableEntity, "invalid_param", "The parameter "+k+" is not allowed.")
			}
		}
		err := pgx.BeginFunc(ctx, g.Pool, func(tx pgx.Tx) error {
			params, _ := json.Marshal(orEmpty(in.Body.Params))
			n, err := New(tx).UpdateFunction(ctx, UpdateFunctionParams{Function: in.Function, EndpointID: in.Body.EndpointID,
				Model: strings.TrimSpace(in.Body.Model), Params: params, PriceIn: in.Body.PriceIn, PriceOut: in.Body.PriceOut,
				Budget: in.Body.Budget, Enabled: in.Body.Enabled})
			if err != nil {
				var pe *pgconn.PgError
				if errors.As(err, &pe) && pe.Code == "23503" {
					return api.Problem(http.StatusUnprocessableEntity, "unknown_endpoint", "The endpoint does not exist.")
				}
				return err
			}
			if n == 0 {
				return api.ErrNotFound
			}
			return audit.Write(ctx, tx, audit.Entry{Action: "models.function_update", Target: "function:" + in.Function,
				Details: map[string]any{"endpoint": in.Body.EndpointID, "model": in.Body.Model, "budget": in.Body.Budget, "enabled": in.Body.Enabled}})
		})
		return nil, wrap(err)
	})

	type statusOut struct {
		Body Status
	}
	huma.Register(a, huma.Operation{
		OperationID: "getModelStatus", Method: http.MethodGet, Path: "/api/v1/models/status", Tags: []string{"models"},
		Summary: "Report functions that are off or over budget", Middlewares: huma.Middlewares{api.Require(auth.RoleMember)},
	}, func(ctx context.Context, _ *struct{}) (*statusOut, error) {
		st, err := g.Status(ctx)
		if err != nil {
			return nil, api.Internal(err)
		}
		return &statusOut{Body: *st}, nil
	})
}

// Status lists the functions that are off or over budget.
type Status struct {
	Off        []string `json:"off"`
	OverBudget []string `json:"overBudget"`
}

// Status reports the functions that are off or over budget.
func (g *Gateway) Status(ctx context.Context) (*Status, error) {
	recs, err := g.functions(ctx)
	if err != nil {
		return nil, err
	}
	st := &Status{Off: []string{}, OverBudget: []string{}}
	for _, r := range recs {
		if !r.Enabled {
			st.Off = append(st.Off, r.Function)
		} else if r.OverBudget {
			st.OverBudget = append(st.OverBudget, r.Function)
		}
	}
	return st, nil
}

func (g *Gateway) functions(ctx context.Context) ([]FunctionRecord, error) {
	q := New(g.Pool)
	rows, err := q.ListFunctions(ctx)
	if err != nil {
		return nil, err
	}
	spend, err := q.SpendByFunction(ctx, monthStart(g.now()))
	if err != nil {
		return nil, err
	}
	byFn := map[string]SpendByFunctionRow{}
	for _, s := range spend {
		byFn[s.Function] = s
	}
	out := make([]FunctionRecord, 0, len(rows))
	for _, r := range rows {
		s := byFn[r.Function]
		rec := FunctionRecord{Function: r.Function, EndpointID: r.EndpointID, EndpointName: r.EndpointName, Model: r.Model,
			PriceIn: r.PriceIn, PriceOut: r.PriceOut, Budget: r.Budget, Enabled: r.Enabled,
			SpentUSD: s.CostUsd, Calls: s.Calls, TokensIn: s.TokensIn, TokensOut: s.TokensOut, OverBudget: r.Refused}
		_ = json.Unmarshal(r.Params, &rec.Params)
		if rec.Params == nil {
			rec.Params = map[string]any{}
		}
		out = append(out, rec)
	}
	return out, nil
}

func keyName(endpointID int64) string { return fmt.Sprintf("endpoint.%d.key", endpointID) }

func (g *Gateway) setKey(ctx context.Context, tx pgx.Tx, id int64, key string) error {
	sid, err := g.Secrets.Put(ctx, tx, keyName(id), []byte(key))
	if err != nil {
		return err
	}
	return New(tx).SetEndpointSecret(ctx, SetEndpointSecretParams{ID: id, SecretID: &sid})
}

// TestResult is the result of an endpoint test.
type TestResult struct {
	OK      bool     `json:"ok"`
	Models  []string `json:"models"`
	Model   string   `json:"model" doc:"The model of the minimal call."`
	Message string   `json:"message" doc:"The error text of the endpoint when the test fails."`
}

// TestEndpoint lists the models of an endpoint and makes one minimal call
// (REQ-MOD-001).
func (g *Gateway) TestEndpoint(ctx context.Context, id int64, model string) (*TestResult, error) {
	r, err := New(g.Pool).GetEndpoint(ctx, id)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, api.ErrNotFound
	}
	if err != nil {
		return nil, err
	}
	t := &target{function: Decide, kind: r.Kind, baseURL: strings.TrimRight(r.BaseUrl, "/")}
	_ = json.Unmarshal(r.Extra, &t.extra)
	if r.SecretID != nil {
		k, err := g.Secrets.GetByID(ctx, g.Pool, *r.SecretID)
		if err != nil {
			return nil, err
		}
		t.key = string(k)
	}
	res := &TestResult{Models: []string{}}
	ids, err := g.listModels(ctx, t)
	if err != nil {
		// A failed test is a result, not a server error.
		res.Message = err.Error()
		return res, nil //nolint:nilerr // The message carries the error.
	}
	res.Models = ids
	if model == "" && len(ids) > 0 {
		model = ids[0]
	}
	if model == "" {
		res.Message = "The endpoint lists no models. Name a model for the test."
		return res, nil
	}
	res.Model = model
	t.model = model
	var out json.RawMessage
	var body map[string]any
	path := "/chat/completions"
	if t.kind == "decisions" {
		path = "/alpha/decisions"
		body = map[string]any{"model": model, "state": map[string]any{"text": "test"},
			"questions": map[string]any{"q": map[string]any{"type": "noul", "instructions": "Is the text a test?",
				"criteria": map[string]any{"true": "It is a test.", "false": "It is not a test."}}}}
	} else {
		body = map[string]any{"model": model, "max_tokens": 1, "messages": []any{map[string]any{"role": "user", "content": "Say OK."}}}
	}
	if err := g.post(ctx, t, path, body, &out); err != nil {
		res.Message = err.Error()
		return res, nil //nolint:nilerr // The message carries the error.
	}
	res.OK = true
	return res, nil
}

func (g *Gateway) listModels(ctx context.Context, t *target) ([]string, error) {
	base := t.baseURL
	if t.kind == "decisions" {
		base += "/v1"
	}
	client, err := outbound.New(g.Outbound, outbound.Options{BaseURL: base, Timeout: 15 * time.Second})
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, err
	}
	for k, v := range t.extra.Headers {
		req.Header.Set(k, v)
	}
	if t.key != "" {
		req.Header.Set("Authorization", "Bearer "+t.key)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 16<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode >= 400 {
		return nil, &CallError{Status: resp.StatusCode, Message: errorMessage(b)}
	}
	var list struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(b, &list); err != nil {
		return nil, fmt.Errorf("the model list is not valid JSON: %w", err)
	}
	ids := make([]string, 0, len(list.Data))
	for _, d := range list.Data {
		ids = append(ids, d.ID)
	}
	return ids, nil
}

func uniqueName(err error) error {
	var pe *pgconn.PgError
	if errors.As(err, &pe) && pe.Code == "23505" {
		return api.Problem(http.StatusConflict, "name_taken", "An endpoint with this name exists.")
	}
	return err
}

func wrap(err error) error {
	if err == nil {
		return nil
	}
	var ae *api.Error
	if errors.As(err, &ae) {
		return ae
	}
	return api.Internal(err)
}

func orEmpty(m map[string]any) map[string]any {
	if m == nil {
		return map[string]any{}
	}
	return m
}

// Endpoints returns the endpoints without keys.
func (g *Gateway) Endpoints(ctx context.Context) ([]EndpointRecord, error) {
	rows, err := New(g.Pool).ListEndpoints(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]EndpointRecord, 0, len(rows))
	for _, r := range rows {
		out = append(out, EndpointRecord{ID: r.ID, Name: r.Name, Kind: r.Kind, BaseURL: r.BaseUrl, HasKey: r.HasKey, UpdatedAt: r.UpdatedAt})
	}
	return out, nil
}

// Reach lists the models of an endpoint and returns their count. The error
// names the cause, such as a refused connection or a TLS failure.
func (g *Gateway) Reach(ctx context.Context, id int64) (int, error) {
	r, err := New(g.Pool).GetEndpoint(ctx, id)
	if err != nil {
		return 0, err
	}
	t := &target{kind: r.Kind, baseURL: strings.TrimRight(r.BaseUrl, "/")}
	_ = json.Unmarshal(r.Extra, &t.extra)
	if r.SecretID != nil {
		k, err := g.Secrets.GetByID(ctx, g.Pool, *r.SecretID)
		if err != nil {
			return 0, err
		}
		t.key = string(k)
	}
	ids, err := g.listModels(ctx, t)
	return len(ids), err
}
