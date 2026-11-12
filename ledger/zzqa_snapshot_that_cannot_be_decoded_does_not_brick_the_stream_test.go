package ledger_test

import (
	"errors"
	"testing"
)

// zzqaSnapUnreadable is a state whose UnmarshalJSON is stricter than its
// MarshalJSON: it encodes fine - json.Marshal is all codec.CheckStorable and
// queueSnapshot ask of a state - but nothing can read it back. A snapshot is
// written into cairn.state_snapshots and read with json.Unmarshal by every
// later Load and Handle of the stream. Once such a state is snapshotted,
// nothing can load the stream again, so it is bricked: the entry is appended,
// the snapshot row is written, and every later read fails with the same
// decode error.
//
// The ledger refuses an entry whose message, result, events or effects encode
// but cannot be decoded (encodeReadable in ledger/aggregate.go), because an
// entry it cannot decode is permanent. A snapshot is the same hazard on the
// state path: Handle must either refuse the message and write nothing, or
// write a snapshot the ledger can read back.
type zzqaSnapUnreadable struct{ Note string }

func (zzqaSnapUnreadable) MarshalJSON() ([]byte, error) { return []byte(`{"note":"stored"}`), nil }

func (*zzqaSnapUnreadable) UnmarshalJSON([]byte) error {
	return errors.New("zzqaSnapUnreadable refuses every payload")
}

func TestASnapshotTheLedgerCannotDecodeDoesNotBrickTheStream(t *testing.T) {
	t.Parallel()
	agg := snapshotEveryMessage(t, "zzqa-snap-unreadable", zzqaSnapUnreadable{Note: "stored"})

	handled, err := agg.Handle(ctx, "a", snapMsg{})
	if err != nil {
		// The message was refused before it was recorded: nothing may have
		// been written, and the stream must still load.
		entries, readErr := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
		if readErr != nil {
			t.Fatalf("Handle refused the message (%v) but the stream can no longer be read: %v", err, readErr)
		}
		if len(entries) != 0 {
			t.Fatalf("Handle refused the message (%v) but recorded %d entries", err, len(entries))
		}
		if _, loadErr := agg.Load(ctx, "a"); loadErr != nil {
			t.Fatalf("Handle refused the message (%v) but the stream can no longer be loaded: %v", err, loadErr)
		}
		return
	}

	// The message was accepted, so the snapshot it wrote has to be readable.
	// A Load that fails here is permanent: the snapshot row is only replaced
	// when another snapshot is due, which needs a Load first.
	if _, err := agg.Load(ctx, "a"); err != nil {
		t.Fatalf("the message was recorded (%+v) but the stream can no longer be loaded: %v", handled.Entry.Seq, err)
	}
	// The next message on the same stream has to stay possible too.
	if _, err := agg.Handle(ctx, "a", snapMsg{}); err != nil {
		t.Fatalf("the stream is stuck after one recorded message: %v", err)
	}
}

// The control: a state that round-trips is still recorded and loadable, so a
// fix that refuses every snapshot is not a pass.
func TestASnapshotThatCanBeDecodedIsStillRecorded(t *testing.T) {
	t.Parallel()
	agg := snapshotEveryMessage(t, "zzqa-snap-readable", zzqaSnapReadable{Note: "stored"})

	if _, err := agg.Handle(ctx, "a", snapMsg{}); err != nil {
		t.Fatalf("a state that round-trips was refused: %v", err)
	}
	loaded, err := agg.Load(ctx, "a")
	if err != nil {
		t.Fatalf("a stream with a readable snapshot cannot be loaded: %v", err)
	}
	if loaded.Seq != 1 || loaded.State.Note != "stored" {
		t.Fatalf("loaded %+v, want seq 1 with Note %q", loaded, "stored")
	}
}

type zzqaSnapReadable struct{ Note string }
