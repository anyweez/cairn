package ledger_test

import (
	"encoding/json"
	"slices"
	"sort"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// qaRank is a message with a slice field, the shape a caller hands over and a
// decider may sort. The slice header is copied into Decide, but the backing
// array is the caller's, so normalising it in place reaches the caller's value
// and whatever Handle encodes afterwards.
type qaRank struct{ Order []int }

type qaRankMsg interface{ isQARankMsg() }

func (qaRank) isQARankMsg() {}

// The entry log is the record of what was asked: ReadStream, Verify and a
// stream rebuilt from its entries decode the message that was recorded. A
// decider that sorts or otherwise normalises a message field in place must not
// change what is recorded, and Handle must not change the value the caller
// passed.
func TestTheEntryHoldsTheMessageTheCallerSent(t *testing.T) {
	msgs := codec.New[qaRankMsg]()
	codec.Register[qaRank](msgs, "qa.rank", 1)
	results := codec.New[int]()
	codec.Register[int](results, "qa.rank.result", 1)
	events := codec.New[event]()
	codec.Register[added](events, "qa.rank.event", 1)
	effects := codec.New[effect]()
	codec.Register[announce](effects, "qa.rank.effect", 1)

	agg := &ledger.Aggregate[counter, qaRankMsg, int, event, effect]{
		Store: ledgertest.NewStore(t),
		Kind:  "qa-rank",
		Decider: cairn.Decider[counter, qaRankMsg, int, event, effect]{
			Initial: func() counter { return counter{} },
			Decide: func(c counter, m qaRankMsg) cairn.Decision[int, event, effect] {
				rank := m.(qaRank)
				// The ordinary way Go code normalises a list it was handed.
				sort.Ints(rank.Order)
				return cairn.Accept[int, event, effect](len(rank.Order), nil)
			},
			Evolve: func(c counter, e event) counter { return c },
		},
		Codecs: &codec.Set[qaRankMsg, int, event, effect]{
			Messages: msgs, Results: results, Events: events, Effects: effects,
		},
	}

	sent := qaRank{Order: []int{3, 1, 2}}
	if _, err := agg.Handle(ctx, "a", sent); err != nil {
		t.Fatal(err)
	}

	entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("want one entry, got %d", len(entries))
	}
	var stored qaRank
	if err := json.Unmarshal(entries[0].Message.Data, &stored); err != nil {
		t.Fatal(err)
	}
	if want := []int{3, 1, 2}; !slices.Equal(stored.Order, want) {
		t.Errorf("the entry decodes to order %v, but the caller sent %v: Handle recorded the message after Decide had normalised it", stored.Order, want)
	}
	if want := []int{3, 1, 2}; !slices.Equal(sent.Order, want) {
		t.Errorf("the caller's own message now has order %v, but it was sent as %v: Handle let Decide write through it", sent.Order, want)
	}
}
