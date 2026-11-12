package decisions

import (
	"crypto/rand"
	"strings"
	"sync"
)

// Content is the register's own mutable table: the words of a decision, and
// the names behind the member ids. The ledger stores only the Ref or Member
// that names a row here, so a deletion request is an ordinary DELETE and the
// append-only log keeps nothing that needed erasing. In an application this is
// a table (or two) in its own schema, keyed by the same ids; here it is a map
// so the example runs without a second database.
//
// Erasing a person means deleting the rows that name them — their positions'
// text, and the mapping from their name to their id. The ledger's history still
// loads and verifies, because it only ever held the id. See docs/REDACTION.md.
type Content struct {
	mu    sync.Mutex
	newID func(prefix string) string

	text  map[Ref]string
	names map[Member]string
	ids   map[string]Member
}

// NewContent returns an empty table.
func NewContent() *Content {
	return &Content{
		// Random, so two processes sharing a table never mint the same id.
		newID: func(prefix string) string { return prefix + rand.Text()[:16] },
		text:  map[Ref]string{},
		names: map[Member]string{},
		ids:   map[string]Member{},
	}
}

// Put stores text and returns the ref that names it. Text that is only
// whitespace is stored under no ref, so a message carrying "" is refused by
// the decider the way a blank title always was.
func (c *Content) Put(text string) Ref {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ref := Ref(c.newID("t-"))
	c.text[ref] = text
	return ref
}

// Text returns the text a ref names, or "" when the row has been deleted.
func (c *Content) Text(ref Ref) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text[ref]
}

// IndexedText returns the text the reference index holds for a decision: its
// question, and the rationale once an outcome is recorded. It is how a
// delivery of IndexReferences becomes the text the index stores.
func (c *Content) IndexedText(title, rationale Ref) string {
	text := c.Text(title)
	if rationale == "" {
		return text
	}
	if rationaleText := c.Text(rationale); text == "" {
		return rationaleText
	} else if rationaleText != "" {
		return text + "\n" + rationaleText
	}
	return text
}

// Delete removes rows of text. It is ordinary deletion, and it is the reason
// the words are kept here: the ledger's entries still name the refs and the
// stream still loads and verifies, because it never held what was deleted.
func (c *Content) Delete(refs ...Ref) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ref := range refs {
		delete(c.text, ref)
	}
}

// Member returns the opaque id for a person's name, minting one on first
// sight. A name that is only whitespace names nobody, and gets "", which the
// decider refuses.
func (c *Content) Member(name string) Member {
	name = strings.TrimSpace(name)
	if name == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if id, ok := c.ids[name]; ok {
		return id
	}
	id := Member(c.newID("m-"))
	c.ids[name] = id
	c.names[id] = name
	return id
}

// Name returns the name a member id was minted for, or "" once it is
// forgotten.
func (c *Content) Name(id Member) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.names[id]
}

// Forget erases a person: it deletes the link from their name to their id, and
// the name itself. The ledger is untouched and keeps the member id it always
// had, which is the arrangement docs/REDACTION.md describes.
func (c *Content) Forget(id Member) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if name, ok := c.names[id]; ok {
		delete(c.ids, name)
		delete(c.names, id)
	}
}
