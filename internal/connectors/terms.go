package connectors

import (
	"regexp"
	"strings"
	"unicode"

	"github.com/alternayte/assay/internal/content"
	"github.com/alternayte/assay/internal/entities"
)

// TermKey is the key of a term that a connection defines.
func TermKey(provider, host, name string) string {
	return provider + ":" + strings.ToLower(host) + ":term:" + strings.ReplaceAll(entities.NormalizeAlias(name), " ", "-")
}

func cells(line string) []string {
	line = strings.TrimSpace(line)
	if !strings.HasPrefix(line, "|") || !strings.HasSuffix(line, "|") {
		return nil
	}
	parts := strings.Split(line[1:len(line)-1], "|")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return parts
}

// GlossaryFacts reads the Markdown tables with a Term and a Definition
// column. Each row gives a term entity with a defined_as fact; the row is
// the quote (REQ-VOC-002).
func GlossaryFacts(body, provider, host string) []content.Fact {
	var out []content.Fact
	term, def := -1, -1
	for _, line := range strings.Split(body, "\n") {
		cs := cells(line)
		if cs == nil {
			term, def = -1, -1
			continue
		}
		if term < 0 {
			for i, c := range cs {
				switch strings.ToLower(c) {
				case "term":
					term = i
				case "definition":
					def = i
				}
			}
			if term < 0 || def < 0 {
				term, def = -1, -1
			}
			continue
		}
		if strings.Trim(strings.Join(cs, ""), "-: ") == "" || max(term, def) >= len(cs) || cs[term] == "" || cs[def] == "" {
			continue
		}
		subject := content.EntityRef{Type: "term", Key: TermKey(provider, host, cs[term]), Name: cs[term]}
		out = append(out, content.Fact{Subject: subject, Predicate: "defined_as", Text: cs[def], Quote: strings.TrimSpace(line)})
	}
	return out
}

var acronymRe = regexp.MustCompile(`\b((?:\p{Lu}[\p{L}\p{N}-]*\s+){1,5}\p{Lu}[\p{L}\p{N}-]*)\s*\((\p{Lu}{2,6})\)`)

// Acronyms reads "Full Name (ACRONYM)" where the acronym is the initials of
// the words. Each gives a term entity with the acronym as an alias.
func Acronyms(body, provider, host string) []content.EntityRef {
	var out []content.EntityRef
	seen := map[string]bool{}
	for _, m := range acronymRe.FindAllStringSubmatch(body, -1) {
		words := strings.Fields(m[1])
		acr := m[2]
		// The name may start with words that are not part of the acronym,
		// such as "The".
		if len(words) < len(acr) {
			continue
		}
		words = words[len(words)-len(acr):]
		ok := true
		for i, w := range words {
			if unicode.ToUpper([]rune(w)[0]) != rune(acr[i]) {
				ok = false
			}
		}
		name := strings.Join(words, " ")
		if !ok || seen[name] {
			continue
		}
		seen[name] = true
		out = append(out, content.EntityRef{Type: "term", Key: TermKey(provider, host, name), Name: name, Aliases: []string{acr}})
	}
	return out
}
