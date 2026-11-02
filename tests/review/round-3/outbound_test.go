package round3

import (
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/alternayte/assay/internal/outbound"
)

// A source on https sends a redirect to http on the same host and port.
// Following it sends the bearer token without TLS, so the client must
// refuse it as it refuses another host (SI-04).
func TestRVW_3_09_SameHostRedirectToHTTPKeepsToken(t *testing.T) {
	srv := httptest.NewTLSServer(http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		http.Redirect(rw, r, "http://"+r.Host+"/plain", http.StatusFound)
	}))
	t.Cleanup(srv.Close)
	ca := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: srv.Certificate().Raw}))
	c, err := outbound.New(outbound.Settings{ExtraCAPEM: ca}, outbound.Options{BaseURL: srv.URL})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodGet, srv.URL+"/rest/api/space", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer acme-token")
	resp, err := c.Do(req)
	if err == nil {
		_ = resp.Body.Close()
	}
	if !errors.Is(err, outbound.ErrForeignHost) {
		t.Errorf("the client follows a redirect from https to http on the same host: %v", err)
	}
}
