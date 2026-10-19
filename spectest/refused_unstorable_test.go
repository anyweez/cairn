package spectest_test

import (
	"strings"
	"testing"

	"pgregory.net/rapid"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/spectest"
)

// note is a message the ledger cannot always store: a Text field with bytes
// that are not valid UTF-8 makes codec.Encode fail with a NotStorableError.
type note struct{ Text string }

type noteMsg interface{ isNote() }

func (note) isNote() {}

func noteCodecs() *codec.Set[noteMsg, int, int, struct{}] {
	msgs := codec.New[noteMsg]()
	codec.Register[note](msgs, "note", 1)
	return &codec.Set[noteMsg, int, int, struct{}]{Messages: msgs}
}

// noteMachine refuses a message the ledger cannot store, exactly as
// examples/decisions does (`codec.CheckStorable(m)` in decide). The ledger
// supports this: queueEntry returns the decider's refusal and records no
// entry when Encode rejects the message.
func noteMachine() spectest.Machine[int, noteMsg, int, int, struct{}] {
	return spectest.Machine[int, noteMsg, int, int, struct{}]{
		Decider: cairn.Decider[int, noteMsg, int, int, struct{}]{
			Initial: func() int { return 0 },
			Decide: func(n int, m noteMsg) cairn.Decision[int, int, struct{}] {
				if !cairn.StorableText(m.(note).Text) {
					return cairn.Refuse[int, int, struct{}]("invalid", "text cannot be stored")
				}
				return cairn.Accept[int, int, struct{}](n+1, []int{1})
			},
			Evolve: func(n int, _ int) int { return n + 1 },
		},
		Messages: func(t *rapid.T, _ int) noteMsg {
			return rapid.SampledFrom([]noteMsg{note{Text: "ok"}, note{Text: "bad\xff"}}).Draw(t, "note")
		},
		Codecs: noteCodecs(),
	}
}

// A decider that refuses a message its codec cannot encode is correct, and
// the ledger answers that refusal without recording anything. The checker
// must not fail it: D6 only means something for a message that is recorded,
// and an unstorable message is not.
func TestRefusedUnstorableMessagePassesD6(t *testing.T) {
	if msg, failed := spectest.Fails(t, noteMachine().Check); failed {
		if strings.Contains(msg, spectest.Encodable.ID) {
			t.Fatalf("a correct machine that refuses an unstorable message was failed by %s:\n%s",
				spectest.Encodable.ID, msg)
		}
		t.Fatalf("the machine failed for some other reason:\n%s", msg)
	}
}

// The other half of the rule: a message the decider accepts is recorded, so
// it must be storable. D6 still fails an accepted message the codec rejects.
func TestAcceptedUnstorableMessageFailsD6(t *testing.T) {
	m := noteMachine()
	m.Decider.Decide = func(n int, _ noteMsg) cairn.Decision[int, int, struct{}] {
		return cairn.Accept[int, int, struct{}](n+1, []int{1})
	}
	msg, failed := spectest.Fails(t, m.Check)
	if !failed || !strings.Contains(msg, spectest.Encodable.ID) {
		t.Fatalf("an accepted unstorable message should fail %s, got failed=%v:\n%s",
			spectest.Encodable.ID, failed, msg)
	}
}
