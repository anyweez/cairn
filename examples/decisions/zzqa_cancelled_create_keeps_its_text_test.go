package decisions

// A caller whose request times out or is cancelled gives up on the message it
// sent, but the ledger may still record it: handleCached returns the caller's
// ctx.Err() as soon as its context ends, while the group it was waiting on can
// go on to commit. The register's cleanup reads every error that is not
// ErrRecorded or ErrUnknownOutcome as "nothing was recorded" and deletes the
// text the message stored, so a committed entry is left naming refs whose rows
// are gone.
//
// The stalling connection is shared with the member-row reproduction in
// zzqa_a_cancelled_create_keeps_the_senders_member_row_test.go: it delivers the
// COMMIT's CommandComplete and withholds the read that follows, so the server
// has committed and the client has read the commit tag, but the recording group
// cannot finish. The test cancels the caller then, waits for Create to return,
// and only then lets the recording group finish. That makes the order
// deterministic: the entry is durable, and the caller was answered with
// context.Canceled rather than a recorded error.

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

func TestACancelledCreateKeepsTheTextItsRecordedEntryNames(t *testing.T) {
	verify := ledgertest.NewStore(t, ledger.RequireActor(false))
	svc, stalled, release := stallAfterCommitRegister(t, verify)
	defer release()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type answer struct {
		id  string
		err error
	}
	answered := make(chan answer, 1)
	go func() {
		id, err := svc.Create(ctx, "ana", "Ship the beta?", "yes", "not yet")
		answered <- answer{id, err}
	}()

	// The COMMIT has been read by the server and the client; only the read
	// that follows it is outstanding. Cancel the caller, then wait for Create
	// to take the cancellation path before letting the recording group finish.
	select {
	case <-stalled:
	case <-time.After(10 * time.Second):
		t.Fatal("the Create never reached its COMMIT")
	}
	cancel()
	got := <-answered
	release()

	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Create returned %v, want the caller's context cancellation; "+
			"this shape needs the message to be in flight when its caller gives up", got.err)
	}

	// Read back over a healthy connection: the entry is there.
	state, err := NewService(verify).Get(context.Background(), got.id)
	if err != nil {
		t.Fatalf("reading the recorded decision back: %v", err)
	}
	if state.Title == "" || len(state.Options) != 2 {
		t.Fatalf("premise: the Create was not recorded (state %+v), so no text was lost", state)
	}
	if gotText := svc.Content.Text(state.Title); gotText != "Ship the beta?" {
		t.Fatalf("the recorded entry names title %q, but the register's table holds %q for it: "+
			"the text a request whose caller gave up stored was deleted even though its entry exists",
			state.Title, gotText)
	}
	for i, want := range []string{"yes", "not yet"} {
		if gotText := svc.Content.Text(state.Options[i].Title); gotText != want {
			t.Fatalf("the recorded entry names option %d's title %q, but the register's table holds %q for it, want %q",
				i, state.Options[i].Title, gotText, want)
		}
	}
}
