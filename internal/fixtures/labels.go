package fixtures

import (
	"encoding/json"
	"strings"
)

// Pair is two entity keys of the corpus. Keys hold {host} for the host of
// the fixture server.
type Pair struct {
	A string `json:"a"`
	B string `json:"b"`
}

// Labels are the known answers of the corpus for resolution: pairs that
// are one thing, and similar pairs that are not.
type Labels struct {
	Duplicates []Pair `json:"duplicates"`
	Different  []Pair `json:"different"`
}

// CorpusLabels returns the labels with {host} replaced by host.
func CorpusLabels(host string) Labels {
	b, err := corpus.ReadFile("corpus/labels.json")
	if err != nil {
		panic(err)
	}
	b = []byte(strings.ReplaceAll(string(b), "{host}", strings.ToLower(host)))
	var l Labels
	if err := json.Unmarshal(b, &l); err != nil {
		panic(err)
	}
	return l
}
