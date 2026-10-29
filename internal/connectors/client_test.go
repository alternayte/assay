package connectors_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alternayte/assay/internal/connectors"
	"github.com/alternayte/assay/internal/outbound"
)

func TestSCN_CON_009_OnlyGetAndHead(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		hits.Add(1)
		_, _ = w.Write([]byte("{}"))
	}))
	defer srv.Close()
	c, err := connectors.NewClient(outbound.Settings{}, connectors.ClientOptions{BaseURL: srv.URL, Authorization: "Bearer t"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range []string{http.MethodPost, http.MethodPut, http.MethodPatch, http.MethodDelete} {
		req, _ := http.NewRequestWithContext(context.Background(), m, srv.URL+"/rest/api/content", strings.NewReader("{}"))
		resp, err := c.Do(req)
		if err == nil {
			_ = resp.Body.Close()
			t.Fatalf("%s was sent", m)
		}
		if !errors.Is(err, outbound.ErrMethod) {
			t.Fatalf("%s: %v", m, err)
		}
	}
	if n := hits.Load(); n != 0 {
		t.Fatalf("the server received %d requests", n)
	}
	for _, m := range []string{http.MethodGet, http.MethodHead} {
		req, _ := http.NewRequestWithContext(context.Background(), m, srv.URL+"/x", nil)
		resp, err := c.Do(req)
		if err != nil {
			t.Fatalf("%s: %v", m, err)
		}
		_ = resp.Body.Close()
	}
	// Another host fails before sending.
	req, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://other.acme.test/x", nil)
	if _, err := c.Do(req); !errors.Is(err, outbound.ErrForeignHost) {
		t.Fatalf("other host: %v", err)
	}
}
