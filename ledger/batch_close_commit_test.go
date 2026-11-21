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

// A batch whose COMMIT response never arrives is neither a commit the client
// saw nor a failure the database reported: the transaction may have committed.
// The caller must be told the outcome is unknown, so a caller holding data the
// entry would name keeps it rather than deleting the only copy.
func TestABatchWhoseCommitResponseIsLostSaysTheOutcomeIsUnknown(t *testing.T) {
	t.Parallel()

	var b Batch
	b.own(sqlStep("beginning a transaction"), `BEGIN`)
	b.own(execResult, `INSERT INTO recorded VALUES (1)`)
	b.end()

	err := b.send(context.Background(), commitLostSender{})
	if err == nil {
		t.Fatal("send returned nil, want the lost connection's error")
	}
	if errors.Is(err, ErrRecorded) {
		t.Fatalf("send returned %v; the COMMIT's result was never read, so it cannot say the message was recorded", err)
	}
	if !errors.Is(err, ErrUnknownOutcome) {
		t.Fatalf("send returned %v, want an error wrapping ErrUnknownOutcome", err)
	}
}

// commitLostSender answers every statement up to the COMMIT, and fails that
// one with the connection gone: the tag carrying its outcome never arrives.
type commitLostSender struct{}

func (commitLostSender) SendBatch(_ context.Context, b *pgx.Batch) pgx.BatchResults {
	return &commitLostResults{remaining: b.Len()}
}

type commitLostResults struct {
	remaining int
}

func (r *commitLostResults) Exec() (pgconn.CommandTag, error) {
	r.remaining--
	if r.remaining == 0 {
		return pgconn.CommandTag{}, errors.New("unexpected EOF")
	}
	return pgconn.NewCommandTag("INSERT 0 1"), nil
}

func (r *commitLostResults) Query() (pgx.Rows, error) {
	return nil, errors.New("unexpected Query")
}

func (r *commitLostResults) QueryRow() pgx.Row { return nil }

func (r *commitLostResults) Close() error { return nil }

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
