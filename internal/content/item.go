package content

import "time"

// Item is one fetched source object with a normalized Markdown body.
type Item struct {
	ExternalID string
	Kind       string // page, issue, file, code, pull_request or work_item
	Title      string
	URL        string
	Author     string
	EditedBy   string
	Created    time.Time
	Updated    time.Time
	Body       string
	// Passages are the passages of a source code file. Nil means that
	// Store splits the Markdown body.
	Passages []Passage
	// Revision is the version of the item in its source, such as the blob
	// ID of a repository file. Sync skips a file whose revision is stored.
	Revision string
	// Part is the scope unit of the connection that lists the item, such as
	// a space or a repository. Reconciliation of a part tombstones only the
	// items of that part.
	Part     string
	Metadata map[string]any
	// Entity is the thing that the item is, such as an issue or a
	// decision. Nil when the item is only a document.
	Entity *EntityRef
	// Entities are other things that the item defines, such as the
	// documents of a catalog file.
	Entities []EntityRef
	// Facts are the deterministic claims of the item.
	Facts []Fact
	// Origin is the origin of new claims of the facts. Empty means source.
	// Registry records set human.
	Origin string
	// DropStale deletes the evidence of the item that the new version no
	// longer supports and retracts claims left without evidence. Registry
	// records set it: a removed field is no longer a fact.
	DropStale bool
}

// EntityRef names an entity by its provider-qualified key.
type EntityRef struct {
	Type    string   `json:"type"`
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
	// Metadata are source facts about the entity that are not claims, such
	// as the email of a person. They merge into the stored metadata.
	Metadata map[string]any `json:"metadata,omitempty"`
	// KeepType keeps the type of a stored entity with the key. A reference
	// to an entity that another source owns, such as a Jira issue that a
	// pull request names, sets it: the other source knows the type.
	KeepType bool `json:"keepType,omitempty"`
}

// Fact is one deterministic claim. The object is an entity or a text.
// Quote is an exact substring of the normalized text of one passage of the
// item; that passage is the field evidence.
type Fact struct {
	Subject   EntityRef
	Predicate string
	Object    *EntityRef
	Text      string
	Quote     string
	// Source is the evidence source type, such as catalog. Empty means the
	// type of the connection.
	Source string `json:",omitempty"`
	// Line is the source line of the quote, or 0. It selects the passage
	// when the same quote is in several passages.
	Line int `json:",omitempty"`
	// At dates a value of a time-scoped predicate, such as a status change
	// of a changelog. The claim is the one with valid_from At. The facts
	// with At of an item replace its earlier field evidence for their
	// subject and predicate. Zero keeps the newest value when it is
	// unchanged.
	At time.Time `json:",omitzero"`
}
