package ledger_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

func TestMetadataIsRecordedWithTheEntry(t *testing.T) {
	agg := newCounter(t)
	occurred := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	request := ledger.ContextWithMeta(ctx, ledger.Meta{
		Actor: "u_ana", Via: "ui", CorrelationID: "req-1", Extra: map[string]any{"ip": "10.0.0.1"},
	})

	// The option sets what the request did not, and wins where both do.
	h, err := agg.Handle(request, "a", add{1, 10}, ledger.WithMeta(ledger.Meta{
		Via: "api", OccurredAt: occurred, Extra: map[string]any{"client": "cli"},
	}))
	if err != nil {
		t.Fatal(err)
	}
	want := ledger.Meta{
		Actor: "u_ana", Via: "api", CorrelationID: "req-1", OccurredAt: occurred,
		Extra: map[string]any{"ip": "10.0.0.1", "client": "cli"},
	}
	entries, err := agg.Store.ReadStream(ctx, agg.Stream("a"), 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, got := range []ledger.Meta{h.Entry.Meta, entries[0].Meta} {
		got.OccurredAt = got.OccurredAt.UTC()
		if fmt.Sprint(got) != fmt.Sprint(want) {
			t.Fatalf("recorded %+v, want %+v", got, want)
		}
	}

	// A message with none records none.
	if _, err := agg.Handle(ctx, "a", add{1, 10}); err != nil {
		t.Fatal(err)
	}
	entries, _ = agg.Store.ReadStream(ctx, agg.Stream("a"), 1)
	if fmt.Sprint(entries[0].Meta) != fmt.Sprint(ledger.Meta{}) {
		t.Fatalf("a message with no metadata recorded %+v", entries[0].Meta)
	}
}

func TestAStoreThatRequiresAnActorRefusesToRecordWithoutOne(t *testing.T) {
	agg := newCounter(t)
	agg.Store = ledgertest.NewStore(t, ledger.RequireActor(true))

	if _, err := agg.Handle(ctx, "a", add{1, 10}); !errors.Is(err, ledger.ErrNoActor) {
		t.Fatalf("want ErrNoActor, got %v", err)
	}
	if entries, _ := agg.Store.ReadStream(ctx, agg.Stream("a"), 0); len(entries) != 0 {
		t.Fatalf("recorded without an actor: %+v", entries)
	}
	if _, err := agg.Handle(ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "u_ana"}), "a", add{1, 10}); err != nil {
		t.Fatal(err)
	}
}

func TestStoresRequireAnActorByDefault(t *testing.T) {
	agg := newCounter(t)
	// The same database, opened the way an application would.
	agg.Store = ledger.New(agg.Store.Pool())
	if _, err := agg.Handle(ctx, "a", add{1, 10}); !errors.Is(err, ledger.ErrNoActor) {
		t.Fatalf("a store built with New must require an actor, got %v", err)
	}
}

func TestTheActorAMessageNamesFillsAndGuardsItsMetadata(t *testing.T) {
	agg := newCounter(t)
	agg.Store = ledgertest.NewStore(t, ledger.RequireActor(true))
	// The counter's message has no "by" field, so its cap stands in for one.
	agg.Actor = func(m msg) string { return fmt.Sprintf("u_%d", m.(add).Cap) }

	h, err := agg.Handle(ctx, "a", add{1, 10})
	if err != nil {
		t.Fatalf("the message names its actor, so none is needed in context: %v", err)
	}
	if h.Entry.Meta.Actor != "u_10" {
		t.Fatalf("actor %q, want the one the message names", h.Entry.Meta.Actor)
	}

	as := ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "u_99"})
	if _, err := agg.Handle(as, "a", add{1, 10}); !errors.Is(err, ledger.ErrActorMismatch) {
		t.Fatalf("a request acting as someone else must be refused, got %v", err)
	}
	if entries, _ := agg.Store.ReadStream(ctx, agg.Stream("a"), 0); len(entries) != 1 {
		t.Fatalf("the mismatched message was recorded: %d entries", len(entries))
	}
}

func TestAPerCallActorCannotStandInForTheRequestsActor(t *testing.T) {
	agg := newCounter(t)
	agg.Actor = func(m msg) string { return fmt.Sprintf("u_%d", m.(add).Cap) }

	as := ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "u_ana"})
	_, err := agg.Handle(as, "a", add{1, 10}, ledger.WithMeta(ledger.Meta{Actor: "u_10"}))
	if !errors.Is(err, ledger.ErrActorMismatch) {
		t.Fatalf("the message names u_10 and the request carried u_ana; want ErrActorMismatch, got %v", err)
	}
}

func TestAMessageThatNamesNoActorKeepsTheRequestsActor(t *testing.T) {
	agg := newCounter(t)
	agg.Actor = func(msg) string { return "" }
	h, err := agg.Handle(ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "u_ana"}), "a", add{1, 10})
	if err != nil || h.Entry.Meta.Actor != "u_ana" {
		t.Fatalf("actor %q, %v; want the request's", h.Entry.Meta.Actor, err)
	}
}

func TestMetadataReadFromAContextIsACopy(t *testing.T) {
	carrying := ledger.ContextWithMeta(ctx, ledger.Meta{Extra: map[string]any{"a": 1}})
	ledger.MetaFrom(carrying).Extra["b"] = 2
	derived := ledger.ContextWithMeta(carrying, ledger.Meta{Actor: "u_ana"})
	ledger.MetaFrom(derived).Extra["c"] = 3
	for _, c := range []context.Context{carrying, derived} {
		if got := ledger.MetaFrom(c).Extra; len(got) != 1 {
			t.Fatalf("a write to a copy reached the context: %v", got)
		}
	}
}

// A retry is answered from the first entry before its metadata is checked, so
// one that lost the request's metadata still gets the answer.
func TestARetryWithoutMetadataGetsTheFirstAnswer(t *testing.T) {
	agg := newCounter(t)
	agg.Store = ledgertest.NewStore(t, ledger.RequireActor(true))
	as := ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "u_ana"})
	if _, err := agg.Handle(as, "a", add{1, 10}, ledger.IdempotencyKey("k")); err != nil {
		t.Fatal(err)
	}
	h, err := agg.Handle(ctx, "a", add{1, 10}, ledger.IdempotencyKey("k"))
	if err != nil || !h.Duplicate || h.Result != 1 {
		t.Fatalf("want the first answer, got %+v %v", h, err)
	}
}

func TestARetryKeepsTheFirstMessagesMetadata(t *testing.T) {
	agg := newCounter(t)
	first := ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "u_ana", CorrelationID: "req-1"})
	if _, err := agg.Handle(first, "a", add{1, 10}, ledger.IdempotencyKey("k")); err != nil {
		t.Fatal(err)
	}
	retry := ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "u_ana", CorrelationID: "req-2"})
	h, err := agg.Handle(retry, "a", add{1, 10}, ledger.IdempotencyKey("k"))
	if err != nil || !h.Duplicate {
		t.Fatalf("want a duplicate, got %+v %v", h, err)
	}
	if h.Entry.Meta.CorrelationID != "req-1" {
		t.Fatalf("the retry's metadata replaced the first's: %+v", h.Entry.Meta)
	}
}

// A message recorded while carrying out an effect is attributed to the
// effect: same actor and flow, caused by the delivery.
func TestAMessageRecordedByAnEffectHandlerIsCausedByTheDelivery(t *testing.T) {
	agg := newCounter(t)
	request := ledger.ContextWithMeta(ctx, ledger.Meta{Actor: "u_ana", Via: "ui", CorrelationID: "req-1"})
	if _, err := agg.Handle(request, "a", add{2, 10}); err != nil {
		t.Fatal(err)
	}

	var delivered ledger.Delivery[effect]
	d := &ledger.Dispatcher[effect]{
		Store: agg.Store, Kind: "counter", Effects: agg.Effects,
		Handle: func(ctx context.Context, dl ledger.Delivery[effect]) error {
			delivered = dl
			_, err := agg.Handle(ctx, "audit", add{1, 1000})
			return err
		},
	}
	if n, err := d.DrainOnce(ctx); err != nil || n != 1 {
		t.Fatalf("drained %d, %v", n, err)
	}
	if delivered.Meta.Actor != "u_ana" || delivered.Meta.CorrelationID != "req-1" {
		t.Fatalf("the delivery should carry its entry's metadata, got %+v", delivered.Meta)
	}
	entries, err := agg.Store.ReadStream(ctx, agg.Stream("audit"), 0)
	if err != nil || len(entries) != 1 {
		t.Fatalf("audit entries %+v, %v", entries, err)
	}
	want := ledger.Meta{Actor: "u_ana", Via: "outbox", CorrelationID: "req-1", CausationID: "counter/a#1.0"}
	if fmt.Sprint(entries[0].Meta) != fmt.Sprint(want) {
		t.Fatalf("caused message recorded %+v, want %+v", entries[0].Meta, want)
	}
}

// With no correlation ID, the flow starts at the entry that produced the
// effect; with no actor, the outbox is the actor, so a follow-up is not
// refused by a store that requires one.
func TestAnEffectStartsAFlowWhenItsEntryHadNone(t *testing.T) {
	agg := newCounter(t)
	if _, err := agg.Handle(ctx, "a", add{2, 10}); err != nil {
		t.Fatal(err)
	}
	var got ledger.Meta
	d := &ledger.Dispatcher[effect]{
		Store: agg.Store, Kind: "counter", Effects: agg.Effects,
		Handle: func(ctx context.Context, _ ledger.Delivery[effect]) error {
			got = ledger.MetaFrom(ctx)
			return nil
		},
	}
	if _, err := d.DrainOnce(ctx); err != nil {
		t.Fatal(err)
	}
	if got.CorrelationID != "counter/a#1" || got.CausationID != "counter/a#1.0" || got.Actor != "outbox" {
		t.Fatalf("handler context carried %+v", got)
	}
}

// A view reads who changed a stream from the entry that caused the write, and
// a rebuild hands it the same entry a live write did.
func TestAViewWritesTheActorFromTheEntryThatCausedIt(t *testing.T) {
	agg := newCounter(t)
	if _, err := agg.Store.Pool().Exec(ctx,
		`CREATE TABLE edited (id text PRIMARY KEY, total int NOT NULL, updated_by text NOT NULL)`); err != nil {
		t.Fatal(err)
	}
	view := ledger.View[counter]{Name: "edited", Write: func(b *ledger.Batch, at ledger.Loaded[counter], cause ledger.Entry) error {
		b.Queue(`
INSERT INTO edited VALUES ($1, $2, $3)
ON CONFLICT (id) DO UPDATE SET total = excluded.total, updated_by = excluded.updated_by`,
			at.Stream.ID, at.State.Total, cause.Meta.Actor)
		return nil
	}}
	agg.Views = []ledger.View[counter]{view}
	read := func() string {
		var total int
		var by string
		if err := agg.Store.Pool().QueryRow(ctx, `SELECT total, updated_by FROM edited WHERE id = 'a'`).Scan(&total, &by); err != nil {
			t.Fatal(err)
		}
		return fmt.Sprintf("%d by %s", total, by)
	}

	as := func(actor string) context.Context { return ledger.ContextWithMeta(ctx, ledger.Meta{Actor: actor}) }
	if _, err := agg.Handle(as("u_ana"), "a", add{2, 10}); err != nil {
		t.Fatal(err)
	}
	if _, err := agg.Handle(as("u_ben"), "a", add{3, 10}); err != nil {
		t.Fatal(err)
	}
	// A refusal changes nothing, so it is not the latest change either.
	_, _ = agg.Handle(as("u_cy"), "a", add{50, 10})
	if got := read(); got != "5 by u_ben" {
		t.Fatalf("live view: %s", got)
	}

	if _, err := agg.Store.Pool().Exec(ctx, `TRUNCATE edited`); err != nil {
		t.Fatal(err)
	}
	if _, err := agg.Rebuild(ctx, view); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != "5 by u_ben" {
		t.Fatalf("rebuilt view: %s, want what the live write produced", got)
	}
}
