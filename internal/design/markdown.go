package design

import (
	"fmt"
	"strings"
)

// Markdown returns the design page as Markdown. Footnotes cite the source
// of each marker: the file and line range of a passage, or the claim.
// Diagrams are Mermaid code blocks.
func (p *DesignPage) Markdown(base string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# %s: design\n\n", oneLine(p.Name))
	if p.Level == "system" {
		fmt.Fprintf(&b, "The design of the %s %s from the design pages of its repositories.\n\n", p.Type, oneLine(p.Name))
	}
	notes := map[string]int{}
	var defs []string
	ref := func(c DesignCite) string {
		k := fmt.Sprintf("%s|%d|%s|%d", c.Kind, c.ItemID, c.Hash, c.ClaimID)
		if n, ok := notes[k]; ok {
			return fmt.Sprintf("[^%d]", n)
		}
		n := len(notes) + 1
		notes[k] = n
		var d string
		switch {
		case c.Kind == "claim":
			d = "Claim: " + oneLine(c.Text) + "."
		case c.Start > 0:
			d = fmt.Sprintf("%s, lines %d to %d. %s", oneLine(c.Title), c.Start, c.End, c.URL)
		default:
			d = fmt.Sprintf("%s. %s", oneLine(c.Title), c.URL)
		}
		if c.Stale {
			d += " This passage is no longer in the source."
		}
		defs = append(defs, fmt.Sprintf("[^%d]: %s", n, d))
		return fmt.Sprintf("[^%d]", n)
	}
	for _, s := range p.Sections {
		fmt.Fprintf(&b, "## %s\n\n", oneLine(s.Title))
		if s.RepositoryKey != "" {
			fmt.Fprintf(&b, "Design page: %s/e/%s/design\n\n", base, s.RepositoryKey)
		}
		byID := map[string]DesignCite{}
		for _, c := range s.Cites {
			byID[c.ID] = c
		}
		switch {
		case s.State == StateMissing && s.Markdown == "":
			b.WriteString("Assay has not written this section yet.\n\n")
		default:
			text := markerOf.ReplaceAllStringFunc(s.Markdown, func(m string) string {
				if c, ok := byID[m[1:len(m)-1]]; ok {
					return ref(c)
				}
				return ""
			})
			if text != "" {
				b.WriteString(text + "\n\n")
			}
			if s.Pinned {
				b.WriteString("A person corrected this section.\n\n")
			}
		}
		if s.Diagram != "" {
			b.WriteString("```mermaid\n" + s.Diagram + "\n```\n\n")
			var srcs []string
			for _, m := range s.DiagramCites {
				if c, ok := byID[m]; ok {
					srcs = append(srcs, ref(c))
				}
			}
			if len(srcs) > 0 {
				b.WriteString("Diagram sources: " + strings.Join(srcs, " ") + "\n\n")
			}
		}
	}
	if len(defs) > 0 {
		b.WriteString(strings.Join(defs, "\n") + "\n")
	}
	return b.String()
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }
