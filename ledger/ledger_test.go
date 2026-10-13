package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/codec"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A counter that refuses to pass its cap: the smallest decider with a result,
// a refusal, events and effects.

type counter struct{ Total int }

type msg interface{ isMsg() }

type add struct{ N, Cap int }

func (add) isMsg() {}

type event interface{ isEvent() }

type added struct{ N int }

func (added) isEvent() {}

type effect interface{ isEffect() }

type announce struct{ Total int }

func (announce) isEffect() {}

func decide(s counter, m msg) cairn.Decision[int, event, effect] {
	a := m.(add)
	if s.Total+a.N > a.Cap {
		return cairn.Refuse[int, event, effect]("over_cap", "%d + %d passes %d", s.Total, a.N, a.Cap)
	}
	return cairn.Accept[int, event, effect](s.Total+a.N, []event{added{a.N}}, announce{s.Total + a.N})
}

var counterDecider = cairn.Decider[counter, msg, int, event, effect]{
	Initial: func() counter { return counter{} },
	Decide:  decide,
	Evolve:  func(s counter, e event) counter { s.Total += e.(added).N; return s },
}

func newCounter(t *testing.T) *ledger.Aggregate[counter, msg, int, event, effect] {
	t.Helper()
	msgs := codec.New[msg]()
	codec.Register[add](msgs, "add", 1)
	events := codec.New[event]()
	codec.Register[added](events, "added", 1)
	effects := codec.New[effect]()
	codec.Register[announce](effects, "announce", 1)
	return &ledger.Aggregate[counter, msg, int, event, effect]{
		Store: ledgertest.NewStore(t), Kind: "counter", Decider: counterDecider,
		Messages: msgs, Events: events, Effects: effects,
	}
}

var ctx = context.Background()

func TestHandleRecordsDecisionsAndRefusals(t *testing.T) {
	agg := newCounter(t)

	if _, err := agg.Handle(ctx, "a", add{3, 10}); err != nil {
		t.Fatal(err)
	}
	h, err := agg.Handle(ctx, "a", add{9, 10})
	if !errors.Is(err, &cairn.Refusal{Code: "over_cap"}) {
		t.Fatalf("expected an over_cap refusal, got %v", err)
	}
	if h.Entry.Seq != 2 || h.State.Total != 3 {
		t.Fatalf("a refusal is recorded and changes nothing: %+v", h)
	}
	h, err = agg.Handle(ctx, "a", add{2, 10})
	if err != nil {
		t.Fatal(err)
	}
	if h.Result != 5 || h.Entry.Seq != 3 {
		t.Fatalf("got %+v", h)
	}

	entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 3 || entries[1].Refusal == nil || len(entries[1].Events) != 0 || len(entries[2].Effects) != 1 {
		t.Fatalf("entries: %+v", entries)
	}

	loaded, err := agg.Load(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State.Total != 5 || loaded.Seq != 3 {
		t.Fatalf("loaded %+v", loaded)
	}
}

func TestConcurrentHandlesOnOneStreamSerialize(t *testing.T) {
	agg := newCounter(t)
	var wg sync.WaitGroup
	for range 20 {
		wg.Go(func() {
			if _, err := agg.Handle(ctx, "busy", add{1, 1000}); err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()

	entries, err := agg.Store.ReadStream(ctx, agg.Stream("busy"), 0)
	if err != nil {
		t.Fatal(err)
	}
	for i, e := range entries {
		if e.Seq != int64(i+1) || string(e.Result) != fmt.Sprint(i+1) {
			t.Fatalf("entry %d: seq %d result %s; every handle must see the one before it", i, e.Seq, e.Result)
		}
	}
	if len(entries) != 20 {
		t.Fatalf("%d entries", len(entries))
	}
}

func TestIdempotencyKeyAnswersARetryWithoutDecidingAgain(t *testing.T) {
	agg := newCounter(t)
	first, err := agg.Handle(ctx, "a", add{4, 10}, ledger.IdempotencyKey("req-1"))
	if err != nil {
		t.Fatal(err)
	}
	again, err := agg.Handle(ctx, "a", add{4, 10}, ledger.IdempotencyKey("req-1"))
	if err != nil {
		t.Fatal(err)
	}
	if !again.Duplicate || again.Result != first.Result || again.State.Total != 4 || again.Entry.Seq != 1 {
		t.Fatalf("retry: %+v", again)
	}

	// A refused message is answered the same way on retry.
	if _, err := agg.Handle(ctx, "a", add{99, 10}, ledger.IdempotencyKey("req-2")); err == nil {
		t.Fatal("expected a refusal")
	}
	_, err = agg.Handle(ctx, "a", add{1, 1000}, ledger.IdempotencyKey("req-2"))
	if !errors.Is(err, &cairn.Refusal{Code: "over_cap"}) {
		t.Fatalf("a retried refusal must stay refused, got %v", err)
	}
}

func TestSnapshotsAreUsedAndIgnoredWhenTheirVersionChanges(t *testing.T) {
	agg := newCounter(t)
	agg.StateVersion, agg.SnapshotEvery = "v1", 2
	for range 5 {
		if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
			t.Fatal(err)
		}
	}
	var seq int64
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT seq FROM cairn.snapshots WHERE stream = 'counter/a'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq != 4 {
		t.Fatalf("snapshot at %d, want 4", seq)
	}

	// Doctor the snapshot to prove Load reads it: 100 from the snapshot plus
	// the one entry after it.
	if _, err := agg.Store.Pool().Exec(ctx, `UPDATE cairn.snapshots SET state = '{"Total": 100}'`); err != nil {
		t.Fatal(err)
	}
	loaded, err := agg.Load(ctx, "a")
	if err != nil {
		t.Fatal(err)
	}
	if loaded.State.Total != 101 {
		t.Fatalf("loaded %d; the snapshot was not used", loaded.State.Total)
	}

	agg.StateVersion = "v2"
	if loaded, err = agg.Load(ctx, "a"); err != nil {
		t.Fatal(err)
	}
	if loaded.State.Total != 5 {
		t.Fatalf("loaded %d; a snapshot of another version must be ignored", loaded.State.Total)
	}
}

func TestProjectionSeesEachEntryOnceInCommitOrder(t *testing.T) {
	agg := newCounter(t)
	pool := agg.Store.Pool()
	if _, err := pool.Exec(ctx, `CREATE TABLE totals (stream text PRIMARY KEY, total int NOT NULL, entries int NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	projection := ledger.Projection{
		Name:      "totals",
		Kinds:     []string{"counter"},
		BatchSize: 2,
		Apply: func(ctx context.Context, tx pgx.Tx, e ledger.Entry) error {
			total := 0
			if e.Refusal == nil {
				total = int(e.Result[0] - '0')
			}
			_, err := tx.Exec(ctx, `
INSERT INTO totals VALUES ($1, $2, 1)
ON CONFLICT (stream) DO UPDATE SET total = greatest(totals.total, $2), entries = totals.entries + 1`, e.Stream, total)
			return err
		},
	}

	for _, id := range []string{"a", "b", "a"} {
		if _, err := agg.Handle(ctx, id, add{2, 9}); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := agg.Store.CatchUp(ctx, projection); err != nil || n != 3 {
		t.Fatalf("caught up %d, %v", n, err)
	}
	if _, err := agg.Handle(ctx, "b", add{2, 9}); err != nil {
		t.Fatal(err)
	}
	if n, err := agg.Store.CatchUp(ctx, projection); err != nil || n != 1 {
		t.Fatalf("second catch-up applied %d, %v; only the new entry should be applied", n, err)
	}

	rows, _ := pool.Query(ctx, `SELECT stream, total, entries FROM totals ORDER BY stream`)
	got, err := pgx.CollectRows(rows, pgx.RowToStructByPos[struct {
		Stream         string
		Total, Entries int
	}])
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[{counter/a 4 2} {counter/b 4 2}]" {
		t.Fatalf("totals: %v", got)
	}
}

func TestOutboxRetriesUntilDelivered(t *testing.T) {
	agg := newCounter(t)
	if _, err := agg.Handle(ctx, "a", add{1, 9}); err != nil {
		t.Fatal(err)
	}
	if _, err := agg.Handle(ctx, "a", add{1, 9}); err != nil {
		t.Fatal(err)
	}

	var delivered []string
	failOnce := true
	d := &ledger.Dispatcher[effect]{
		Store: agg.Store, Kind: "counter", Effects: agg.Effects, Batch: 1,
		Backoff: func(int) time.Duration { return 0 },
		Handle: func(ctx context.Context, d ledger.Delivery[effect]) error {
			if failOnce {
				failOnce = false
				return errors.New("downstream unavailable")
			}
			delivered = append(delivered, fmt.Sprintf("%s=%d@%d", d.Key(), d.Effect.(announce).Total, d.Attempt))
			return nil
		},
	}
	for {
		n, err := d.DrainOnce(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if n == 0 {
			break
		}
	}
	if fmt.Sprint(delivered) != "[counter/a#1.0=1@2 counter/a#2.0=2@1]" {
		t.Fatalf("delivered %v", delivered)
	}
}

func TestVerifyReportsWhereTheCurrentDeciderDisagrees(t *testing.T) {
	agg := newCounter(t)
	for _, n := range []int{3, 4, 5} {
		_, _ = agg.Handle(ctx, "a", add{n, 9}) // the 5 is refused: 12 passes 9
	}
	_, _ = agg.Handle(ctx, "b", add{5, 4}) // refused: 5 passes 4
	if d, err := agg.VerifyAll(ctx); err != nil || len(d) != 0 {
		t.Fatalf("the decider that wrote the ledger must agree with it: %v %v", d, err)
	}

	// A release that lets the counter reach one past its cap. Stream a's refusal
	// still stands (12 passes 10); stream b's does not (5 reaches 5).
	agg.Decider.Decide = func(s counter, m msg) cairn.Decision[int, event, effect] {
		a := m.(add)
		a.Cap++
		return decide(s, a)
	}
	divergences, err := agg.VerifyAll(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(divergences) != 1 || divergences[0].Aspect != "refusal" || divergences[0].Stream != "counter/b" {
		t.Fatalf("divergences: %v", divergences)
	}
	t.Log(divergences[0])
}

func TestTheLibraryKeepsItsOwnGuarantees(t *testing.T) {
	agg := newCounter(t)
	if _, err := agg.Handle(ctx, "a", add{1, 9}); err != nil {
		t.Fatal(err)
	}

	if _, err := agg.Store.Pool().Exec(ctx, `UPDATE cairn.entries SET seq = 9`); err == nil {
		t.Error("entries must be append-only")
	}

	bad := *agg
	bad.Decider.Decide = func(counter, msg) cairn.Decision[int, event, effect] {
		d := cairn.Refuse[int, event, effect]("no", "no")
		d.Events = []event{added{1}}
		return d
	}
	if _, err := bad.Handle(ctx, "a", add{1, 9}); err == nil || errors.As(err, new(*cairn.Refusal)) {
		t.Errorf("a refusal with events must be rejected, got %v", err)
	}

	err := agg.Store.Transact(ctx, func(tx *ledger.Tx) error {
		if err := tx.Lock(ctx, agg.Stream("a")); err != nil {
			return err
		}
		if err := tx.Lock(ctx, agg.Stream("b")); err == nil {
			t.Error("a second Lock must be refused")
		}
		if _, err := agg.LoadLocked(ctx, tx, "b"); !errors.Is(err, ledger.ErrNotLocked) {
			t.Errorf("loading an unlocked stream for writing: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
