package entra

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/alternayte/assay/internal/outbound"
)

// The token client is the one client that may POST to a source host. It
// must refuse every URL but the token URL, and every method but POST.
func TestSCN_CON_014_TokenClientSendsOnlyToTheTokenURL(t *testing.T) {
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		if r.Method != http.MethodPost || r.URL.Path != "/acme-tenant/oauth2/v2.0/token" {
			http.Error(w, "unexpected", http.StatusTeapot)
			return
		}
		if err := r.ParseForm(); err != nil || r.PostForm.Get("grant_type") != "client_credentials" ||
			r.PostForm.Get("client_secret") != "s3cret" || r.PostForm.Get("scope") != "https://graph.microsoft.com/.default" {
			http.Error(w, `{"error":"invalid_client"}`, http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"access_token":"tok-1","expires_in":3600,"token_type":"Bearer"}`))
	}))
	defer srv.Close()

	tokenURL := TokenURL(srv.URL, "acme-tenant")
	ts, err := NewTokenSource(outbound.Settings{}, Credentials{TokenURL: tokenURL, ClientID: "app", ClientSecret: "s3cret",
		Scope: "https://graph.microsoft.com/.default"})
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	auth, err := ts.Authorization(ctx)
	if err != nil || auth != "Bearer tok-1" {
		t.Fatalf("Authorization = %q, %v", auth, err)
	}
	if _, err := ts.Token(ctx); err != nil || hits.Load() != 1 {
		t.Fatalf("the cached token was not reused: hits %d, %v", hits.Load(), err)
	}

	for _, tc := range []struct {
		method, url string
		want        error
	}{
		{http.MethodPost, srv.URL + "/acme-tenant/oauth2/v2.0/authorize", outbound.ErrForeignURL},
		{http.MethodPost, srv.URL + "/other-tenant/oauth2/v2.0/token", outbound.ErrForeignURL},
		{http.MethodPost, tokenURL + "/", outbound.ErrForeignURL},
		{http.MethodPost, tokenURL + "?x=1", outbound.ErrForeignURL},
		{http.MethodPost, strings.Replace(tokenURL, "127.0.0.1", "localhost", 1), outbound.ErrForeignURL},
		{http.MethodGet, tokenURL, outbound.ErrMethod},
		{http.MethodPut, tokenURL, outbound.ErrMethod},
	} {
		req, _ := http.NewRequestWithContext(ctx, tc.method, tc.url, strings.NewReader("x=1"))
		resp, err := ts.http.Do(req)
		if resp != nil {
			_ = resp.Body.Close()
		}
		if !errors.Is(err, tc.want) {
			t.Errorf("%s %s: error %v, want %v", tc.method, tc.url, err, tc.want)
		}
	}
	if hits.Load() != 1 {
		t.Fatalf("a refused request reached the server: hits %d", hits.Load())
	}
}
