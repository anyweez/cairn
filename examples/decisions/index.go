package decisions

import "sync"

// ReferenceIndex is the downstream reference index the register keeps in step
// through IndexReferences.
//
// The effect names the rows in the application's own table that hold a
// decision's text — the question, and the rationale once one is recorded — and
// the index keeps those refs, not the words. It reads them through Content
// when it is asked, so it holds no copy of the text: deleting a row to honour
// a deletion request removes it from the index in the same step it removes it
// from every other reader of that table. See docs/REDACTION.md.
//
// The outbox delivers effects at least once and not in order, so the effect
// from an older message can be retried after a newer one has been applied; a
// consumer that simply stores what it is handed goes back to refs the decision
// no longer has. Every delivery carries the seq of the entry that produced the
// effect, so the index keeps the seq of the refs it holds and ignores a
// delivery that is older. The effect is absolute, so applying the same entry
// again is harmless. A decision's ID is the stream it is recorded on, so the
// seq kept for it is counted on the one stream that writes its text.
type ReferenceIndex struct {
	content *Content

	mu   sync.Mutex
	refs map[string]IndexReferences
	seq  map[string]int64
}

// NewReferenceIndex returns an empty index over the register's own table,
// which is where the words live.
func NewReferenceIndex(content *Content) *ReferenceIndex {
	return &ReferenceIndex{
		content: content,
		refs:    map[string]IndexReferences{},
		seq:     map[string]int64{},
	}
}

// Apply records the rows one delivery names for a decision. seq is the seq of
// the entry that produced the effect; a delivery older than the text already
// held is ignored, so a retried effect cannot overwrite newer text.
func (x *ReferenceIndex) Apply(seq int64, ir IndexReferences) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if held, ok := x.seq[ir.Decision]; ok && seq < held {
		return
	}
	x.seq[ir.Decision] = seq
	x.refs[ir.Decision] = ir
}

// Text returns the text the index answers with for a decision, read from the
// rows the newest delivery named. A row deleted since resolves to nothing, so
// an erased decision has no text here rather than the ref that named it.
func (x *ReferenceIndex) Text(decision string) string {
	x.mu.Lock()
	ir, ok := x.refs[decision]
	x.mu.Unlock()
	if !ok {
		return ""
	}
	return x.content.IndexedText(ir.Title, ir.Rationale)
}
