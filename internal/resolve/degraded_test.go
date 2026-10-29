package resolve_test

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSCN_MOD_008_DegradedModes(t *testing.T) {
	w := start(t)
	// No model function is on.

	// Without embed, search uses full-text and trigram.
	var found struct {
		Degraded bool `json:"degraded"`
		Results  []struct {
			Title string `json:"title"`
		} `json:"results"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/search?q="+url.QueryEscape("payments api"), nil).JSON(t, &found)
	if !found.Degraded || len(found.Results) == 0 {
		t.Fatalf("search: degraded %v, %d results", found.Degraded, len(found.Results))
	}

	// Without answer, Ask shows results and a notice.
	r := w.owner.Do(http.MethodPost, "/api/v1/ask", map[string]string{"question": "Who owns the Payments API?"})
	body := string(r.Body)
	if r.Status != http.StatusOK || !strings.Contains(body, `"code":"answer_off"`) || !strings.Contains(body, "event: results") {
		t.Fatalf("ask: %d %s", r.Status, body)
	}

	// Without compile, pages show the deterministic sections.
	key := url.PathEscape("backstage:component:default/payments-api")
	var p struct {
		Facts   []struct{} `json:"facts"`
		Sources []struct{} `json:"sources"`
		Summary struct {
			State string `json:"state"`
		} `json:"summary"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/entities/"+key+"/page", nil).JSON(t, &p)
	if p.Summary.State != "off" || len(p.Facts) < 3 || len(p.Sources) == 0 {
		t.Fatalf("page: state %s, %d facts, %d sources", p.Summary.State, len(p.Facts), len(p.Sources))
	}
	c := w.owner.Must(http.StatusOK, http.MethodPost, "/api/v1/entities/"+key+"/page/compile", nil)
	if !strings.Contains(string(c.Body), `"state":"off"`) || !strings.Contains(string(c.Body), "event: page") {
		t.Fatalf("compile: %s", c.Body)
	}

	// Without decide, resolution creates cards with no band.
	if err := w.env.Server.Resolve().Run(context.Background()); err != nil {
		t.Fatal(err)
	}
	var cards struct {
		Cards []struct {
			Kind string `json:"kind"`
			Band string `json:"band"`
		} `json:"cards"`
	}
	w.owner.Must(http.StatusOK, http.MethodGet, "/api/v1/cards?kind=merge", nil).JSON(t, &cards)
	if len(cards.Cards) == 0 {
		t.Fatal("no merge cards without decide")
	}
	for _, cd := range cards.Cards {
		if cd.Band != "" {
			t.Fatalf("a card has the band %s without decide", cd.Band)
		}
	}
	if n := w.env.Count("SELECT count(*) FROM model_usage"); n != 0 {
		t.Fatalf("%d model calls with all functions off", n)
	}
}
