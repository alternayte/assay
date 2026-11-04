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
	"initiative", "issue", "document", "decision", "environment", "term", "operation", "event", "data_model"}

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
	// Extract is set for the predicates that prose extraction may use. The
	// extract schema and the extractor version read only these.
	Extract bool
}

// Source types of evidence. decision is the source of repo_system claims
// and of the edges that code proposes.
var SourceTypes = []string{"registry", "catalog", "codeowners", "jira", "confluence", "repository", "review", "decision"}

var owners = []string{"service", "api", "system", "resource", "repository", "project", "initiative", "term"}

// Registry is the predicate registry (SDD section 10).
var Registry = map[string]Predicate{}

func init() {
	for _, p := range []Predicate{
		{Name: "owned_by", Subjects: owners, Objects: []string{"team", "person"}, Priority: []string{"registry", "catalog", "codeowners", "jira", "others"}},
		{Name: "part_of", Subjects: []string{"service", "api", "resource", "issue", "initiative", "repository"},
			Objects: []string{"system", "initiative", "project"}, Priority: []string{"registry", "catalog", "jira", "others", "decision"}},
		{Name: "depends_on", Subjects: []string{"service", "api", "resource", "repository"}, Objects: []string{"service", "api", "resource", "repository"},
			Multi: true, Priority: []string{"registry", "catalog", "others", "decision"}},
		{Name: "provides_api", Subjects: []string{"service", "repository"}, Objects: []string{"api"}, Multi: true,
			Priority: []string{"registry", "catalog", "others", "decision"}},
		{Name: "consumes_api", Subjects: []string{"service", "repository"}, Objects: []string{"api"}, Multi: true,
			Priority: []string{"registry", "catalog", "others", "decision"}},
		{Name: "implements", Subjects: []string{"repository"}, Objects: []string{"service", "api"}, Multi: true,
			Priority: []string{"catalog", "registry", "others", "decision"}},
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
		p.Extract = true
		Registry[p.Name] = p
	}
	// Contracts come from spec files and from design digests, never from
	// prose extraction.
	contract := []string{"registry", "repository", "catalog", "others", "decision"}
	for _, p := range []Predicate{
		{Name: "has_operation", Subjects: []string{"api"}, Objects: []string{"operation"}, Multi: true, Priority: contract},
		{Name: "request", Subjects: []string{"operation"}, Objects: []string{"data_model"}, Priority: contract},
		{Name: "response", Subjects: []string{"operation"}, Objects: []string{"data_model"}, Multi: true, Priority: contract},
		{Name: "publishes", Subjects: []string{"service", "repository"}, Objects: []string{"event"}, Multi: true, Priority: contract},
		{Name: "subscribes_to", Subjects: []string{"service", "repository"}, Objects: []string{"event"}, Multi: true, Priority: contract},
		{Name: "payload", Subjects: []string{"event"}, Objects: []string{"data_model"}, Priority: contract},
		{Name: "channel", Subjects: []string{"event"}, Objects: []string{ObjText}, Priority: contract},
		{Name: "has_field", Subjects: []string{"data_model"}, Objects: []string{ObjText}, Multi: true, Priority: contract},
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

// CheckExtracted is Check for a claim from extraction. Only registry
// records and accepted reviews set the status of a term (REQ-VOC-001).
func CheckExtracted(predicate, subjectType, objType string) error {
	if err := Check(predicate, subjectType, objType); err != nil {
		return err
	}
	if predicate == "status" && subjectType == "term" {
		return fmt.Errorf("claims: extraction does not set the status of a term")
	}
	return nil
}

func errUnknownPredicate(p string) error { return fmt.Errorf("claims: unknown predicate %q", p) }

func errUnknownSourceType(t string) error { return fmt.Errorf("claims: unknown source type %q", t) }
