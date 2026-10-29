package models_test

import (
	"encoding/pem"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/alternayte/assay/internal/fixtures"
	"github.com/alternayte/assay/internal/outbound"
	"github.com/alternayte/assay/internal/testkit"
)

type testResult struct {
	OK      bool   `json:"ok"`
	Message string `json:"message"`
}

func TestSCN_INS_006_ProxyAndExtraCA(t *testing.T) {
	fake := fixtures.NewModels()
	tlsSrv := httptest.NewTLSServer(fake)
	defer tlsSrv.Close()
	caPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: tlsSrv.Certificate().Raw}))
	// The test certificate covers example.com. The proxy dials the real
	// listener for that name, so the request works only through the proxy.
	listen := strings.TrimPrefix(tlsSrv.URL, "https://")
	_, port, _ := strings.Cut(listen, ":")
	base := "https://example.com:" + port + "/v1"

	run := func(t *testing.T, settings outbound.Settings) (testResult, *testkit.Proxy) {
		proxy := testkit.StartProxy(t, listen)
		settings.HTTPSProxy = proxy.URL
		env := testkit.Start(t, testkit.WithOutbound(settings))
		admin := env.Setup("owner@acme.test")
		ep := testkit.CreateEndpoint(t, admin, "Behind proxy", "openai", base, fixtures.FakeKey, nil)
		var res testResult
		admin.Must(http.StatusOK, http.MethodPost, "/api/v1/models/endpoints/"+itoa(ep)+"/test", map[string]any{}).JSON(t, &res)
		return res, proxy
	}

	t.Run("with the extra CA", func(t *testing.T) {
		res, proxy := run(t, outbound.Settings{ExtraCAPEM: caPEM})
		if !res.OK {
			t.Fatalf("test failed: %s", res.Message)
		}
		if len(proxy.Targets()) == 0 {
			t.Fatal("the request did not go through the proxy")
		}
	})

	t.Run("without the extra CA", func(t *testing.T) {
		res, proxy := run(t, outbound.Settings{})
		if res.OK || !strings.Contains(res.Message, "certificate") {
			t.Fatalf("want a TLS error, got %+v", res)
		}
		if len(proxy.Targets()) == 0 {
			t.Fatal("the request did not go through the proxy")
		}
	})
}

func TestSCN_OPS_007_NoRedirectToAnotherHost(t *testing.T) {
	other := httptest.NewServer(fixtures.NewModels())
	defer other.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, other.URL+r.URL.Path, http.StatusFound)
	}))
	defer redirect.Close()

	env := testkit.Start(t)
	admin := env.Setup("owner@acme.test")
	ep := testkit.CreateEndpoint(t, admin, "Redirects", "openai", redirect.URL+"/v1", fixtures.FakeKey, nil)
	var res testResult
	admin.Must(http.StatusOK, http.MethodPost, "/api/v1/models/endpoints/"+itoa(ep)+"/test", map[string]any{}).JSON(t, &res)
	if res.OK || !strings.Contains(res.Message, "is not followed") {
		t.Fatalf("want a refused redirect, got %+v", res)
	}
}
