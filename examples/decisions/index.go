package decisions

import "sync"

// ReferenceIndex is the downstream reference index the register keeps in step
// through IndexReferences.
//
// The effect names the rows in the application's own table that hold a
// decision's text — the question, and the rationale once one is recorded — and
// the shell reads them (Content.IndexedText) before handing the index the
// words. The text therefore never passes through the ledger, and the index is
// told only the reference that names it.
//
// The outbox delivers effects at least once and not in order, so the effect
// from an older message can be retried after a newer one has been applied; a
// consumer that simply stores the text it is handed goes back to text the
// decision no longer has. Every delivery carries the seq of the entry that
// produced the effect, so the index keeps the seq of the text it holds and
// ignores a delivery that is older. Text is absolute, so applying the same
// entry again is harmless. A decision's ID is the stream it is recorded on, so
// the seq kept for it is counted on the one stream that writes its text.
type ReferenceIndex struct {
	mu   sync.Mutex
	text map[string]string
	seq  map[string]int64
}

// NewReferenceIndex returns an empty index.
func NewReferenceIndex() *ReferenceIndex {
	return &ReferenceIndex{text: map[string]string{}, seq: map[string]int64{}}
}

// Apply records the text for one decision from one delivery. seq is the seq of
// the entry that produced the effect; a delivery older than the text already
// held is ignored, so a retried effect cannot overwrite newer text.
func (x *ReferenceIndex) Apply(seq int64, decision, text string) {
	x.mu.Lock()
	defer x.mu.Unlock()
	if held, ok := x.seq[decision]; ok && seq < held {
		return
	}
	x.seq[decision] = seq
	x.text[decision] = text
}

// Text returns the text the index holds for a decision.
func (x *ReferenceIndex) Text(decision string) string {
	x.mu.Lock()
	defer x.mu.Unlock()
	return x.text[decision]
}
