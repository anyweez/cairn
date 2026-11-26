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
// Erasing a person means deleting the rows that name them — everything they
// wrote, and the mapping from their name to their id. A text row also records
// the slot it belongs to (a decision's question, one member's position on it)
// and the member whose request stored it, so deleting the row a state names
// releases the versions it replaced, and forgetting a member releases every
// row they wrote. The ledger's history still loads and verifies, because it
// only ever held the id. See docs/REDACTION.md.
type Content struct {
	mu    sync.Mutex
	newID func(prefix string) string

	text  map[Ref]string
	names map[Member]string
	ids   map[string]Member

	// owner records what names each row of text: the slot the state names it
	// under, and the member whose request stored it. Both are how a row is
	// found again; see Delete and Forget.
	owner map[Ref]rowOwner
	// held is the member row each request using it is holding, so a name
	// minted for a request that records nothing is released when the last
	// request holding it finishes.
	held map[Member]*memberHold
}

// rowOwner is what names a row of text: the slot whose state names it, and the
// member whose request stored it.
type rowOwner struct {
	slot   string
	member Member
}

// memberHold is one member mapping while requests are using it. A row no
// recorded request has ever named is released when users reaches zero; one a
// recorded entry names is kept for good.
type memberHold struct {
	name    string
	users   int
	durable bool
}

// NewContent returns an empty table.
func NewContent() *Content {
	return &Content{
		// Random, so two processes sharing a table never mint the same id.
		newID: func(prefix string) string { return prefix + rand.Text()[:16] },
		text:  map[Ref]string{},
		names: map[Member]string{},
		ids:   map[string]Member{},
		owner: map[Ref]rowOwner{},
		held:  map[Member]*memberHold{},
	}
}

// Put stores text and returns the ref that names it. Text that is only
// whitespace is stored under no ref, so a message carrying "" is refused by
// the decider the way a blank title always was.
func (c *Content) Put(text string) Ref {
	return c.put("", "", text)
}

// PutFor stores text and returns the ref that names it, recording the slot the
// decision's state will name it under and the member whose request stored it.
// A stored message goes through here; Put is for a row no decision names.
func (c *Content) PutFor(slot string, by Member, text string) Ref {
	return c.put(slot, by, text)
}

func (c *Content) put(slot string, by Member, text string) Ref {
	text = strings.TrimSpace(text)
	if text == "" {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	ref := Ref(c.newID("t-"))
	c.text[ref] = text
	if slot != "" {
		c.owner[ref] = rowOwner{slot: slot, member: by}
	}
	return ref
}

// Text returns the text a ref names, or "" when the row has been deleted.
func (c *Content) Text(ref Ref) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.text[ref]
}

// IndexedText returns the text the reference index answers with for a
// decision: its question, and the rationale once an outcome is recorded. The
// index keeps the refs IndexReferences delivered and resolves them through
// here when it is read, so a deleted row contributes nothing.
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

// Delete removes rows of text, along with the rows they replaced. It is
// ordinary deletion, and it is the reason the words are kept here: the
// ledger's entries still name the refs and the stream still loads and
// verifies, because it never held what was deleted.
//
// A ref minted for a slot releases every row that slot has held — the old
// question an edit replaced, the earlier body a member overwrote — so deleting
// the row a decision's current state names erases the text it superseded too.
func (c *Content) Delete(refs ...Ref) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ref := range refs {
		c.release(ref)
	}
}

// release deletes ref and, when it belongs to a slot, every row that slot
// holds. c.mu must be held.
func (c *Content) release(ref Ref) {
	if ref == "" {
		return
	}
	owner, ok := c.owner[ref]
	delete(c.text, ref)
	delete(c.owner, ref)
	if !ok || owner.slot == "" {
		return
	}
	for other, o := range c.owner {
		if o.slot == owner.slot {
			delete(c.text, other)
			delete(c.owner, other)
		}
	}
}

// discard removes exactly the rows a request stored for a message it did not
// record. It does not release their slots: an earlier row in the same slot is
// still named by the decision's state.
func (c *Content) discard(refs ...Ref) {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, ref := range refs {
		if ref != "" {
			delete(c.text, ref)
			delete(c.owner, ref)
		}
	}
}

// Member returns the opaque id for a person's name, minting one on first
// sight. A name that is only whitespace names nobody, and gets "", which the
// decider refuses.
func (c *Content) Member(name string) Member {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, _ := c.mint(name)
	return id
}

// resolve returns the member id a request names as its sender and holds the
// row until done is called for it, so a row minted for a request that records
// nothing can be released once the request is over.
func (c *Content) resolve(name string) Member {
	c.mu.Lock()
	defer c.mu.Unlock()
	id, _ := c.mint(name)
	if id != "" {
		c.held[id].users++
	}
	return id
}

// mint returns the id for name, minting it on first sight, and reports whether
// it minted it. c.mu must be held.
func (c *Content) mint(name string) (Member, bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return "", false
	}
	if id, ok := c.ids[name]; ok {
		if c.held[id] == nil {
			c.held[id] = &memberHold{name: name}
		}
		return id, false
	}
	id := Member(c.newID("m-"))
	c.ids[name] = id
	c.names[id] = name
	c.held[id] = &memberHold{name: name}
	return id, true
}

// done finishes one request's hold on a member row. A row a recorded request
// has used — a success, a recorded refusal, or a ledger error that leaves the
// outcome open — is kept for good; a row only unrecorded requests have held is
// released when the last of them finishes.
func (c *Content) done(id Member, recorded bool) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	row, ok := c.held[id]
	if !ok {
		return
	}
	if recorded {
		row.durable = true
	}
	row.users--
	if row.users > 0 || row.durable {
		return
	}
	delete(c.ids, row.name)
	delete(c.names, id)
	delete(c.held, id)
}

// Name returns the name a member id was minted for, or "" once it is
// forgotten.
func (c *Content) Name(id Member) string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.names[id]
}

// Forget erases a person: it deletes every row of text their requests stored,
// the link from their name to their id, and the name itself. The ledger is
// untouched and keeps the member id it always had, which is the arrangement
// docs/REDACTION.md describes.
func (c *Content) Forget(id Member) {
	if id == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if name, ok := c.names[id]; ok {
		delete(c.ids, name)
		delete(c.names, id)
	}
	delete(c.held, id)
	for ref, owner := range c.owner {
		if owner.member == id {
			delete(c.text, ref)
			delete(c.owner, ref)
		}
	}
}
