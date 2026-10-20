package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
)

// A batch whose every statement succeeded, its COMMIT included, has recorded
// its message. pgx can still fail after that: BatchResults.Close waits for the
// server's ReadyForQuery, a round trip after the COMMIT's CommandComplete, and
// a connection lost in between returns an error. The caller must be told the
// message was recorded — ErrRecorded — or a retry without an idempotency key
// records it a second time.
func TestABatchThatCommittedBeforeACloseErrorSaysSo(t *testing.T) {
	t.Parallel()

	var b Batch
	b.own(sqlStep("beginning a transaction"), `BEGIN`)
	b.own(execResult, `INSERT INTO recorded VALUES (1)`)
	b.end()

	err := b.send(context.Background(), closeFailsSender{})
	if err == nil {
		t.Fatal("send returned nil, want the lost connection's error")
	}
	if !errors.Is(err, ErrRecorded) {
		t.Fatalf("send returned %v; every statement including COMMIT succeeded, so the message was recorded", err)
	}
}

// closeFailsSender answers every statement successfully, including the
// COMMIT, and then fails to close: the connection dies after the transaction
// committed but before the server resynchronises.
type closeFailsSender struct{}

// A batch that queues no COMMIT has nothing send can vouch for when Close
// fails: its statements may have rolled back, or a caller's transaction may
// still be open. Only a batch that ended its own transaction has a commit to
// report.
func TestABatchThatDidNotCommitDoesNotSaySoWhenCloseFails(t *testing.T) {
	t.Parallel()

	var b Batch
	b.own(sqlStep("beginning a transaction"), `BEGIN`)
	b.own(execResult, `INSERT INTO recorded VALUES (1)`)
	// The cached path used to send a batch that declared itself committing
	// without queuing a COMMIT.
	b.commits = true

	err := b.send(context.Background(), closeFailsSender{})
	if err == nil {
		t.Fatal("send returned nil, want the lost connection's error")
	}
	if errors.Is(err, ErrRecorded) {
		t.Fatalf("send returned %v, but the batch queued no COMMIT to have committed", err)
	}
}

func (closeFailsSender) SendBatch(_ context.Context, b *pgx.Batch) pgx.BatchResults {
	return &closeFailsResults{remaining: b.Len()}
}

type closeFailsResults struct {
	remaining int
}

func (r *closeFailsResults) Exec() (pgconn.CommandTag, error) {
	r.remaining--
	if r.remaining == 0 {
		return pgconn.NewCommandTag("COMMIT"), nil
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (r *closeFailsResults) Query() (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (r *closeFailsResults) QueryRow() pgx.Row { return nil }

func (r *closeFailsResults) Close() error { return errors.New("connection reset by peer") }
