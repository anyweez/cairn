package ledger_test

import (
	"errors"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// The ledger proves a message and an accepted result can be read back before it
// appends them (queueEntry decodes both), because an entry it cannot decode is
// permanent: entries are append-only and every later Load, Handle and Verify on
// the stream fails. Events and effects get no such check: EncodeAll succeeds,
// the entry commits, and the failure only appears when something later reads
// the stream.
//
// A codec whose UnmarshalJSON is stricter than its MarshalJSON — a validating
// event, or one whose MarshalJSON adds a field its struct does not have — is
// enough to brick a stream this way. This is framework rule D6, but a machine
// that does not run the spec check is not refused: it is recorded.
//
// The test accepts either resolution: Handle refuses the message and records
// nothing, or it records an entry that the stream can still be loaded from.
func TestAnEventThatCannotBeDecodedIsNotRecorded(t *testing.T) {
	t.Parallel()
	store := ledgertest.NewStore(t)
	msgs := codec.New[zzqaMsg]()
	codec.Register[zzqaAdd](msgs, "zzqa.add", 1)
	events := codec.New[zzqaEvent]()
	codec.Register[zzqaAdded](events, "zzqa.added", 1)
	effects := codec.New[zzqaEffect]()
	results := codec.New[zzqaResult]()
	codec.Register[zzqaResult](results, "zzqa.result", 1)

	agg := &ledger.Aggregate[zzqaState, zzqaMsg, zzqaResult, zzqaEvent, zzqaEffect]{
		Store: store, Kind: "zzqa",
		Decider: cairn.Decider[zzqaState, zzqaMsg, zzqaResult, zzqaEvent, zzqaEffect]{
			Initial: func() zzqaState { return zzqaState{} },
			Decide: func(zzqaState, zzqaMsg) cairn.Decision[zzqaResult, zzqaEvent, zzqaEffect] {
				return cairn.Accept[zzqaResult, zzqaEvent, zzqaEffect](1, []zzqaEvent{zzqaAdded{}})
			},
			Evolve: func(s zzqaState, _ zzqaEvent) zzqaState { return s },
		},
		Codecs: &codec.Set[zzqaMsg, zzqaResult, zzqaEvent, zzqaEffect]{
			Messages: msgs, Results: results, Events: events, Effects: effects,
		},
	}

	if _, err := agg.Handle(ctx, "a", zzqaAdd{}); err != nil {
		// The ledger refused the message before recording it. Nothing may have
		// been appended under the refusal either.
		entries, readErr := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
		if readErr != nil {
			t.Fatalf("Handle refused the message (%v) but reading the stream failed: %v", err, readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("Handle refused the message (%v) but recorded %d entries", err, len(entries))
		}
		return
	}
	if _, err := agg.Load(ctx, "a"); err != nil {
		t.Fatalf("the message was recorded but the stream can no longer be loaded: %v", err)
	}
}

type zzqaState struct{ N int }

type zzqaMsg interface{ isZZQAMsg() }

type zzqaAdd struct{}

func (zzqaAdd) isZZQAMsg() {}

type zzqaEvent interface{ isZZQAEvent() }

type zzqaAdded struct{}

func (zzqaAdded) isZZQAEvent() {}

func (zzqaAdded) MarshalJSON() ([]byte, error) { return []byte(`{"added":true}`), nil }

// UnmarshalJSON is stricter than MarshalJSON: the event encodes, but the
// ledger's DisallowUnknownFields decode cannot read it back.
func (*zzqaAdded) UnmarshalJSON([]byte) error {
	return errors.New("zzqaAdded refuses every payload")
}

type zzqaEffect interface{ isZZQAEffect() }

type zzqaResult int
