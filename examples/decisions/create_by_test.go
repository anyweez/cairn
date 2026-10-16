package decisions

import (
	"testing"
	"time"
)

// A Create that names no sender is accepted, and the Created event it stores
// names nobody. The ledger attributes the entry to the request's actor, so the
// log and the decision's own record of who created it disagree.
//
// The fix is for decide's Create case to refuse an empty m.By (and a zero
// m.At), the way RecordOutcome is asked to in t2556, or to fill m.By from
// somewhere it is actually known.
func TestCreateNeedsASender(t *testing.T) {
	at := time.Unix(1_700_000_000, 0).UTC()
	d, _ := Decider.Step(State{}, Create{ID: "d-1", Title: "Ship it?", By: "", At: at})
	if d.Refused() {
		return // refusing the message is the expected fix, and passes this test.
	}
	if len(d.Events) != 1 {
		t.Fatalf("events: %+v", d.Events)
	}
	created, ok := d.Events[0].(Created)
	if !ok {
		t.Fatalf("event %T, want Created", d.Events[0])
	}
	if created.By == "" {
		t.Fatalf("accepted a Create with no sender; the stored Created names nobody: %+v", created)
	}
	if created.At.IsZero() {
		t.Fatalf("accepted a Create with a zero time; the stored Created has no creation time: %+v", created)
	}
}
