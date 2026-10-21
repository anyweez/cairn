package ledger_test

import (
	"context"
	"encoding/json"
	"math"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A state is stored in a snapshot through a jsonb column, and jsonb is not a
// faithful copy of the bytes json.Marshal wrote: it reorders object keys,
// reformats numbers ("1e2" becomes 100, "-0" becomes 0) and normalises
// whitespace. The spec's D7 (Snapshottable) checks json.Marshal followed by
// json.Unmarshal, so it accepts these states, and the README says a snapshot
// holds the state Evolve produced. But a Load that reads the snapshot returns a
// different value: an opaque JSON payload comes back with its keys reordered,
// and a negative zero comes back positive. A decider that reads either (a
// signature over the payload, math.Signbit, 1/x) decides on a state the stream
// never had.
type qaSnapState struct {
	Raw json.RawMessage `json:"raw"`
	X   float64         `json:"x"`
}

type qaSnapMsg interface{ isQASnapMsg() }

type qaSnapNoop struct{}

func (qaSnapNoop) isQASnapMsg() {}

var qaSnapDecider = cairn.Decider[qaSnapState, qaSnapMsg, int, struct{}, struct{}]{
	Initial: func() qaSnapState {
		return qaSnapState{
			// Compact, so the plain JSON round trip D7 checks leaves it alone.
			Raw: json.RawMessage(`{"b":2,"a":1}`),
			X:   math.Copysign(0, -1),
		}
	},
	Decide: func(s qaSnapState, m qaSnapMsg) cairn.Decision[int, struct{}, struct{}] {
		return cairn.Accept[int, struct{}, struct{}](0, []struct{}{})
	},
	Evolve: func(s qaSnapState, e struct{}) qaSnapState { return s },
}

func TestASnapshotReturnsTheStateItStored(t *testing.T) {
	t.Parallel()
	msgs := codec.New[qaSnapMsg]()
	codec.Register[qaSnapNoop](msgs, "qa_snap_noop", 1)
	results := codec.New[int]()
	codec.Register[int](results, "qa_snap_result", 1)
	events := codec.New[struct{}]()
	effects := codec.New[struct{}]()
	agg := &ledger.Aggregate[qaSnapState, qaSnapMsg, int, struct{}, struct{}]{
		Store: ledgertest.NewStore(t), Kind: "qa-snap", Decider: qaSnapDecider,
		Codecs:        &codec.Set[qaSnapMsg, int, struct{}, struct{}]{Messages: msgs, Results: results, Events: events, Effects: effects},
		StateVersion:  "qa/snap/1",
		SnapshotEvery: 1,
	}

	initial := qaSnapDecider.Initial()
	// D7's round trip, which is what the spec accepts the state by.
	raw, err := json.Marshal(initial)
	if err != nil {
		t.Fatal(err)
	}
	var back qaSnapState
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if string(back.Raw) != string(initial.Raw) || math.Signbit(back.X) != math.Signbit(initial.X) {
		t.Fatalf("the plain JSON round trip already changed the state (%s), so this probe is not about the ledger", raw)
	}

	if _, err := agg.Handle(context.Background(), "a", qaSnapNoop{}); err != nil {
		t.Fatal(err)
	}

	// Load reads the snapshot the first message wrote, since nothing after it
	// changed the stream.
	loaded, err := agg.Load(context.Background(), "a")
	if err != nil {
		t.Fatal(err)
	}
	if string(loaded.State.Raw) != string(initial.Raw) {
		t.Errorf("the loaded state's opaque JSON payload is %s, but the snapshotted state had %s: the snapshot column rewrote bytes the state carried",
			loaded.State.Raw, initial.Raw)
	}
	if math.Signbit(loaded.State.X) != math.Signbit(initial.X) {
		t.Errorf("the loaded state has X=%v, but the snapshotted state had X=%v: the snapshot column lost the sign",
			loaded.State.X, initial.X)
	}
}
