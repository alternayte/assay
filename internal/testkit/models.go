package testkit

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
)

// FakeModels starts the fake model API and returns it with the base URLs of
// its OpenAI-compatible API and its Decisions API.
func FakeModels(t testing.TB) (fake *fixtures.Models, openaiURL, decisionsURL string) {
	t.Helper()
	fake = fixtures.NewModels()
	srv := httptest.NewServer(fake)
	t.Cleanup(srv.Close)
	return fake, srv.URL + "/v1", srv.URL + "/api"
}

// CreateEndpoint creates a model endpoint as admin and returns its ID.
func CreateEndpoint(t testing.TB, admin *Client, name, kind, baseURL, key string, extra map[string]any) int64 {
	t.Helper()
	body := map[string]any{"name": name, "kind": kind, "baseUrl": baseURL, "apiKey": key, "extra": extra}
	if extra == nil {
		body["extra"] = map[string]any{}
	}
	var out struct {
		ID int64 `json:"id"`
	}
	admin.Must(http.StatusCreated, http.MethodPost, "/api/v1/models/endpoints", body).JSON(t, &out)
	return out.ID
}

// Function is the configuration of one model function.
type Function struct {
	Endpoint int64
	Model    string
	Params   map[string]any
	PriceIn  float64
	PriceOut float64
	Budget   float64
}

// SetFunction turns a model function on with a configuration.
func SetFunction(t testing.TB, admin *Client, fn string, f Function) {
	t.Helper()
	if f.Params == nil {
		f.Params = map[string]any{}
	}
	if f.Budget == 0 {
		f.Budget = 100
	}
	admin.Must(http.StatusNoContent, http.MethodPut, fmt.Sprintf("/api/v1/models/functions/%s", fn), map[string]any{
		"endpointId": f.Endpoint, "model": f.Model, "params": f.Params, "priceInPerMTok": f.PriceIn,
		"priceOutPerMTok": f.PriceOut, "monthlyBudgetUsd": f.Budget, "enabled": true,
	})
}
