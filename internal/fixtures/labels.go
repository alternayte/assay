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
// are one thing, similar pairs that are not, and labelled mentions.
type Labels struct {
	Duplicates []Pair `json:"duplicates"`
	Different  []Pair `json:"different"`
	// Links are mentions in an item that mean a known entity.
	Links []Mention `json:"links"`
	// Unnamed are description mentions of things that no entity is.
	Unnamed []Mention `json:"unnamed"`
}

// Mention is a labelled mention in an item of the corpus.
type Mention struct {
	Item    string `json:"item" doc:"The external ID of the item."`
	Mention string `json:"mention"`
	Entity  string `json:"entity,omitempty"`
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
