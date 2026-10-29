// Package claims holds the predicate registry: the subject and object types,
// the cardinality, the time scope and the default source priority of each
// predicate. The registry is code because a change alters the meaning of
// stored claims.
package claims

import (
	"fmt"
	"slices"
)

// EntityTypes are the entity types (REQ-ENT-001).
var EntityTypes = []string{"person", "team", "service", "api", "system", "resource", "repository", "project",
	"initiative", "issue", "document", "decision", "environment", "term"}

// Object kinds that are not entity types.
const (
	ObjText = "text"
	ObjDate = "date"
	ObjAny  = "any"
)

// Predicate is one registry entry.
type Predicate struct {
	Name       string
	Subjects   []string // entity types, or ObjAny
	Objects    []string // entity types, ObjText, ObjDate or ObjAny
	Multi      bool
	TimeScoped bool
	// Priority is the default source priority. "others" stands for the
	// remaining source types in no order.
	Priority []string
	// NeverConflicts is set for stated.
	NeverConflicts bool
}

// Source types of evidence.
var SourceTypes = []string{"registry", "catalog", "codeowners", "jira", "confluence", "repository", "review"}

var owners = []string{"service", "api", "system", "resource", "repository", "project", "initiative", "term"}

// Registry is the predicate registry (SDD section 10).
var Registry = map[string]Predicate{}

func init() {
	for _, p := range []Predicate{
		{Name: "owned_by", Subjects: owners, Objects: []string{"team", "person"}, Priority: []string{"registry", "catalog", "codeowners", "jira", "others"}},
		{Name: "part_of", Subjects: []string{"service", "api", "resource", "issue", "initiative"}, Objects: []string{"system", "initiative", "project"},
			Priority: []string{"registry", "catalog", "jira", "others"}},
		{Name: "depends_on", Subjects: []string{"service", "api", "resource"}, Objects: []string{"service", "api", "resource"}, Multi: true,
			Priority: []string{"registry", "catalog", "others"}},
		{Name: "provides_api", Subjects: []string{"service"}, Objects: []string{"api"}, Multi: true, Priority: []string{"registry", "catalog", "others"}},
		{Name: "consumes_api", Subjects: []string{"service"}, Objects: []string{"api"}, Multi: true, Priority: []string{"registry", "catalog", "others"}},
		{Name: "implements", Subjects: []string{"repository"}, Objects: []string{"service", "api"}, Multi: true, Priority: []string{"catalog", "others"}},
		{Name: "deployed_to", Subjects: []string{"service"}, Objects: []string{"environment"}, Multi: true, Priority: []string{"repository", "others"}},
		{Name: "blocks", Subjects: []string{"issue", "initiative"}, Objects: []string{"issue", "initiative"}, Multi: true, Priority: []string{"jira", "others"}},
		{Name: "assigned_to", Subjects: []string{"issue", "initiative"}, Objects: []string{"person"}, Priority: []string{"jira"}},
		{Name: "status", Subjects: []string{"issue", "initiative", "decision", "service", "term"}, Objects: []string{ObjText}, TimeScoped: true,
			Priority: []string{"registry", "jira", "catalog", "others"}},
		{Name: "target_date", Subjects: []string{"issue", "initiative"}, Objects: []string{ObjDate}, TimeScoped: true, Priority: []string{"jira", "others"}},
		{Name: "decided", Subjects: []string{"decision"}, Objects: []string{ObjText}, Priority: []string{"others"}},
		{Name: "rationale", Subjects: []string{"decision"}, Objects: []string{ObjText}, Multi: true, Priority: []string{"others"}},
		{Name: "supersedes", Subjects: []string{"decision"}, Objects: []string{"decision"}, Multi: true, Priority: []string{"others"}},
		{Name: "defined_as", Subjects: []string{"term"}, Objects: []string{ObjText}, Priority: []string{"registry", "confluence", "others"}},
		{Name: "preferred_term", Subjects: []string{"term"}, Objects: []string{"term"}, Priority: []string{"registry", "others"}},
		{Name: "broader", Subjects: []string{"term"}, Objects: []string{"term"}, Multi: true, Priority: []string{"registry", "others"}},
		{Name: "relates_to", Subjects: []string{"term"}, Objects: []string{ObjAny}, Multi: true, Priority: []string{"registry", "others"}},
		{Name: "stated", Subjects: []string{ObjAny}, Objects: []string{ObjText}, Multi: true, Priority: []string{"others"}, NeverConflicts: true},
	} {
		Registry[p.Name] = p
	}
}

// Check validates a claim against the registry. objType is an entity type,
// or ObjText or ObjDate for a text object.
func Check(predicate, subjectType, objType string) error {
	p, ok := Registry[predicate]
	if !ok {
		return fmt.Errorf("claims: unknown predicate %q", predicate)
	}
	if !slices.Contains(EntityTypes, subjectType) {
		return fmt.Errorf("claims: unknown entity type %q", subjectType)
	}
	if !slices.Contains(p.Subjects, ObjAny) && !slices.Contains(p.Subjects, subjectType) {
		return fmt.Errorf("claims: %s does not take a %s subject", predicate, subjectType)
	}
	switch {
	case slices.Contains(p.Objects, ObjAny):
		return nil
	case slices.Contains(p.Objects, objType):
		return nil
	case objType == ObjDate && slices.Contains(p.Objects, ObjText):
		return nil
	}
	return fmt.Errorf("claims: %s does not take a %s object", predicate, objType)
}
