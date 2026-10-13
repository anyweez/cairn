package decisions

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

func refused(t *testing.T, err error, code string) {
	t.Helper()
	if !errors.Is(err, &cairn.Refusal{Code: code}) {
		t.Fatalf("expected refusal %s, got %v", code, err)
	}
}

func must(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func TestRegisterOnTheLedger(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t))

	first, err := svc.Create(ctx, "ana", "Ship the beta?", "yes", "not yet")
	must(t, err)
	state, err := svc.Get(ctx, first)
	must(t, err)
	yes, notYet := state.Options[0].ID, state.Options[1].ID

	must(t, svc.SavePosition(ctx, first, "ana", yes, "it is ready"))
	must(t, svc.SavePosition(ctx, first, "ben", notYet, "two bugs left"))
	must(t, svc.RecordOutcome(ctx, first, "cy", yes, "the bugs are cosmetic"))

	// Frozen once decided.
	refused(t, svc.SavePosition(ctx, first, "ben", yes, "fine, yes"), Frozen)
	_, err = svc.AddOption(ctx, first, "ben", "ship to half")
	refused(t, err, Frozen)
	// Settled decisions do not move sideways.
	refused(t, svc.SetStatus(ctx, first, "ben", Dropped), NotAllowed)

	must(t, svc.Acknowledge(ctx, first, "ben", true))

	// A second decision replaces the first.
	second, err := svc.Create(ctx, "ana", "Ship the beta to everyone?", "yes")
	must(t, err)
	refused(t, svc.Supersede(ctx, first, second, "ana"), NotAllowed) // the replacement is still open
	refused(t, svc.Supersede(ctx, first, "", "ana"), Invalid)
	// Reading a replacement that does not exist leaves no stream behind.
	refused(t, svc.Supersede(ctx, first, "dec-missing", "ana"), NotAllowed)
	if streams, err := svc.Register.Store.Streams(ctx, "decision"); err != nil || len(streams) != 2 {
		t.Fatalf("only the two real decisions have streams: %v, %v", streams, err)
	}
	state, err = svc.Get(ctx, second)
	must(t, err)
	must(t, svc.RecordOutcome(ctx, second, "cy", state.Options[0].ID, "metrics held"))
	must(t, svc.Supersede(ctx, first, second, "ana"))

	// Superseding back would make the chain circular. It is refused because the
	// first decision, read under lock, is superseded rather than decided.
	refused(t, svc.Supersede(ctx, second, first, "ana"), NotAllowed)

	state, err = svc.Get(ctx, first)
	must(t, err)
	if state.Status != Superseded || state.SupersededBy != second || state.Positions["ben"].Committed == nil {
		t.Fatalf("first decision: %+v", state)
	}

	// Reopening erases the outcome and every answer to it.
	must(t, svc.SetStatus(ctx, first, "ana", Open))
	state, err = svc.Get(ctx, first)
	must(t, err)
	if state.Status != Open || state.Outcome != nil || state.SupersededBy != "" || state.Positions["ben"].Committed != nil {
		t.Fatalf("reopened decision: %+v", state)
	}

	// Every stored decision is one the current decider would make again.
	divergences, err := svc.Register.VerifyAll(ctx)
	must(t, err)
	if len(divergences) != 0 {
		t.Fatalf("divergences: %v", divergences)
	}

	// The reference-index effects were queued in the same transactions.
	var indexed []string
	dispatcher := &ledger.Dispatcher[Effect]{
		Store: svc.Register.Store, Kind: "decision", Effects: svc.Register.Effects,
		Handle: func(_ context.Context, d ledger.Delivery[Effect]) error {
			indexed = append(indexed, d.Effect.(IndexReferences).Decision)
			return nil
		},
	}
	for {
		n, err := dispatcher.DrainOnce(ctx)
		must(t, err)
		if n == 0 {
			break
		}
	}
	// Two creates and two recorded outcomes.
	if len(indexed) != 4 {
		t.Fatalf("indexed %v", indexed)
	}
}

// Two supersedes in opposite directions, racing. Lore locks both rows in ref
// order to stop them committing a two-decision loop; here Lock orders the
// streams, and the loser reads the winner's result.
func TestOppositeSupersedesCannotBothWin(t *testing.T) {
	ctx := context.Background()
	svc := NewService(ledgertest.NewStore(t))

	var ids []string
	for range 2 {
		id, err := svc.Create(ctx, "ana", "which?", "this")
		must(t, err)
		state, err := svc.Get(ctx, id)
		must(t, err)
		must(t, svc.RecordOutcome(ctx, id, "ana", state.Options[0].ID, "because"))
		ids = append(ids, id)
	}

	for range 20 {
		var wg sync.WaitGroup
		errs := make([]error, 2)
		wg.Go(func() { errs[0] = svc.Supersede(ctx, ids[0], ids[1], "ana") })
		wg.Go(func() { errs[1] = svc.Supersede(ctx, ids[1], ids[0], "ben") })
		wg.Wait()

		if (errs[0] == nil) == (errs[1] == nil) {
			t.Fatalf("exactly one supersede must win: %v", errs)
		}
		for _, err := range errs {
			if err != nil && !errors.As(err, new(*cairn.Refusal)) {
				t.Fatal(err)
			}
		}

		// Put both back to decided-and-unlinked for the next round.
		for _, id := range ids {
			state, err := svc.Get(ctx, id)
			must(t, err)
			if state.Status == Superseded {
				must(t, svc.SetStatus(ctx, id, "ana", Open))
				state, err = svc.Get(ctx, id)
				must(t, err)
				must(t, svc.RecordOutcome(ctx, id, "ana", state.Options[0].ID, "because"))
			}
		}
	}
}
