package decisions

import (
	"testing"
	"time"
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
