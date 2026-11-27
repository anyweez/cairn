package decisions

// A caller whose request times out or is cancelled gives up on the message it
// sent, but the ledger may still record it: handleCached returns the caller's
// ctx.Err() as soon as its context ends, while the group it was waiting on can
// go on to commit. The register's cleanup reads every error that is not
// ErrRecorded or ErrUnknownOutcome as "nothing was recorded" and deletes the
// text the message stored, so a committed entry is left naming refs whose rows
// are gone.
//
// The connection below stalls the read that follows the COMMIT's
// CommandComplete, so the server has committed and the client has read the
// commit tag, but the recording group cannot finish. The test cancels the
// caller then, waits for Create to return, and only then lets the recording
// group finish. That makes the order deterministic: the entry is durable, and
// the caller was answered with context.Canceled rather than a recorded error.

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

// stallAfterCommitConn delivers the COMMIT's CommandComplete and then blocks
// the next read until release is closed, failing it. By the time it blocks the
// server has committed and the client has read the commit tag, so the
// recording group has a durable entry it has not been able to finish.
//
// One is made for each connection the pool dials: when the caller gives up,
// pgx dials one of its own to send a cancellation request, and that connection
// must pass through rather than stall as well.
type stallAfterCommitConn struct {
	net.Conn
	shared *stalledCommit
	mu     sync.Mutex
	tail   []byte
	stall  bool
}

// stalledCommit is what the test shares with every connection the pool dials:
// the signal that the COMMIT has been read, and the release of the read after
// it.
type stalledCommit struct {
	ready   chan struct{}
	release chan struct{}
	once    sync.Once
}

func (c *stallAfterCommitConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	if c.stall {
		c.mu.Unlock()
		<-c.shared.release
		return 0, errConnectionLost
	}
	tail := c.tail
	c.mu.Unlock()

	n, err := c.Conn.Read(p)
	if n > 0 {
		scan := append(append([]byte(nil), tail...), p[:n]...)
		if i := bytes.Index(scan, commitComplete); i >= 0 {
			delivered := i + len(commitComplete) - len(tail)
			if delivered < 0 {
				delivered = 0
			}
			c.mu.Lock()
			c.stall = true
			c.mu.Unlock()
			c.shared.once.Do(func() { close(c.shared.ready) })
			if delivered < n {
				// Drop whatever followed the commit, ReadyForQuery included.
				return delivered, nil
			}
			return n, nil
		}
		if len(scan) > len(commitComplete) {
			tail = scan[len(scan)-len(commitComplete):]
		} else {
			tail = scan
		}
		c.mu.Lock()
		c.tail = tail
		c.mu.Unlock()
	}
	return n, err
}

func TestACancelledCreateKeepsTheTextItsRecordedEntryNames(t *testing.T) {
	verify := ledgertest.NewStore(t, ledger.RequireActor(false))

	stall := &stalledCommit{
		ready:   make(chan struct{}),
		release: make(chan struct{}),
	}
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
		return &stallAfterCommitConn{Conn: conn, shared: stall}, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("opening the pool: %v", err)
	}
	t.Cleanup(pool.Close)

	svc := NewService(ledger.New(pool, ledger.RequireActor(false)))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	type answer struct {
		id  string
		err error
	}
	answered := make(chan answer, 1)
	go func() {
		id, err := svc.Create(ctx, "ana", "Ship the beta?", "yes", "not yet")
		answered <- answer{id, err}
	}()

	// The COMMIT has been read by the server and the client; only the read
	// that follows it is outstanding. Cancel the caller, then wait for Create
	// to take the cancellation path before letting the recording group finish.
	select {
	case <-stall.ready:
	case <-time.After(10 * time.Second):
		t.Fatal("the Create never reached its COMMIT")
	}
	cancel()
	got := <-answered
	close(stall.release)

	if !errors.Is(got.err, context.Canceled) {
		t.Fatalf("Create returned %v, want the caller's context cancellation; "+
			"this shape needs the message to be in flight when its caller gives up", got.err)
	}

	// Read back over a healthy connection: the entry is there.
	state, err := NewService(verify).Get(context.Background(), got.id)
	if err != nil {
		t.Fatalf("reading the recorded decision back: %v", err)
	}
	if state.Title == "" || len(state.Options) != 2 {
		t.Fatalf("premise: the Create was not recorded (state %+v), so no text was lost", state)
	}
	if gotText := svc.Content.Text(state.Title); gotText != "Ship the beta?" {
		t.Fatalf("the recorded entry names title %q, but the register's table holds %q for it: "+
			"the text a request whose caller gave up stored was deleted even though its entry exists",
			state.Title, gotText)
	}
	for i, want := range []string{"yes", "not yet"} {
		if gotText := svc.Content.Text(state.Options[i].Title); gotText != want {
			t.Fatalf("the recorded entry names option %d's title %q, but the register's table holds %q for it, want %q",
				i, state.Options[i].Title, gotText, want)
		}
	}
}
