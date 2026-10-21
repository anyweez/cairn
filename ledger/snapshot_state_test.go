package ledger_test

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

type snapMsg struct{}

type snapEvent struct{}

// snapshotEveryMessage builds an aggregate whose state is state and which is
// due a snapshot on every message, so the first Handle is where the snapshot
// column is written and where Load reads it back from.
func snapshotEveryMessage[S any](t *testing.T, kind string, state S) *ledger.Aggregate[S, snapMsg, int, snapEvent, struct{}] {
	t.Helper()
	msgs := codec.New[snapMsg]()
	codec.Register[snapMsg](msgs, kind+".msg", 1)
	events := codec.New[snapEvent]()
	codec.Register[snapEvent](events, kind+".event", 1)
	results := codec.New[int]()
	codec.Register[int](results, kind+".result", 1)
	return &ledger.Aggregate[S, snapMsg, int, snapEvent, struct{}]{
		Store: ledgertest.NewStore(t), Kind: kind,
		Decider: cairn.Decider[S, snapMsg, int, snapEvent, struct{}]{
			Initial: func() S { return state },
			Decide: func(S, snapMsg) cairn.Decision[int, snapEvent, struct{}] {
				return cairn.Accept[int, snapEvent, struct{}](1, []snapEvent{{}})
			},
			Evolve: func(s S, _ snapEvent) S { return s },
		},
		Codecs: &codec.Set[snapMsg, int, snapEvent, struct{}]{
			Messages: msgs, Results: results, Events: events, Effects: codec.New[struct{}](),
		},
		StateVersion:  "v1",
		SnapshotEvery: 1,
	}
}

type snapTextState struct{ Note string }

// A snapshot is read back with json.Unmarshal, so the state it holds has to be
// the state that was snapshotted. json.Marshal silently rewrites a string that
// is not valid UTF-8 as U+FFFD, so such a state used to be recorded and then
// loaded back as a different value, with nothing reporting it. It is refused
// now, naming the field, before anything is written.
func TestASnapshotStateWithTextTheLedgerWouldRewriteIsRefused(t *testing.T) {
	t.Parallel()
	agg := snapshotEveryMessage(t, "qa-snap-text", snapTextState{Note: "a\xffb"})

	_, err := agg.Handle(ctx, "a", snapMsg{})
	if err == nil {
		t.Fatal("a state carrying invalid UTF-8 was recorded, so Load would return a state the stream never had")
	}
	var notStorable *codec.NotStorableError
	if !errors.As(err, &notStorable) {
		t.Fatalf("the caller is not told which text cannot be stored: %v", err)
	}
	if msg := err.Error(); !strings.Contains(msg, "snapshot") || !strings.Contains(msg, "Note") {
		t.Fatalf("the error names neither the snapshot nor the field: %v", err)
	}
	entries, readErr := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if readErr != nil {
		t.Fatal(readErr)
	}
	if len(entries) != 0 {
		t.Fatalf("the refused message was recorded: %d entries", len(entries))
	}
}

// snapMarshalsItself decides its own JSON, and returns valid JSON whose string
// holds a NUL escape. That is a state Postgres's jsonb cannot represent, so the
// write used to fail with a raw SQLSTATE from the driver.
type snapMarshalsItself struct{}

func (snapMarshalsItself) MarshalJSON() ([]byte, error) { return []byte(`"a\u0000b"`), nil }

func TestASnapshotStateTheJSONColumnCannotHoldIsRefused(t *testing.T) {
	t.Parallel()
	agg := snapshotEveryMessage(t, "qa-snap-nul-json", snapMarshalsItself{})

	_, err := agg.Handle(ctx, "a", snapMsg{})
	if err == nil {
		t.Fatal("the message was recorded although its snapshot carries a NUL")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Fatalf("the caller gets a raw database error instead of a cairn error: %v", err)
	}
	if !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("the error does not name the snapshot: %v", err)
	}
}

type snapNumberState struct{ N json.Number }

// A snapshot keeps the bytes json.Marshal wrote, so a state that carries its
// own number text loads back with that text: jsonb stored 1e2 as 100, and a
// decider that reads the text back then sees a state it never produced.
func TestASnapshotKeepsANumbersOwnText(t *testing.T) {
	t.Parallel()
	want := snapNumberState{N: json.Number("1e2")}
	agg := snapshotEveryMessage(t, "qa-snap-number", want)
	if _, err := agg.Handle(ctx, "a", snapMsg{}); err != nil {
		t.Fatal(err)
	}
	loaded, err := agg.Load(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if got := loaded.State.N.String(); got != "1e2" {
		t.Fatalf("the loaded state's number is %s, but the snapshotted state had 1e2", got)
	}
}
