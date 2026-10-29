package content

import "time"

// Item is one fetched source object with a normalized Markdown body.
type Item struct {
	ExternalID string
	Kind       string // page, issue or file
	Title      string
	URL        string
	Author     string
	EditedBy   string
	Created    time.Time
	Updated    time.Time
	Body       string
	Metadata   map[string]any
	// Entity is the thing that the item is, such as an issue or a
	// decision. Nil when the item is only a document.
	Entity *EntityRef
	// Facts are the deterministic claims of the item.
	Facts []Fact
}

// EntityRef names an entity by its provider-qualified key.
type EntityRef struct {
	Type    string   `json:"type"`
	Key     string   `json:"key"`
	Name    string   `json:"name"`
	Aliases []string `json:"aliases,omitempty"`
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
}
