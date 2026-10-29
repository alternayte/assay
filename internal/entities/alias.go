// Package entities holds entities: keys, aliases, profiles, merges and
// traversal.
package entities

import (
	"fmt"
	"slices"
	"strings"
	"unicode"

	"golang.org/x/text/unicode/norm"

	"github.com/alternayte/assay/internal/claims"
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

// Providers are the key prefixes of entities (REQ-ENT-002).
var Providers = []string{"registry", "backstage", "jira", "confluence", "github", "azdo"}

// CheckRef reports whether an entity type and key are valid: a known type
// and a provider-qualified key.
func CheckRef(typ, key string) error {
	if !slices.Contains(claims.EntityTypes, typ) {
		return fmt.Errorf("entities: unknown entity type %q", typ)
	}
	provider, rest, ok := strings.Cut(key, ":")
	if !ok || strings.TrimSpace(rest) == "" || !slices.Contains(Providers, provider) {
		return fmt.Errorf("entities: the key %q is not provider-qualified", key)
	}
	return nil
}
