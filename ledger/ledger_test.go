package ledger_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
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
	return counterOn(ledgertest.NewStore(t))
}

func counterOn(store *ledger.Store) *ledger.Aggregate[counter, msg, int, event, effect] {
	msgs := codec.New[msg]()
	codec.Register[add](msgs, "add", 1)
	events := codec.New[event]()
	codec.Register[added](events, "added", 1)
	effects := codec.New[effect]()
	codec.Register[announce](effects, "announce", 1)
	results := codec.New[int]()
	codec.Register[int](results, "total", 1)
	return &ledger.Aggregate[counter, msg, int, event, effect]{
		Store: store, Kind: "counter", Decider: counterDecider,
		Codecs: &codec.Set[msg, int, event, effect]{Messages: msgs, Results: results, Events: events, Effects: effects},
	}
}

var ctx = context.Background()

func TestHandleRecordsDecisionsAndRefusals(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
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
		if e.Seq != int64(i+1) || string(e.Result.Data) != fmt.Sprint(i+1) {
			t.Fatalf("entry %d: seq %d result %s; every handle must see the one before it", i, e.Seq, e.Result.Data)
		}
	}
	if len(entries) != 20 {
		t.Fatalf("%d entries", len(entries))
	}
}

// Transactions that lock overlapping sets of streams queue behind one another.
// The global log must still hand out each stream's entries in seq order, or a
// projection would fold a stream's history out of order.
func TestTheGlobalLogKeepsEachStreamInOrder(t *testing.T) {
	// Not parallel: the global log waits on open transactions in every database
	// on the server, so other tests would hold it back.
	agg := newCounter(t)
	ids := []string{"s0", "s1", "s2", "s3"}
	var wg sync.WaitGroup
	for w := range 8 {
		wg.Go(func() {
			for i := range 15 {
				pair := []string{ids[(w+i)%len(ids)], ids[(w+2*i+1)%len(ids)]}
				err := agg.Store.Transact(ctx, []ledger.Stream{agg.Stream(pair[0]), agg.Stream(pair[1])}, func(tx *ledger.Tx) error {
					for _, id := range pair {
						at, err := agg.LoadLocked(ctx, tx, id)
						if err != nil {
							return err
						}
						if _, err := agg.Apply(ctx, tx, &at, add{1, 1 << 30}); err != nil {
							return err
						}
						if pair[0] == pair[1] {
							break
						}
					}
					return nil
				})
				if err != nil {
					t.Error(err)
					return
				}
			}
		})
	}
	wg.Wait()
	var written int
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT count(*) FROM cairn.entries`).Scan(&written); err != nil {
		t.Fatal(err)
	}

	// The cutoff is the oldest transaction running anywhere on the server, so
	// another database's transaction can hold entries back for a while: keep
	// reading until every entry has arrived.
	last := map[ledger.Stream]int64{}
	deadline := time.Now().Add(10 * time.Second)
	for cursor, total := (ledger.Cursor{}), 0; total < written; {
		entries, next, err := agg.Store.ReadAll(ctx, cursor, 50)
		if err != nil {
			t.Fatal(err)
		}
		if len(entries) == 0 {
			if time.Now().After(deadline) {
				t.Fatalf("read %d of %d entries", total, written)
			}
			time.Sleep(10 * time.Millisecond)
			continue
		}
		for _, e := range entries {
			if e.Seq != last[e.Stream]+1 {
				t.Fatalf("%s#%d arrived after #%d", e.Stream, e.Seq, last[e.Stream])
			}
			last[e.Stream] = e.Seq
		}
		total += len(entries)
		cursor = next
	}
}

// Transaction IDs order the log as numbers, including across a change in their
// number of digits.
func TestTheGlobalLogOrdersTransactionIDsAsNumbers(t *testing.T) {
	// Not parallel: the global log waits on open transactions in every database
	// on the server, so other tests would hold it back.
	agg := newCounter(t)
	pool := agg.Store.Pool()
	if _, err := agg.Handle(ctx, "first", add{1, 10}); err != nil {
		t.Fatal(err)
	}
	var current uint64
	if err := pool.QueryRow(ctx, `SELECT pg_current_xact_id()::text::bigint`).Scan(&current); err != nil {
		t.Fatal(err)
	}
	next := uint64(10)
	for next <= current {
		next *= 10
	}
	if next-current > 2_000_000 {
		t.Skipf("transaction ID %d is too far from %d to cross cheaply", current, next)
	}
	// Each iteration commits, so each one uses up a transaction ID.
	if _, err := pool.Exec(ctx, fmt.Sprintf(
		`DO $$ BEGIN FOR i IN 1..%d LOOP PERFORM pg_current_xact_id(); COMMIT; END LOOP; END $$`, next-current)); err != nil {
		t.Fatal(err)
	}
	if _, err := agg.Handle(ctx, "second", add{1, 10}); err != nil {
		t.Fatal(err)
	}

	var entries []ledger.Entry
	cursor := ledger.Cursor{}
	for deadline := time.Now().Add(10 * time.Second); len(entries) < 2 && time.Now().Before(deadline); {
		more, next, err := agg.Store.ReadAll(ctx, cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(more) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		entries, cursor = append(entries, more...), next
	}
	if len(entries) != 2 || entries[0].Stream != agg.Stream("first") || entries[1].Stream != agg.Stream("second") {
		t.Fatalf("got %d entries, first %v", len(entries), entries)
	}
}

// An entry that commits after a later one must not be skipped by a reader that
// has already moved its cursor past the later one.
func TestTheGlobalLogHoldsBackEntriesBehindAnOpenTransaction(t *testing.T) {
	// Not parallel: the global log waits on open transactions in every database
	// on the server, so other tests would hold it back.
	agg := newCounter(t)
	appended, release, done := make(chan struct{}), make(chan struct{}), make(chan error)
	go func() {
		done <- agg.Store.Transact(ctx, []ledger.Stream{agg.Stream("slow")}, func(tx *ledger.Tx) error {
			at, err := agg.LoadLocked(ctx, tx, "slow")
			if err != nil {
				return err
			}
			if _, err := agg.Apply(ctx, tx, &at, add{1, 10}); err != nil {
				return err
			}
			close(appended)
			<-release
			return nil
		})
	}()
	<-appended
	if _, err := agg.Handle(ctx, "fast", add{1, 10}); err != nil {
		t.Fatal(err)
	}

	entries, cursor, err := agg.Store.ReadAll(ctx, ledger.Cursor{}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Fatalf("read %d entries past a transaction still in flight", len(entries))
	}

	close(release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// The cutoff is the oldest transaction running anywhere on the server, so
	// another database's transaction can hold entries back a little longer.
	entries = nil
	for deadline := time.Now().Add(10 * time.Second); len(entries) < 2 && time.Now().Before(deadline); {
		more, next, err := agg.Store.ReadAll(ctx, cursor, 10)
		if err != nil {
			t.Fatal(err)
		}
		if len(more) == 0 {
			time.Sleep(10 * time.Millisecond)
		}
		entries, cursor = append(entries, more...), next
	}
	if len(entries) != 2 || entries[0].Stream != agg.Stream("slow") || entries[1].Stream != agg.Stream("fast") {
		t.Fatalf("got %+v", entries)
	}
}

func TestLockingAStreamDoesNotCreateIt(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	err := agg.Store.Transact(ctx, []ledger.Stream{agg.Stream("read"), agg.Stream("written")}, func(tx *ledger.Tx) error {
		read, err := agg.LoadLocked(ctx, tx, "read")
		if err != nil {
			return err
		}
		if read.Seq != 0 || read.State.Total != 0 {
			t.Errorf("a stream that does not exist loads empty: %+v", read)
		}
		written, err := agg.LoadLocked(ctx, tx, "written")
		if err != nil {
			return err
		}
		_, err = agg.Apply(ctx, tx, &written, add{2, 9})
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	streams, err := agg.Store.Streams(ctx, "counter")
	if err != nil {
		t.Fatal(err)
	}
	if len(streams) != 1 || streams[0] != agg.Stream("written") {
		t.Fatalf("only the stream written to exists, got %v", streams)
	}

	h, err := agg.Handle(ctx, "read", add{2, 9})
	if err != nil || h.Entry.Seq != 1 {
		t.Fatalf("the first write creates the stream: %+v, %v", h.Entry, err)
	}
}

func TestIdempotencyKeyAnswersARetryWithoutDecidingAgain(t *testing.T) {
	t.Parallel()
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
	t.Parallel()
	agg := newCounter(t)
	agg.StateVersion, agg.SnapshotEvery = "v1", 2
	for range 5 {
		if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
			t.Fatal(err)
		}
	}
	var seq int64
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT seq FROM cairn.state_snapshots WHERE stream = 'counter/a'`).Scan(&seq); err != nil {
		t.Fatal(err)
	}
	if seq != 4 {
		t.Fatalf("snapshot at %d, want 4", seq)
	}

	// Doctor the snapshot to prove Load reads it: 100 from the snapshot plus
	// the one entry after it.
	if _, err := agg.Store.Pool().Exec(ctx, `UPDATE cairn.state_snapshots SET state = '{"Total": 100}'`); err != nil {
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

// Two versions of the code handling one stream, as during a rolling deploy,
// each keep their own snapshot instead of evicting the other's and
// re-snapshotting on every message.
func TestSnapshotsOfDifferentVersionsCoexist(t *testing.T) {
	t.Parallel()
	v1 := newCounter(t)
	v1.StateVersion, v1.SnapshotEvery = "v1", 3
	v2 := *v1
	v2.StateVersion = "v2"

	handle := func(agg *ledger.Aggregate[counter, msg, int, event, effect]) {
		t.Helper()
		if _, err := agg.Handle(ctx, "a", add{1, 100}); err != nil {
			t.Fatal(err)
		}
	}
	for range 6 {
		handle(v1) // snapshots at 3 and 6
	}
	handle(&v2) // v2 has none yet, so it snapshots at 7
	handle(v1)  // v1's snapshot at 6 is 2 behind: no new one

	rows, err := v1.Store.Pool().Query(ctx,
		`SELECT version || '@' || seq FROM cairn.state_snapshots WHERE stream = 'counter/a' ORDER BY version`)
	if err != nil {
		t.Fatal(err)
	}
	got, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(got) != "[v1@6 v2@7]" {
		t.Fatalf("snapshots %v, want [v1@6 v2@7]", got)
	}
}

func TestProjectionSeesEachEntryOnceInCommitOrder(t *testing.T) {
	// Not parallel: the global log waits on open transactions in every database
	// on the server, so other tests would hold it back.
	agg := newCounter(t)
	pool := agg.Store.Pool()
	if _, err := pool.Exec(ctx, `CREATE TABLE totals (stream text PRIMARY KEY, total int NOT NULL, entries int NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	projection := ledger.Projection{
		Name:      "totals",
		Kinds:     []string{"counter"},
		BatchSize: 2,
		Apply: func(_ context.Context, b *ledger.Batch, e ledger.Entry) error {
			total := 0
			if e.Refusal == nil {
				total = int(e.Result.Data[0] - '0')
			}
			b.Queue(`
INSERT INTO totals VALUES ($1, $2, 1)
ON CONFLICT (stream) DO UPDATE SET total = greatest(totals.total, $2), entries = totals.entries + 1`, e.Stream.String(), total)
			return nil
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

// seenProjection records each entry it applies once, keyed by stream and seq,
// so applying one twice fails.
func seenProjection(t *testing.T, agg *ledger.Aggregate[counter, msg, int, event, effect]) ledger.Projection {
	t.Helper()
	if _, err := agg.Store.Pool().Exec(ctx,
		`CREATE TABLE seen (stream text NOT NULL, seq bigint NOT NULL, PRIMARY KEY (stream, seq))`); err != nil {
		t.Fatal(err)
	}
	return ledger.Projection{Name: "seen", Apply: func(_ context.Context, b *ledger.Batch, e ledger.Entry) error {
		b.Queue(`INSERT INTO seen VALUES ($1, $2)`, e.Stream.String(), e.Seq)
		return nil
	}}
}

func countSeen(t *testing.T, agg *ledger.Aggregate[counter, msg, int, event, effect]) int {
	t.Helper()
	var n int
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT count(*) FROM seen`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// A statement that fails rolls back the whole batch of entries with the
// checkpoint's advance, so the next catch-up starts from the same place.
func TestAFailingProjectionStatementLeavesTheCheckpoint(t *testing.T) {
	// Not parallel: the global log waits on open transactions in every database
	// on the server, so other tests would hold it back.
	agg := newCounter(t)
	projection := seenProjection(t, agg)
	for _, id := range []string{"a", "a", "b"} {
		if _, err := agg.Handle(ctx, id, add{1, 9}); err != nil {
			t.Fatal(err)
		}
	}

	apply := projection.Apply
	broken := projection
	broken.Apply = func(ctx context.Context, b *ledger.Batch, e ledger.Entry) error {
		if err := apply(ctx, b, e); err != nil {
			return err
		}
		if e.Stream.ID == "a" && e.Seq == 2 {
			b.Queue(`INSERT INTO seen VALUES ($1, NULL)`, e.Stream.String())
		}
		return nil
	}
	n, err := agg.Store.CatchUp(ctx, broken)
	if n != 0 || err == nil || !strings.Contains(err.Error(), "projection seen at counter/a#2: statement 1:") {
		t.Fatalf("want the failing entry and statement named, got %d, %v", n, err)
	}
	if got := countSeen(t, agg); got != 0 {
		t.Fatalf("a failed batch left %d rows", got)
	}

	if n, err := agg.Store.CatchUp(ctx, projection); err != nil || n != 3 {
		t.Fatalf("after the failure, caught up %d, %v; want all 3 from the start", n, err)
	}
}

// Two runners of one projection that read the same batch race to advance its
// checkpoint. The loser's writes, sent in the same round trip as its claim,
// roll back with it, so every entry is applied once.
func TestConcurrentRunnersApplyEachEntryOnce(t *testing.T) {
	// Not parallel: the global log waits on open transactions in every database
	// on the server, so other tests would hold it back.
	agg := newCounter(t)
	projection := seenProjection(t, agg)
	for _, id := range []string{"a", "b", "a", "c", "b"} {
		if _, err := agg.Handle(ctx, id, add{1, 9}); err != nil {
			t.Fatal(err)
		}
	}

	// Both runners read the checkpoint and their entries before either sends
	// its batch.
	var applied atomic.Int64
	apply := projection.Apply
	projection.Apply = func(ctx context.Context, b *ledger.Batch, e ledger.Entry) error {
		if applied.Add(1) <= 2 {
			time.Sleep(100 * time.Millisecond)
		}
		return apply(ctx, b, e)
	}
	var wg sync.WaitGroup
	var total atomic.Int64
	for range 2 {
		wg.Go(func() {
			n, err := agg.Store.CatchUp(ctx, projection)
			if err != nil {
				t.Error(err)
			}
			total.Add(int64(n))
		})
	}
	wg.Wait()

	if total.Load() != 5 || countSeen(t, agg) != 5 {
		t.Fatalf("applied %d entries, %d rows; want 5 each", total.Load(), countSeen(t, agg))
	}
	if applied.Load() <= 5 {
		t.Fatalf("Apply ran %d times: the runners never raced", applied.Load())
	}
}

func TestOutboxRetriesUntilDelivered(t *testing.T) {
	t.Parallel()
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
		Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects, Batch: 1,
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

// A dispatcher whose lease ran out must not record an outcome for an effect
// another dispatcher has since claimed.
func TestAnExpiredClaimCannotRecordAnOutcome(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	if _, err := agg.Handle(ctx, "a", add{1, 9}); err != nil {
		t.Fatal(err)
	}
	var calls []string
	var slow *ledger.Dispatcher[effect]
	fast := &ledger.Dispatcher[effect]{
		Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects,
		Handle: func(_ context.Context, d ledger.Delivery[effect]) error {
			calls = append(calls, fmt.Sprintf("fast@%d", d.Attempt))
			return nil
		},
	}
	slow = &ledger.Dispatcher[effect]{
		Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects, Lease: 50 * time.Millisecond,
		Handle: func(ctx context.Context, d ledger.Delivery[effect]) error {
			calls = append(calls, fmt.Sprintf("slow@%d", d.Attempt))
			<-ctx.Done() // outlives its lease
			time.Sleep(50 * time.Millisecond)
			if _, err := fast.DrainOnce(context.Background()); err != nil {
				t.Error(err)
			}
			return errors.New("timed out")
		},
	}
	if _, err := slow.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}

	var attempts int
	var done, failed bool
	var lastError *string
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT attempts, done_at IS NOT NULL, failed_at IS NOT NULL, last_error
FROM cairn.outbox`).Scan(&attempts, &done, &failed, &lastError); err != nil {
		t.Fatal(err)
	}
	if fmt.Sprint(calls) != "[slow@1 fast@2]" || attempts != 2 || !done || failed || lastError != nil {
		t.Fatalf("calls %v, attempts %d, done %v, failed %v, last error %v", calls, attempts, done, failed, lastError)
	}
}

// An effect whose dispatchers keep dying before recording an outcome is given
// up on rather than claimed forever.
func TestAnEffectClaimedTooOftenWithoutAnOutcomeIsFailed(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	if _, err := agg.Handle(ctx, "a", add{1, 9}); err != nil {
		t.Fatal(err)
	}
	// Two claims whose dispatchers died: attempts counted, nothing recorded.
	if _, err := agg.Store.Pool().Exec(ctx, `UPDATE cairn.outbox SET attempts = 2`); err != nil {
		t.Fatal(err)
	}
	d := &ledger.Dispatcher[effect]{
		Store: agg.Store, Kind: "counter", Effects: agg.Codecs.Effects, MaxAttempts: 2,
		Handle: func(context.Context, ledger.Delivery[effect]) error {
			t.Error("an effect past MaxAttempts must not be delivered again")
			return nil
		},
	}
	if _, err := d.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}
	var failed bool
	if err := agg.Store.Pool().QueryRow(ctx, `SELECT failed_at IS NOT NULL FROM cairn.outbox`).Scan(&failed); err != nil {
		t.Fatal(err)
	}
	if !failed {
		t.Fatal("effect not marked failed")
	}
}

func TestVerifyReportsWhereTheCurrentDeciderDisagrees(t *testing.T) {
	t.Parallel()
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
	if len(divergences) != 1 || divergences[0].Aspect != "refusal" || divergences[0].Stream != agg.Stream("b") {
		t.Fatalf("divergences: %v", divergences)
	}
	t.Log(divergences[0])
}

// Version 2 of the counter's result: the total gained the cap it was checked
// against, and a v1 result can't say what that was.
type totalV2 struct {
	Total int `json:"total"`
	Cap   int `json:"cap"`
}

func TestVerifyComparesTheFieldsAStoredResultCarries(t *testing.T) {
	t.Parallel()
	agg := newCounter(t)
	for _, n := range []int{3, 4} {
		if _, err := agg.Handle(ctx, "a", add{n, 9}); err != nil {
			t.Fatal(err)
		}
	}

	// The next release returns a struct, registered as version 2 of "total".
	// Its upcaster can carry the total over but has to leave the cap out.
	v2 := func(upcast codec.Upcaster, decide func(counter, add) totalV2) []ledger.Divergence {
		results := codec.New[totalV2]()
		codec.Register[totalV2](results, "total", 2)
		results.Upcast("total", 1, upcast)
		next := &ledger.Aggregate[counter, msg, totalV2, event, effect]{
			Store: agg.Store, Kind: "counter",
			Codecs: &codec.Set[msg, totalV2, event, effect]{Messages: agg.Codecs.Messages, Results: results, Events: agg.Codecs.Events, Effects: agg.Codecs.Effects},
			Decider: cairn.Decider[counter, msg, totalV2, event, effect]{
				Initial: counterDecider.Initial,
				Evolve:  counterDecider.Evolve,
				Decide: func(s counter, m msg) cairn.Decision[totalV2, event, effect] {
					d := decide(s, m.(add))
					a := m.(add)
					return cairn.Accept[totalV2, event, effect](d, []event{added{a.N}}, announce{s.Total + a.N})
				},
			},
		}
		divergences, err := next.Verify(ctx, "a")
		if err != nil {
			t.Fatal(err)
		}
		return divergences
	}
	totalOnly := func(old json.RawMessage) (json.RawMessage, error) {
		return []byte(`{"total":` + string(old) + `}`), nil
	}
	same := func(s counter, a add) totalV2 { return totalV2{Total: s.Total + a.N, Cap: a.Cap} }

	if d := v2(totalOnly, same); len(d) != 0 {
		t.Errorf("a field the entries predate must not diverge: %v", d)
	}
	if d := v2(totalOnly, func(s counter, a add) totalV2 { return totalV2{Total: s.Total + a.N + 1} }); len(d) != 2 || d[0].Aspect != "result" {
		t.Errorf("a field the entries carry must still be compared: %v", d)
	}
	// An upcaster that can fill the new field in has it checked: these
	// entries say the cap was 10, and the decider says 9.
	withCap := func(old json.RawMessage) (json.RawMessage, error) {
		return []byte(`{"total":` + string(old) + `,"cap":10}`), nil
	}
	if d := v2(withCap, same); len(d) != 2 || d[0].Aspect != "result" {
		t.Errorf("a field the upcaster fills in must be compared: %v", d)
	}
}

func TestTheLibraryKeepsItsOwnGuarantees(t *testing.T) {
	t.Parallel()
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

	err := agg.Store.Transact(ctx, []ledger.Stream{agg.Stream("a")}, func(tx *ledger.Tx) error {
		if _, err := agg.LoadLocked(ctx, tx, "b"); !errors.Is(err, ledger.ErrNotLocked) {
			t.Errorf("loading an unlocked stream for writing: %v", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
