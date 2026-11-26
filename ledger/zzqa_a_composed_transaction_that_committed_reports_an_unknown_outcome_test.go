package ledger_test

// Store.Transact is the ledger's public API for a transaction that spans
// several streams: the caller composes LoadLocked and Apply inside it, and
// Transact sends the COMMIT. When the connection goes away before the COMMIT's
// CommandComplete is read, the transaction is durable on the server, but
// Transact reports the failure as it receives it — "conn closed", wrapping
// neither ErrRecorded nor ErrUnknownOutcome. A caller therefore reads a
// possibly-recorded write as a definite failure and is free to delete the data
// its entry names, which is what examples/decisions does with the sender's
// member row when Supersede's commit response is lost.
//
// The wrapper below drops the read that carries the COMMIT's CommandComplete
// and fails every read after it, so the client never learns that the
// transaction committed. The test proves the entry is in the ledger before it
// asserts that the error says the outcome is not a definite failure.

import (
	"bytes"
	"context"
	"errors"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// zzqaCommitComplete is the server's CommandComplete for a COMMIT: message
// type 'C' and the payload "COMMIT\x00".
var zzqaCommitComplete = []byte("COMMIT\x00")

var errZZQAConnectionLost = errors.New("qa: the connection was lost after COMMIT")

// zzqaLostCommitConn is a connection whose reads stop at the COMMIT's
// CommandComplete: those bytes are dropped and every later read fails, so the
// client never learns the transaction committed.
type zzqaLostCommitConn struct {
	net.Conn
	mu   sync.Mutex
	lost bool
	// tail holds the end of what earlier reads delivered, so a COMMIT tag
	// split across two reads is still recognised before it is passed on.
	tail []byte
}

func (c *zzqaLostCommitConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	lost := c.lost
	c.mu.Unlock()
	if lost {
		return 0, errZZQAConnectionLost
	}
	n, err := c.Conn.Read(p)
	if n > 0 {
		scan := append(append([]byte(nil), c.tail...), p[:n]...)
		if bytes.Contains(scan, zzqaCommitComplete) {
			c.mu.Lock()
			c.lost = true
			c.mu.Unlock()
			return 0, errZZQAConnectionLost
		}
		if len(scan) > len(zzqaCommitComplete) {
			c.tail = scan[len(scan)-len(zzqaCommitComplete):]
		} else {
			c.tail = scan
		}
	}
	return n, err
}

// zzqaStoreThatLosesItsCommit opens the same database as healthy, over
// connections that never deliver the COMMIT's CommandComplete.
func zzqaStoreThatLosesItsCommit(t *testing.T, healthy *ledger.Store) *ledger.Store {
	t.Helper()
	cfg := healthy.Pool().Config().Copy()
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
		return &zzqaLostCommitConn{Conn: conn}, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("opening the pool that loses its commit: %v", err)
	}
	t.Cleanup(pool.Close)
	return ledger.New(pool, ledger.RequireActor(false))
}

func TestAComposedTransactionThatCommittedReportsAnUnknownOutcome(t *testing.T) {
	t.Parallel()
	healthy := ledgertest.NewStore(t, ledger.RequireActor(false))
	lossy := counterOn(zzqaStoreThatLosesItsCommit(t, healthy))

	err := lossy.Store.Transact(ctx, []ledger.Stream{lossy.Stream("a")}, func(tx *ledger.Tx) error {
		at, err := lossy.LoadLocked(ctx, tx, "a")
		if err != nil {
			return err
		}
		_, err = lossy.Apply(ctx, tx, &at, add{1, 10})
		return err
	})
	if err == nil {
		t.Fatal("Transact returned no error, so the connection did not fail at the commit")
	}

	// The premise: the server committed, so the caller's request was recorded
	// even though it was told it failed.
	entries, readErr := healthy.ReadStream(ctx, ledger.Stream{Kind: "counter", ID: "a"}, 0)
	if readErr != nil {
		t.Fatalf("reading the stream back over a healthy connection: %v", readErr)
	}
	if len(entries) != 1 {
		t.Fatalf("premise: the transaction recorded %d entries, not 1, so its outcome was a definite failure", len(entries))
	}

	// A caller told "unknown" or "recorded" keeps the data the entry names;
	// a plain failure tells it the write did not happen.
	if !errors.Is(err, ledger.ErrUnknownOutcome) && !errors.Is(err, ledger.ErrRecorded) {
		t.Fatalf("Transact returned %v for a transaction whose entry %s#%d is in the ledger: "+
			"the caller cannot tell the outcome from a rollback, so it may delete data the entry names",
			err, entries[0].Stream, entries[0].Seq)
	}
}
