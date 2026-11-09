package decisions

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A sender that is only whitespace is not a member. decide refuses the empty
// sender ("a message needs a sender") and trims every other piece of text, so
// a title or a body of spaces is refused; a sender of spaces is taken at face
// value. The position is then filed under a member name that trims to nothing,
// and the entry the ledger records is attributed to the same blank name, so a
// caller whose name field held a stray space leaves a position nobody can
// address: Acknowledge looks it up by the name a person would type, and finds
// nothing.
//
// The assertions below do not say how the fix works. Refusing the message and
// normalising the sender to the member it names both satisfy them; what must
// not survive is a stored member or entry actor whose name is blank.
func TestABlankSenderIsNoMember(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()

	t.Run("decide", func(t *testing.T) {
		state := Decider.Initial()
		var d decision
		if d, state = Decider.Step(state, Create{ID: "d-1", By: "ana", At: at, Title: "ship it?",
			Options: []Option{{ID: "o1", Title: "yes"}}}); d.Refused() {
			t.Fatal(d.Refusal)
		}
		_, after := Decider.Step(state, SavePosition{By: "  \u00a0 ", Option: "o1", Body: "yes"})
		for member := range after.Positions {
			if strings.TrimSpace(member) == "" {
				t.Fatalf("a position was filed under member %q, which is blank: %+v", member, after.Positions)
			}
		}
	})

	// The same input through the shell and a ledger: the blank name also
	// becomes the actor the entry is attributed to, so the log says the message
	// was sent by nobody.
	t.Run("the entry's actor", func(t *testing.T) {
		ctx := context.Background()
		svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(true)))
		id, err := svc.Create(ctx, "ana", "ship it?", "yes")
		must(t, err)
		created, err := svc.Get(ctx, id)
		must(t, err)
		_ = svc.SavePosition(ctx, id, "  \u00a0 ", created.Options[0].ID, "yes")

		entries, err := svc.Register.Store.ReadStream(ctx, svc.Register.Stream(id), 0)
		must(t, err)
		for _, e := range entries {
			if strings.TrimSpace(e.Meta.Actor) == "" {
				t.Fatalf("entry %d of the decision's stream is attributed to actor %q, which is blank",
					e.Seq, e.Meta.Actor)
			}
		}
	})
}
