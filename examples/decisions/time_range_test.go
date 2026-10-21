package decisions

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A time outside the range encoding/json can write is accepted by Decide and
// then cannot be stored: the message codec's json.Marshal rejects it with
// "Time.MarshalJSON: year outside of range [0,9999]". The decider already
// checks that a Create or a RecordOutcome carries a non-zero time; it should
// check the range too, so the caller gets a refusal rather than an encoding
// error. Nothing is stored either way, but the ledger's contract (a message
// Decide accepts survives its codec, README D6) is broken, and the check the
// decider runs for text (codec.CheckStorable) does not catch it because
// time.Time implements json.Marshaler.
func TestTimeOutsideJSONRangeIsRefusedByDecide(t *testing.T) {
	at := time.Date(300000, 1, 1, 0, 0, 0, 0, time.UTC)
	create, _ := Decider.Step(State{}, Create{ID: "d-1", By: "ana", At: at, Title: "ship?",
		Options: []Option{{ID: "o1", Title: "yes"}}})
	if !create.Refused() {
		t.Fatalf("Decide accepted a Create whose time cannot be encoded: %+v", create)
	}

	state := Decider.Initial()
	if d, s := Decider.Step(state, Create{ID: "d-1", By: "ana", At: time.Unix(1_700_000_000, 0).UTC(),
		Title: "ship?", Options: []Option{{ID: "o1", Title: "yes"}}}); d.Refused() {
		t.Fatal(d.Refusal)
	} else {
		state = s
	}
	outcome, _ := Decider.Step(state, RecordOutcome{By: "ana", At: at, Chosen: "o1", Rationale: "because"})
	if !outcome.Refused() {
		t.Fatalf("Decide accepted a RecordOutcome whose time cannot be encoded: %+v", outcome)
	}
}

// The same value through the shell and a ledger. The caller gets the
// register's refusal, not a codec error raised after Decide said yes, and the
// refused message leaves no entry behind: it cannot be stored at all.
func TestATimeOutsideJSONRangeIsRefusedThroughTheLedger(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))
	svc.Now = func() time.Time { return time.Date(300000, time.January, 1, 0, 0, 0, 0, time.UTC) }

	_, err := svc.Create(ctx, "ana", "ship?", "yes")
	var refusal *cairn.Refusal
	if !errors.As(err, &refusal) || refusal.Code != Invalid {
		t.Fatalf("want the register's refusal, got %v", err)
	}
	if streams, err := svc.Register.Store.Streams(ctx, "decision"); err != nil || len(streams) != 0 {
		t.Fatalf("a refused message left a stream: %v, %v", streams, err)
	}
}
