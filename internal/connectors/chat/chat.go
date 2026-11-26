// Package chat builds the items of chat sources, Microsoft Teams and Slack:
// one item for each thread, and one passage for each message. Each author
// becomes a person entity with a posted_in fact.
package chat

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/alternayte/assay/internal/content"
)

// Kind is the item kind of a thread.
const Kind = "thread"

// titleChars is the length of the root message part of a title.
const titleChars = 80

// Message is one kept message of a thread: not a bot, not a join or leave
// event, and with text.
type Message struct {
	AuthorID string
	// Author is the display name of the author.
	Author string
	// Aliases are other names of the author, such as a user name.
	Aliases []string
	Sent    time.Time
	// Markdown is the normalized Markdown of the message text.
	Markdown string
}

// Thread is a root message and its replies.
type Thread struct {
	ExternalID string
	// Channel is the channel name as people see it, such as #payments.
	Channel string
	URL     string
	// Revision is the newest reply time in the form of the source.
	Revision string
	// PersonPrefix is the key of an author without the user ID, such as
	// slack:T0ACME:user:.
	PersonPrefix string
	// Messages are the kept messages, the root first.
	Messages []Message
}

// Newest returns the time of the newest message.
func Newest(ms []Message) time.Time {
	var t time.Time
	for _, m := range ms {
		if m.Sent.After(t) {
			t = m.Sent
		}
	}
	return t
}

// Header is the start of the passage of a message: the author and the
// date, such as "Dana Ito, 2026-05-04". A claim from chat needs who said it
// and when.
func Header(m Message) string {
	name := strings.TrimSpace(m.Author)
	if name == "" {
		name = m.AuthorID
	}
	return escape(name) + ", " + m.Sent.UTC().Format("2006-01-02")
}

// escape keeps a display name as plain text in Markdown.
func escape(s string) string {
	var b strings.Builder
	for _, r := range s {
		if strings.ContainsRune("\\`*_[]<>#|~!", r) {
			b.WriteByte('\\')
		}
		b.WriteRune(r)
	}
	return b.String()
}

// text is the Markdown of the passage of a message.
func text(m Message) string {
	body := strings.TrimSpace(m.Markdown)
	first, _, _ := strings.Cut(body, "\n")
	if inline(first) {
		return Header(m) + ": " + body
	}
	return Header(m) + ":\n\n" + body
}

// inline reports whether the first line of a message is paragraph text, so
// that the header can start the same paragraph.
func inline(line string) bool {
	l := strings.TrimLeft(line, " ")
	for _, p := range []string{"```", "~~~", "- ", "* ", "+ ", "> ", "#", "|"} {
		if strings.HasPrefix(l, p) {
			return false
		}
	}
	if i := strings.IndexAny(l, ".)"); i > 0 && i < 10 && strings.Trim(l[:i], "0123456789") == "" {
		return false
	}
	return true
}

// Title is the channel name and the first 80 characters of the root
// message.
func Title(channel string, root Message) string {
	t := content.Plain(root.Markdown)
	if utf8.RuneCountInString(t) > titleChars {
		r := []rune(t)
		t = strings.TrimSpace(string(r[:titleChars])) + "…"
	}
	if t == "" {
		return channel
	}
	return channel + ": " + t
}

// Item returns the item of a thread. It returns nil for a thread without
// kept messages.
func (t Thread) Item() *content.Item {
	if len(t.Messages) == 0 {
		return nil
	}
	root, last := t.Messages[0], t.Messages[len(t.Messages)-1]
	title := Title(t.Channel, root)
	texts := make([]string, len(t.Messages))
	for i, m := range t.Messages {
		texts[i] = text(m)
	}
	it := &content.Item{ExternalID: t.ExternalID, Kind: Kind, Title: title, URL: t.URL, Author: root.Author, EditedBy: last.Author,
		Created: root.Sent, Updated: Newest(t.Messages), Body: strings.Join(texts, "\n\n"), Passages: content.MessagePassages(texts),
		Revision: t.Revision, Metadata: map[string]any{"channel": t.Channel, "replies": len(t.Messages) - 1}}
	seen := map[string]bool{}
	for _, m := range t.Messages {
		if m.AuthorID == "" || seen[m.AuthorID] {
			continue
		}
		seen[m.AuthorID] = true
		p := content.EntityRef{Type: "person", Key: t.PersonPrefix + m.AuthorID, Name: strings.TrimSpace(m.Author), Aliases: m.Aliases}
		it.Entities = append(it.Entities, p)
		// The quote is the header of the first message of the author.
		it.Facts = append(it.Facts, content.Fact{Subject: p, Predicate: "posted_in", Text: title, Quote: content.Plain(Header(m))})
	}
	return it
}

// Options are the options of a chat connection.
type Options struct {
	// RetainDays removes threads whose newest message is older. 0 keeps
	// all threads.
	RetainDays int `json:"retainDays,omitempty"`
}

// ParseRetainDays reads retainDays of the options of a connection.
func ParseRetainDays(options json.RawMessage) (int, error) {
	var o struct {
		RetainDays *json.Number `json:"retainDays"`
	}
	if len(options) > 0 {
		if err := json.Unmarshal(options, &o); err != nil {
			return 0, fmt.Errorf("chat: the options are not valid: %w", err)
		}
	}
	if o.RetainDays == nil {
		return 0, nil
	}
	n, err := o.RetainDays.Int64()
	if err != nil || n < 0 || n > 36500 {
		return 0, errors.New("chat: retainDays is a whole number from 0 to 36500")
	}
	return int(n), nil
}

// Retention keeps the threads whose newest message is newer than the limit.
type Retention struct {
	Days int
	Now  func() time.Time
}

// Keep reports whether a thread with its newest message at newest stays.
func (r Retention) Keep(newest time.Time) bool {
	if r.Days <= 0 {
		return true
	}
	now := time.Now
	if r.Now != nil {
		now = r.Now
	}
	return !newest.Before(now().AddDate(0, 0, -r.Days))
}

// Split cuts an external ID of the form <a>/<b>/<c> into its three parts.
func Split(id string) (string, string, string, bool) {
	a, rest, ok := strings.Cut(id, "/")
	if !ok {
		return "", "", "", false
	}
	b, c, ok := strings.Cut(rest, "/")
	if !ok || a == "" || b == "" || c == "" || strings.Contains(c, "/") {
		return "", "", "", false
	}
	return a, b, c, true
}
