package connectors_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"

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
	resp, err := c.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, outbound.ErrForeignHost) {
		t.Fatalf("other host: %v", err)
	}
}

// TestSCN_CON_009_RateLimitIsNotARefusedToken checks that a 403 for a used
// up rate limit waits for the reset and is never a refused token.
func TestSCN_CON_009_RateLimitIsNotARefusedToken(t *testing.T) {
	var hits atomic.Int32
	limited := func(w http.ResponseWriter) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.Header().Set("X-RateLimit-Reset", strconv.FormatInt(time.Now().Unix(), 10))
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"API rate limit exceeded for user ID 1."}`))
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := hits.Add(1)
		switch {
		case r.URL.Path == "/once" && n == 1:
			limited(w)
		case r.URL.Path == "/secondary":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"You have exceeded a secondary rate limit. Please wait a few minutes before you try again."}`))
		case r.URL.Path == "/always":
			limited(w)
		case r.URL.Path == "/refused":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by personal access token"}`))
		default:
			_, _ = w.Write([]byte("{}"))
		}
	}))
	defer srv.Close()
	c, err := connectors.NewClient(outbound.Settings{}, connectors.ClientOptions{BaseURL: srv.URL, PerSecond: 1000})
	if err != nil {
		t.Fatal(err)
	}
	c.Backoff, c.Retries = time.Millisecond, 2
	ctx := context.Background()
	var out map[string]any
	if err := c.GetJSON(ctx, "/once", nil, &out); err != nil {
		t.Fatalf("after the reset: %v", err)
	}
	for _, p := range []string{"/always", "/secondary"} {
		err := c.GetJSON(ctx, p, nil, &out)
		var se *connectors.StatusError
		if errors.Is(err, connectors.ErrUnauthorized) || !errors.As(err, &se) || !se.RateLimit || se.Path != p {
			t.Fatalf("%s: %v", p, err)
		}
	}
	if err := c.GetJSON(ctx, "/refused", nil, &out); !errors.Is(err, connectors.ErrUnauthorized) {
		t.Fatalf("a refused token: %v", err)
	}
}
