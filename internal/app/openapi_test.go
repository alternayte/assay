package app_test

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/google/jsonschema-go/jsonschema"

	"github.com/alternayte/assay/internal/app"
	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_AGT_004_OpenAPIDocumentAndDocs(t *testing.T) {
	// The document is valid OpenAPI 3.1: it fits the official schema
	// (testdata/oas-3.1-schema.json, from spec.openapis.org).
	doc, err := app.OpenAPIDocument()
	if err != nil {
		t.Fatal(err)
	}
	var instance map[string]any
	if err := json.Unmarshal(doc, &instance); err != nil {
		t.Fatal(err)
	}
	if v, _ := instance["openapi"].(string); !strings.HasPrefix(v, "3.1.") {
		t.Fatalf("openapi version %q", v)
	}
	raw, err := os.ReadFile("testdata/oas-3.1-schema.json")
	if err != nil {
		t.Fatal(err)
	}
	var schema jsonschema.Schema
	if err := json.Unmarshal(raw, &schema); err != nil {
		t.Fatal(err)
	}
	resolved, err := schema.Resolve(nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := resolved.Validate(instance); err != nil {
		t.Fatalf("the OpenAPI document is not valid 3.1: %v", err)
	}

	// The served document equals the committed one that the client uses,
	// and the docs page answers.
	env := testkit.Start(t)
	c := env.Client()
	r := c.Must(http.StatusOK, http.MethodGet, "/api/v1/openapi.json", nil)
	var served map[string]any
	if err := json.Unmarshal(r.Body, &served); err != nil || served["openapi"] != instance["openapi"] {
		t.Fatalf("served document: %v", err)
	}
	committed, err := os.ReadFile("../../web/openapi.json")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(string(committed)) != strings.TrimSpace(string(doc)) {
		t.Fatal("web/openapi.json differs from the document of the code. Run just gen.")
	}
	d := c.Must(http.StatusOK, http.MethodGet, "/api/docs", nil)
	if !strings.Contains(string(d.Body), "/api/v1/openapi") {
		t.Fatalf("docs page: %s", d.Body)
	}
}
