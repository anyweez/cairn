package decisions

// A Create keeps the sender's member row only when the shell believes the
// ledger recorded the message: Content.done releases a row no recorded entry
// names. When the caller gives up while its message is in a cached lane,
// handleCached answers with a bare context.Canceled (ledger/cache.go) even
// though the group may go on and commit, so Service.Create reads the error as
// "nothing was recorded" and the deferred Content.done releases the row.
//
// The entry that was recorded names that member id as its actor. Releasing the
// row splits one person across two identities in an append-only history: the
// recorded actor no longer resolves to a name, and the same person's next
// request mints a second id.
//
// The connection below delivers the COMMIT's CommandComplete and then stalls,
// so the server has committed and the client has read the tag, but the
// recording group cannot finish and the caller is still waiting. Cancelling
// the caller at that point gives it context.Canceled while the entry is in the
// ledger. The test proves the entry exists before it checks the member row, so
// it cannot pass by making the request fail before it was recorded.

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

// stallControl is shared by every connection the pool opens: the first
// connection to reach its COMMIT closes stalled, and every connection waits
// for release before delivering the ReadyForQuery that follows. Both
// cancellation reproductions in this package use it: this one, and
// TestACancelledCreateKeepsTheTextItsRecordedEntryNames in
// zzqa_cancelled_create_keeps_its_text_test.go.
type stallControl struct {
	stalled chan struct{}
	release chan struct{}

	stallOnce   sync.Once
	releaseOnce sync.Once
}

func newStallControl() *stallControl {
	return &stallControl{stalled: make(chan struct{}), release: make(chan struct{})}
}

func (c *stallControl) letGo() { c.releaseOnce.Do(func() { close(c.release) }) }

// stallAfterCommitConn delivers the COMMIT's CommandComplete and withholds
// everything after it — the ReadyForQuery that follows — until release is
// closed. The recording group is past the point of no return: Postgres has
// committed, and the client has read the tag.
type stallAfterCommitConn struct {
	net.Conn
	control *stallControl

	mu       sync.Mutex
	tail     []byte
	pending  []byte
	reached  bool
	released bool
}

func (c *stallAfterCommitConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if c.reached && !c.released {
		c.control.stallOnce.Do(func() { close(c.control.stalled) })
		c.mu.Unlock()
		<-c.control.release
		c.mu.Lock()
		c.released = true
	}
	if len(c.pending) > 0 {
		n := copy(p, c.pending)
		c.pending = c.pending[n:]
		c.mu.Unlock()
		return n, nil
	}
	if c.reached {
		c.mu.Unlock()
		return c.Conn.Read(p)
	}

	n, err := c.Conn.Read(p)
	if n == 0 {
		c.mu.Unlock()
		return n, err
	}
	// tail holds the end of what earlier reads delivered, so a COMMIT tag
	// split across two reads is still found.
	scan := append(append([]byte(nil), c.tail...), p[:n]...)
	if i := bytes.Index(scan, commitComplete); i >= 0 {
		end := i + len(commitComplete) - len(c.tail)
		if end < 0 {
			end = 0
		}
		if end > n {
			end = n
		}
		c.reached = true
		c.tail = nil
		// Withhold whatever followed the tag: the ReadyForQuery must arrive
		// in a later read, which stalls.
		c.pending = append(c.pending, p[end:n]...)
		c.mu.Unlock()
		return end, nil
	}
	if len(scan) > len(commitComplete) {
		c.tail = append(c.tail[:0], scan[len(scan)-len(commitComplete):]...)
	} else {
		c.tail = append(c.tail[:0], scan...)
	}
	c.mu.Unlock()
	return n, err
}

// stallAfterCommitRegister returns a service over the same database as verify
// whose connection delivers the COMMIT tag and then blocks before the
// ReadyForQuery. It is shared with the text-row reproduction in
// zzqa_cancelled_create_keeps_its_text_test.go.
func stallAfterCommitRegister(t *testing.T, verify *ledger.Store) (*Service, <-chan struct{}, func()) {
	t.Helper()
	control := newStallControl()
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
		return &stallAfterCommitConn{Conn: conn, control: control}, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("opening the pool that stalls after the commit: %v", err)
	}
	t.Cleanup(pool.Close)
	svc := NewService(ledger.New(pool, ledger.RequireActor(false)))
	return svc, control.stalled, control.letGo
}

func TestACancelledCreateKeepsTheSendersMemberRow(t *testing.T) {
	verify := ledgertest.NewStore(t, ledger.RequireActor(false))
	lossy, stalled, release := stallAfterCommitRegister(t, verify)
	defer release()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	type created struct {
		id  string
		err error
	}
	result := make(chan created, 1)
	go func() {
		id, err := lossy.Create(ctx, "ana", "Ship the beta?", "yes")
		result <- created{id: id, err: err}
	}()

	select {
	case <-stalled:
	case <-time.After(15 * time.Second):
		t.Fatal("the connection never reached the COMMIT the test cancels after")
	}
	cancel()

	var got created
	select {
	case got = <-result:
	case <-time.After(15 * time.Second):
		t.Fatal("Create did not return after its caller was cancelled")
	}
	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Create returned %v, want context.Canceled; the test did not reproduce the cancellation shape", got.err)
	}
	// Let the recording group finish now that the caller has returned.
	release()

	// The premise: the entry is in the ledger.
	reader := NewService(verify)
	state, err := reader.Get(context.Background(), got.id)
	if err != nil {
		t.Fatalf("reading the decision back over a healthy connection: %v", err)
	}
	if state.Status != Open || state.Title == "" {
		t.Fatalf("premise: the cancelled Create was not recorded (state %+v), so no member row was needed", state)
	}
	entries, err := verify.ReadStream(context.Background(), ledger.Stream{Kind: "decision", ID: got.id}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("premise: the stream holds no entries")
	}
	actor := Member(entries[0].Meta.Actor)
	if actor == "" {
		t.Fatal("premise: the recorded Create names no actor")
	}

	// The recorded entry names this member id, so the register must still
	// resolve it to the name the request used.
	if name := lossy.Content.Name(actor); name != "ana" {
		t.Fatalf("the recorded Create is attributed to member %q, but the register's table resolves it to %q: "+
			"the sender's row was released for a request the ledger recorded", actor, name)
	}
	// And the same person's next request must keep the one identity the
	// ledger's history already uses.
	if again := lossy.Content.Member("ana"); again != actor {
		t.Fatalf("a later request by the same person minted member %q, not the recorded %q: one person now has two ids "+
			"in an append-only history", again, actor)
	}
}
