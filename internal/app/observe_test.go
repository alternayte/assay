package app_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/alternayte/assay/internal/testkit"
)

func TestSCN_OPS_002_RequestSpanExport(t *testing.T) {
	var mu sync.Mutex
	var bodies [][]byte
	collector := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		if r.URL.Path == "/v1/traces" {
			mu.Lock()
			bodies = append(bodies, b)
			mu.Unlock()
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer collector.Close()
	t.Setenv("OTEL_EXPORTER_OTLP_ENDPOINT", collector.URL)

	env := testkit.Start(t)
	env.Client().Must(http.StatusOK, http.MethodGet, "/healthz", nil)
	// Close flushes the batch of spans.
	env.Server.Close()

	mu.Lock()
	defer mu.Unlock()
	if len(bodies) == 0 {
		t.Fatal("the collector received no spans")
	}
	if !bytes.Contains(bytes.Join(bodies, nil), []byte("HTTP GET")) {
		t.Fatal("the export holds no request span")
	}
}
