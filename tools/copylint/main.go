// Command copylint checks the UI copy: every sentence has 20 words or less,
// and no string uses a banned word.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"
)

const maxWords = 20

// banned are the default banned words of the writing lint.
var banned = []string{
	"leverage", "utilize", "robust", "seamless", "delve", "crucial", "comprehensive",
	"holistic", "synergy", "cutting-edge", "game-changer", "in order to", "it is important to note",
}

// Finding is one failed rule.
type Finding struct {
	Key  string
	Rule string
	Text string
}

var sentenceEnd = regexp.MustCompile(`[.!?](\s+|$)`)

// Lint checks the strings of a nested JSON copy file.
func Lint(data []byte) ([]Finding, error) {
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	flat := map[string]string{}
	if err := flatten("", root, flat); err != nil {
		return nil, err
	}
	keys := make([]string, 0, len(flat))
	for k := range flat {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var out []Finding
	for _, k := range keys {
		out = append(out, LintString(k, flat[k])...)
	}
	return out, nil
}

// LintString checks one string.
func LintString(key, s string) []Finding {
	var out []Finding
	for _, sentence := range sentenceEnd.Split(s, -1) {
		if n := len(strings.Fields(sentence)); n > maxWords {
			out = append(out, Finding{Key: key, Rule: fmt.Sprintf("sentence has %d words, the limit is %d", n, maxWords), Text: sentence})
		}
	}
	lower := strings.ToLower(s)
	for _, b := range banned {
		re := regexp.MustCompile(`\b` + regexp.QuoteMeta(b) + `\b`)
		if re.MatchString(lower) {
			out = append(out, Finding{Key: key, Rule: "banned word: " + b, Text: s})
		}
	}
	return out
}

func flatten(prefix string, v any, out map[string]string) error {
	switch t := v.(type) {
	case string:
		out[prefix] = t
	case map[string]any:
		for k, child := range t {
			p := k
			if prefix != "" {
				p = prefix + "." + k
			}
			if err := flatten(p, child, out); err != nil {
				return err
			}
		}
	default:
		return fmt.Errorf("%s: copy values must be strings or objects", prefix)
	}
	return nil
}

func main() {
	if len(os.Args) != 2 {
		fmt.Fprintln(os.Stderr, "usage: copylint <en.json>")
		os.Exit(2)
	}
	data, err := os.ReadFile(os.Args[1])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(2)
	}
	findings, err := Lint(data)
	if err != nil {
		fmt.Fprintln(os.Stderr, "copylint:", err)
		os.Exit(1)
	}
	for _, f := range findings {
		fmt.Printf("%s: %s: %q\n", f.Key, f.Rule, f.Text)
	}
	if len(findings) > 0 {
		os.Exit(1)
	}
}
