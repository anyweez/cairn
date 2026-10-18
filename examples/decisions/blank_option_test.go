package decisions

import (
	"testing"
	"time"
)

// AddOption refuses an option with a blank title
// (`refuse(Invalid, "an option needs a title")`), but Create's filter silently
// drops one instead, so a caller gets a decision with fewer options than it
// asked for and no error. Both are the same missing validation on the options a
// Create carries.
func TestCreateRefusesABlankOptionTitle(t *testing.T) {
	d := Decider.Decide(State{}, Create{
		ID: "d-1", By: "ana", At: time.Unix(1_700_000_000, 0).UTC(),
		Title: "Which way?",
		Options: []Option{
			{ID: "o1", Title: "this"},
			{ID: "o2", Title: "   "},
		},
	})
	if !d.Refused() {
		t.Fatalf("Create accepted a blank-title option and dropped it: options=%+v", d.Events)
	}
	if d.Refusal.Code != Invalid {
		t.Fatalf("refusal code %q, want %q", d.Refusal.Code, Invalid)
	}
}
