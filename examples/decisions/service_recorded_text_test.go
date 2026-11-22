package decisions

// A message the ledger recorded has committed even when its result cannot be
// read back: the error wraps ledger.ErrRecorded. The shell stores the words in
// its own table and the entry names their refs, so the rows must survive. These
// tests drive every method that stores text through the same connection loss,
// and hold the cleanup for a request that really did record nothing.

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anyweez/cairn"
	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// lossyRegister returns a service over the same database as verify whose pool
// loses each connection once the COMMIT has completed, so Handle reports
// ErrRecorded: the message is in the ledger but its result is unreadable.
func lossyRegister(t *testing.T, verify *ledger.Store) *Service {
	t.Helper()
	cfg := verify.Pool().Config().Copy()
	cfg.MaxConns = 1
	cfg.MinConns = 0
	dial := cfg.ConnConfig.DialFunc
	if dial == nil {
		d := &net.Dialer{Timeout: 5 * time.Second}
		dial = d.DialContext
	}
	cfg.ConnConfig.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		conn, err := dial(ctx, network, addr)
		if err != nil {
			return nil, err
		}
		return &afterCommitConn{Conn: conn}, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("opening the pool that loses its connection: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewService(ledger.New(pool, ledger.RequireActor(false)))
}

func TestARecordedMessageKeepsTheTextItsEntryNames(t *testing.T) {
	ctx := context.Background()

	t.Run("AddOption", func(t *testing.T) {
		verify := ledgertest.NewStore(t, ledger.RequireActor(false))
		healthy := NewService(verify)
		id, err := healthy.Create(ctx, "ana", "Ship the beta?", "yes")
		must(t, err)

		lossy := lossyRegister(t, verify)
		optionID, err := lossy.AddOption(ctx, id, "ben", "not yet")
		if !errors.Is(err, ledger.ErrRecorded) {
			t.Fatalf("AddOption returned %v, want an error wrapping ledger.ErrRecorded", err)
		}

		state, err := healthy.Get(ctx, id)
		must(t, err)
		var title Ref
		for _, option := range state.Options {
			if option.ID == optionID {
				title = option.Title
			}
		}
		if title == "" {
			t.Fatalf("the recorded entry names no option title ref, so this test proves nothing")
		}
		if got := lossy.Content.Text(title); got != "not yet" {
			t.Fatalf("the recorded entry names option title %q, but the register's table holds %q for it", title, got)
		}
	})

	t.Run("SavePosition", func(t *testing.T) {
		verify := ledgertest.NewStore(t, ledger.RequireActor(false))
		healthy := NewService(verify)
		id, err := healthy.Create(ctx, "ana", "Ship the beta?", "yes")
		must(t, err)
		state, err := healthy.Get(ctx, id)
		must(t, err)

		lossy := lossyRegister(t, verify)
		err = lossy.SavePosition(ctx, id, "ana", state.Options[0].ID, "it is ready")
		if !errors.Is(err, ledger.ErrRecorded) {
			t.Fatalf("SavePosition returned %v, want an error wrapping ledger.ErrRecorded", err)
		}

		state, err = healthy.Get(ctx, id)
		must(t, err)
		position, ok := state.Positions[lossy.Content.Member("ana")]
		if !ok || position.Body == "" {
			t.Fatalf("the recorded entry names no position body ref, so this test proves nothing")
		}
		if got := lossy.Content.Text(position.Body); got != "it is ready" {
			t.Fatalf("the recorded entry names position body %q, but the register's table holds %q for it", position.Body, got)
		}
	})

	t.Run("RecordOutcome", func(t *testing.T) {
		verify := ledgertest.NewStore(t, ledger.RequireActor(false))
		healthy := NewService(verify)
		id, err := healthy.Create(ctx, "ana", "Ship the beta?", "yes")
		must(t, err)
		state, err := healthy.Get(ctx, id)
		must(t, err)

		lossy := lossyRegister(t, verify)
		err = lossy.RecordOutcome(ctx, id, "cy", state.Options[0].ID, "the bugs are cosmetic")
		if !errors.Is(err, ledger.ErrRecorded) {
			t.Fatalf("RecordOutcome returned %v, want an error wrapping ledger.ErrRecorded", err)
		}

		state, err = healthy.Get(ctx, id)
		must(t, err)
		if state.Outcome == nil || state.Outcome.Rationale == "" {
			t.Fatalf("the recorded entry names no rationale ref, so this test proves nothing")
		}
		if got := lossy.Content.Text(state.Outcome.Rationale); got != "the bugs are cosmetic" {
			t.Fatalf("the recorded entry names rationale %q, but the register's table holds %q for it", state.Outcome.Rationale, got)
		}
	})
}

// A request the ledger refused recorded nothing, so the text it stored must
// still be removed: keeping it for ErrRecorded must not keep it for everything.
func TestARefusedCreateLeavesNoTextBehind(t *testing.T) {
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))
	_, err := svc.Create(context.Background(), "   ", "Ship the beta?", "yes", "not yet")
	if !errors.As(err, new(*cairn.Refusal)) {
		t.Fatalf("Create returned %v, want a refusal", err)
	}
	if rows := storedRows(svc); rows != 0 {
		t.Fatalf("a refused Create left %d rows of text behind, want none", rows)
	}
}

// A guard refuses a request before anything is sent, so nothing names the text
// it stored and it must be removed: keeping text for an outcome the ledger
// cannot rule out must not turn into keeping it for every failure.
func TestACreateAGuardRefusesLeavesNoTextBehind(t *testing.T) {
	svc := NewService(ledgertest.NewStore(t, ledger.RequireActor(false)))
	// The message names its sender as its actor, so metadata naming somebody
	// else is refused before the message reaches the ledger.
	ctx := ledger.ContextWithMeta(context.Background(), ledger.Meta{Actor: "ben"})
	_, err := svc.Create(ctx, "ana", "Ship the beta?", "yes", "not yet")
	if !errors.Is(err, ledger.ErrActorMismatch) {
		t.Fatalf("Create returned %v, want ErrActorMismatch", err)
	}
	if rows := storedRows(svc); rows != 0 {
		t.Fatalf("a Create a guard refused left %d rows of text behind, want none", rows)
	}
}

// storedRows is how many rows the register's own table holds.
func storedRows(svc *Service) int {
	svc.Content.mu.Lock()
	defer svc.Content.mu.Unlock()
	return len(svc.Content.text)
}
