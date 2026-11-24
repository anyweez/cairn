package ledger_test

import (
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
)

// Messages that queue while a stream's transaction is in flight are recorded
// together, and docs/PERFORMANCE.md says what happens when one of them spoils
// the batch: "A failure that spoils a shared transaction is narrowed down by
// halving the group: a half the database accepts is recorded whole, and only
// a failing half is halved again, so one bad message costs a few transactions
// rather than one per message." `TestOneBadMessageInABurstIsNarrowedDownByHalving`
// holds that for a statement the database refuses.
//
// A statement pgx cannot build is another way one message spoils the batch:
// the whole batch is abandoned before it is sent, so nothing was recorded and
// the messages can be decided again, which is exactly when retryable
// (`ledger/cache.go`) says a group may be halved. It does not recognise this
// one, because the failure is a *batchError wrapping pgx's preprocessing error
// rather than a *pgconn.PgError or a *viewFailure. The group is then never
// narrowed, and every caller in it is handed the error about the one entry
// whose view queued the bad statement.
//
// The burst is made one transaction the way the halving test makes it one: the
// first message's view holds the stream's lane open until every message of the
// burst has queued behind it.
func TestABurstKeepsEveryMessageAViewThatCannotEncodeDoesNotSpoil(t *testing.T) {
	// Not parallel: it holds the stream's lane open while the burst queues.
	const burst = 10
	agg := newCounter(t)
	view := totalsView(t, agg)
	holding := make(chan struct{})
	release := make(chan struct{})
	// totals is the state each view write saw, in order: the lane records one
	// transaction at a time, so they are appended from one goroutine.
	var totals []int
	agg.Views = []ledger.View[counter]{{Name: "counter_totals", Write: func(b *ledger.Batch, at ledger.Loaded[counter], cause ledger.Entry) error {
		totals = append(totals, at.State.Total)
		if at.State.Total == 1 {
			// Hold the lane until the burst has queued behind this message,
			// so the whole burst is recorded in one transaction.
			close(holding)
			<-release
		}
		return view.Write(b, at, cause)
	}}}
	agg.EntryViews = []ledger.EntryView[counter, event]{{Name: "audit", Write: func(b *ledger.Batch, _, _ ledger.Loaded[counter], entry ledger.Entry, _ []event) error {
		if entry.Meta.CorrelationID == "poison" {
			// A view author's mistake: a value pgx has no encode plan for.
			// Nothing reaches the database, so the batch is never sent.
			b.Queue(`SELECT $1::int`, make(chan int))
		}
		return nil
	}}}

	var wg sync.WaitGroup
	wg.Go(func() {
		if _, err := agg.Handle(ctx, "a", add{1, 1000}); err != nil {
			t.Error(err)
		}
	})
	<-holding

	hs := make([]ledger.Handled[int], burst)
	errs := make([]error, burst)
	for i := range burst {
		c := ctx
		if i == burst/2 {
			c = ledger.ContextWithMeta(ctx, ledger.Meta{CorrelationID: "poison"})
		}
		wg.Go(func() { hs[i], errs[i] = agg.Handle(c, "a", add{1, 1000}) })
	}
	time.Sleep(100 * time.Millisecond) // every message of the burst joins the lane
	close(release)
	wg.Wait()

	// The premise: the burst shared one transaction, which is what makes one
	// message able to spoil the others. The first write is the held message;
	// the next sees the state after every message of the burst.
	if len(totals) < 2 || totals[1] != burst+1 {
		t.Fatalf("the view's writes saw totals %v, so the burst was not recorded as one transaction", totals)
	}

	var failed int
	for i := range hs {
		if errs[i] == nil {
			continue
		}
		failed++
		// Only the message whose entry the view could not queue a statement
		// for may fail, and its error has to say so.
		if i != burst/2 {
			t.Errorf("message %d failed with %v: only the message whose entry view cannot encode should fail", i, errs[i])
			continue
		}
		if !strings.Contains(errs[i].Error(), "entry view audit") && !strings.Contains(errs[i].Error(), "view audit") {
			t.Errorf("the failed message's error does not name the entry view: %v", errs[i])
		}
	}
	if failed != 1 {
		t.Fatalf("%d of the %d messages failed, want only the one whose entry view cannot encode its statement", failed, burst)
	}
	// Every other message was decided and has to be recorded: the batch that
	// could not be built recorded nothing, so halving the group loses none.
	// The one whose entry view cannot encode has nowhere to go and is the only
	// entry missing: the stream holds the message that held the lane and the
	// burst's other messages.
	checkStream(t, agg, "a", burst, burst)
}
