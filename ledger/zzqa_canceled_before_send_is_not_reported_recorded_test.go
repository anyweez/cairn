package ledger

import (
	"context"
	"errors"
	"testing"
)

// A caller whose context is already canceled when the batch is sent has had
// none of its statements reach the database. Reporting ErrRecorded for it says
// the transaction committed when it never started, and a caller that trusts
// the error (as the README tells it to) does not retry, so the message is lost.
// The connection is open and idle, which is the same state a committed
// transaction leaves behind, so the outcome must come from the cancellation
// rather than from the connection.
func TestQACanceledBeforeSendIsNotReportedRecorded(t *testing.T) {
	t.Parallel()
	_, conn, rows := committedConn(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	var b Batch
	b.own(sqlStep("beginning a transaction"), `BEGIN`)
	b.own(insertOne, `INSERT INTO committed VALUES (1)`)
	b.end()

	err := b.send(ctx, conn)
	if err == nil {
		t.Fatal("send returned nil for a canceled batch, want its context error")
	}
	if errors.Is(err, ErrRecorded) {
		t.Fatalf("send reported the message recorded for a batch whose context was already canceled, but the connection never sent it and the batch inserted %d rows: %v", rows(), err)
	}
}
