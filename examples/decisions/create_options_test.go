package decisions

import (
	"testing"
	"time"
)

// A decision created with two options that share an ID is ambiguous: a
// position or an outcome that names that ID does not say which option was
// chosen. AddOption already refuses a duplicate ("option %s already exists"),
// so Create must refuse the same set instead of storing it.
func TestCreateRefusesDuplicateOptionIDs(t *testing.T) {
	d := Decider.Decide(State{}, Create{
		ID: "d-1", By: "ana", At: time.Unix(1_700_000_000, 0).UTC(),
		Title: "Which way?",
		Options: []Option{
			{ID: "o1", Title: "this"},
			{ID: "o1", Title: "that"},
		},
	})
	if !d.Refused() {
		t.Fatalf("accepted a Create with two options sharing ID o1: %+v", d.Events)
	}
}
