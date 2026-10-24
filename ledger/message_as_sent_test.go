package ledger_test

import (
	"encoding/json"
	"maps"
	"slices"
	"sort"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// ranked carries every kind of field a message shares with its caller: a slice,
// a map and a pointer. Go copies the message into Decide by value, but these
// are only headers, so a decider writing through one reaches the caller's own
// value and whatever the ledger encodes afterwards.
type ranked struct {
	Order []int
	Seen  map[string]int
	Note  *note
}

func (ranked) isMsg() {}

type note struct{ Text string }

// The entry is the record of what was asked, so it holds the message as the
// caller passed it, however Decide normalised its copy. The caller's own value
// is untouched too: nothing the ledger does writes through the fields it was
// handed.
func TestTheEntryHoldsMapsAndPointersTheCallerSent(t *testing.T) {
	t.Parallel()
	msgs := codec.New[msg]()
	codec.Register[ranked](msgs, "ranked", 1)
	results := codec.New[int]()
	codec.Register[int](results, "ranked.result", 1)
	events := codec.New[event]()
	codec.Register[added](events, "ranked.event", 1)
	effects := codec.New[effect]()
	codec.Register[announce](effects, "ranked.effect", 1)

	agg := &ledger.Aggregate[counter, msg, int, event, effect]{
		Store: ledgertest.NewStore(t),
		Kind:  "ranked",
		Decider: cairn.Decider[counter, msg, int, event, effect]{
			Initial: func() counter { return counter{} },
			Decide: func(c counter, m msg) cairn.Decision[int, event, effect] {
				r := m.(ranked)
				sort.Ints(r.Order)
				r.Seen["decided"]++
				r.Note.Text = "decided"
				return cairn.Accept[int, event, effect](r.Seen["decided"], nil)
			},
			Evolve: func(c counter, e event) counter { return c },
		},
		Codecs: &codec.Set[msg, int, event, effect]{
			Messages: msgs, Results: results, Events: events, Effects: effects,
		},
	}

	sent := ranked{
		Order: []int{3, 1, 2},
		Seen:  map[string]int{"sent": 1},
		Note:  &note{Text: "sent"},
	}
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
	var stored ranked
	if err := json.Unmarshal(entries[0].Message.Data, &stored); err != nil {
		t.Fatal(err)
	}
	if want := []int{3, 1, 2}; !slices.Equal(stored.Order, want) {
		t.Errorf("the entry's order is %v, but the caller sent %v", stored.Order, want)
	}
	if want := map[string]int{"sent": 1}; !maps.Equal(stored.Seen, want) {
		t.Errorf("the entry's seen map is %v, but the caller sent %v", stored.Seen, want)
	}
	if want := "sent"; stored.Note == nil || stored.Note.Text != want {
		t.Errorf("the entry's note is %v, but the caller sent %q", stored.Note, want)
	}

	if want := []int{3, 1, 2}; !slices.Equal(sent.Order, want) {
		t.Errorf("the caller's order is now %v, but it was sent as %v", sent.Order, want)
	}
	if want := map[string]int{"sent": 1}; !maps.Equal(sent.Seen, want) {
		t.Errorf("the caller's seen map is now %v, but it was sent as %v", sent.Seen, want)
	}
	if want := "sent"; sent.Note.Text != want {
		t.Errorf("the caller's note is now %q, but it was sent as %q", sent.Note.Text, want)
	}
}
