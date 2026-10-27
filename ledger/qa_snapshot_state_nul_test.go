package ledger_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// qaSnapshotState carries a NUL the way a state can: Initial returns it, and
// no message or event has to contain it. Its JSON is valid and survives the
// round trip spectest's D7 checks, but Postgres jsonb cannot store it.
type qaSnapshotState struct{ Note string }

type qaSnapshotMsg struct{}

type qaSnapshotEvent struct{}

func TestASnapshotStateTheLedgerCannotStoreIsRefusedBeforeTheDatabase(t *testing.T) {
	t.Parallel()
	store := ledgertest.NewStore(t)
	msgs := codec.New[qaSnapshotMsg]()
	codec.Register[qaSnapshotMsg](msgs, "qa.snapshot.msg", 1)
	events := codec.New[qaSnapshotEvent]()
	codec.Register[qaSnapshotEvent](events, "qa.snapshot.event", 1)
	results := codec.New[int]()
	codec.Register[int](results, "qa.snapshot.result", 1)
	effects := codec.New[struct{}]()

	agg := &ledger.Aggregate[qaSnapshotState, qaSnapshotMsg, int, qaSnapshotEvent, struct{}]{
		Store: store, Kind: "qa-snapshot",
		Decider: cairn.Decider[qaSnapshotState, qaSnapshotMsg, int, qaSnapshotEvent, struct{}]{
			Initial: func() qaSnapshotState { return qaSnapshotState{Note: "a\x00b"} },
			Decide: func(qaSnapshotState, qaSnapshotMsg) cairn.Decision[int, qaSnapshotEvent, struct{}] {
				return cairn.Accept[int, qaSnapshotEvent, struct{}](1, []qaSnapshotEvent{{}})
			},
			Evolve: func(s qaSnapshotState, _ qaSnapshotEvent) qaSnapshotState { return s },
		},
		Codecs:        &codec.Set[qaSnapshotMsg, int, qaSnapshotEvent, struct{}]{Messages: msgs, Results: results, Events: events, Effects: effects},
		StateVersion:  "v1",
		SnapshotEvery: 1,
	}

	_, err := agg.Handle(ctx, "a", qaSnapshotMsg{})
	if err == nil {
		t.Fatal("the message was recorded although its snapshot cannot be stored")
	}
	var pgErr *pgconn.PgError
	if errors.As(err, &pgErr) {
		t.Fatalf("the caller gets a raw database error instead of a cairn error saying the state cannot be stored: %v", err)
	}
	if !strings.Contains(err.Error(), "snapshot") {
		t.Fatalf("the error does not name the snapshot: %v", err)
	}
}
