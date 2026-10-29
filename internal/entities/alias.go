// Package entities holds entities: keys, aliases, profiles, merges and
// traversal.
package entities

import (
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"
)

// NormalizeAlias lowercases an alias, replaces punctuation and symbols with
// spaces and collapses the spaces (REQ-ENT-002). Text objects of claims use
// the same form for comparison.
func NormalizeAlias(s string) string {
	s = norm.NFKC.String(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case unicode.IsPunct(r) || unicode.IsSymbol(r):
			b.WriteRune(' ')
		default:
			b.WriteRune(unicode.ToLower(r))
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}
