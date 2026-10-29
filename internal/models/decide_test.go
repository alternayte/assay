package models

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"strings"
	"testing"
)

func TestSCN_MOD_009_DecisionAdapters(t *testing.T) {
	yes := DecisionRequest{Type: YesNo, Instructions: "Same thing?", Yes: "same", No: "different"}
	choice := DecisionRequest{Type: Choice, Instructions: "Which one?", Options: map[string]string{"a": "A", "b": "B", "none": "Neither"}}
	score := DecisionRequest{Type: Score, Instructions: "How sure?", Levels: []string{"low", "mid", "high"}}

	sum := func(p map[string]float64) float64 {
		var s float64
		for _, v := range p {
			s += v
		}
		return s
	}

	t.Run("decisions api", func(t *testing.T) {
		d, err := parseAPIAnswer(yes, json.RawMessage(`{"type":"noul","noul":0.96}`))
		if err != nil || d.P() != 0.96 || math.Abs(d.Probs["no"]-0.04) > 1e-9 {
			t.Fatalf("yes/no: %+v %v", d, err)
		}
		d, err = parseAPIAnswer(choice, json.RawMessage(`{"type":"choice","choice":"a","confidence":0.6,"probabilities":{"a":0.6,"b":0.3,"none":0.2}}`))
		if err != nil || math.Abs(sum(d.Probs)-1) > 1e-9 || d.Probs["a"] <= d.Probs["b"] {
			t.Fatalf("choice: %+v %v", d, err)
		}
		d, err = parseAPIAnswer(score, json.RawMessage(`{"type":"score","score":1.9,"probabilities":{"0":0,"1":0.1,"2":0.9}}`))
		if err != nil || math.Abs(d.Value-1.9) > 1e-9 {
			t.Fatalf("score: %+v %v", d, err)
		}
		if _, err := parseAPIAnswer(yes, json.RawMessage(`{"type":"noul"}`)); err == nil {
			t.Fatal("a yes/no answer without a probability passes")
		}
	})

	t.Run("chat fallback", func(t *testing.T) {
		d, err := parseChatAnswer(yes, `{"probabilities":{"yes":0.8,"no":0.2}}`)
		if err != nil || math.Abs(d.P()-0.8) > 1e-9 {
			t.Fatalf("yes/no: %+v %v", d, err)
		}
		d, err = parseChatAnswer(choice, `{"probabilities":{"a":0.5,"b":0.5,"none":0.5}}`)
		if err != nil || math.Abs(sum(d.Probs)-1) > 1e-9 {
			t.Fatalf("choice does not sum to 1: %+v %v", d, err)
		}
		d, err = parseChatAnswer(score, `{"probabilities":{"0":0.2,"1":0.2,"2":0.6}}`)
		if err != nil || math.Abs(d.Value-1.4) > 1e-9 {
			t.Fatalf("score: %+v %v", d, err)
		}
		if _, err := parseChatAnswer(choice, `{"probabilities":{"a":1}}`); err == nil {
			t.Fatal("a choice answer with a missing option passes")
		}
		if _, err := parseChatAnswer(yes, `not json`); err == nil {
			t.Fatal("text that is not JSON passes")
		}
	})

	t.Run("long input is refused before any request", func(t *testing.T) {
		// A gateway without a database: the check comes before any lookup or
		// request, so a nil pool would panic if the call went further.
		g := &Gateway{}
		long := DecisionRequest{Type: YesNo, Instructions: "Same?", State: map[string]any{"text": strings.Repeat("abcd ", 1280)}}
		if n := long.tokens(); n < 1600 {
			t.Fatalf("the test input has %d tokens", n)
		}
		_, err := g.Decide(context.Background(), long)
		if !errors.Is(err, ErrInputTooLong) {
			t.Fatalf("want ErrInputTooLong, got %v", err)
		}
	})
}
