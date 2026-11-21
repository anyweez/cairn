package decisions

// The ledger can tell a caller that a message was recorded only when it manages
// to read the COMMIT's CommandComplete: that is what ledger.ErrRecorded
// reports, and the shell now keeps the text such an entry names
// (zzqa_err_recorded_create_keeps_its_text_test.go). A connection that goes
// away *at* the COMMIT's response — the response never reaches the client, so
// the tag is never read — is a different shape of the same lost outcome: the
// transaction is durable on the server, but Handle can only report an ordinary
// failure, and the shell reads that as "nothing was recorded" and deletes the
// only copy of the decision's words.
//
// The wrapper below drops the read that carries the COMMIT's CommandComplete
// and fails every read after it, so the server's commit is never seen by the
// client. The assertions prove the entry exists before they show the text is
// gone, so the test cannot pass by making the request fail before it was
// recorded.

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

// dropCommitConn is a connection whose reads stop at the COMMIT's
// CommandComplete: the bytes that carry it are dropped, and every later read
// fails, so the client never learns that the transaction committed.
type dropCommitConn struct {
	net.Conn
	mu   sync.Mutex
	lost bool
	// tail holds the end of what earlier reads delivered, so a COMMIT tag
	// split across two reads is still recognised before it is passed on.
	tail []byte
}

func (c *dropCommitConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	lost := c.lost
	c.mu.Unlock()
	if lost {
		return 0, errConnectionLost
	}
	n, err := c.Conn.Read(p)
	if n > 0 {
		scan := append(append([]byte(nil), c.tail...), p[:n]...)
		if bytes.Contains(scan, commitComplete) {
			// The server only writes this after the COMMIT is durable. Drop
			// the bytes rather than deliver the tag, and fail every read
			// after it.
			c.mu.Lock()
			c.lost = true
			c.mu.Unlock()
			return 0, errConnectionLost
		}
		if len(scan) > len(commitComplete) {
			c.tail = scan[len(scan)-len(commitComplete):]
		} else {
			c.tail = scan
		}
	}
	return n, err
}

// commitLostRegister returns a service over the same database as verify whose
// connections never deliver the COMMIT's CommandComplete to the client.
func commitLostRegister(t *testing.T, verify *ledger.Store) *Service {
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
		return &dropCommitConn{Conn: conn}, nil
	}
	pool, err := pgxpool.NewWithConfig(context.Background(), cfg)
	if err != nil {
		t.Fatalf("opening the pool that loses its commit: %v", err)
	}
	t.Cleanup(pool.Close)
	return NewService(ledger.New(pool, ledger.RequireActor(false)))
}

// A Create that the server committed keeps the text its entry names, even when
// the client never read the commit's response and so was told the request
// failed. Anything less leaves a decision nothing can answer with: the entry
// holds refs and the words are gone.
func TestACreateKeepsItsTextWhenTheCommitResponseIsLost(t *testing.T) {
	ctx := context.Background()
	verify := ledgertest.NewStore(t, ledger.RequireActor(false))
	lossy := commitLostRegister(t, verify)

	id, err := lossy.Create(ctx, "ana", "Ship the beta?", "yes", "not yet")
	if err == nil {
		t.Fatalf("Create returned no error, so the connection did not fail at the commit")
	}
	// The premise: the caller was told the request failed, because the ledger
	// could not read the commit's result. Whether it was recorded is exactly
	// what the caller cannot know, and what a fix has to be safe about.
	if errors.Is(err, ledger.ErrRecorded) {
		t.Fatalf("Create returned ErrRecorded (%v); this shape of lost commit is handled elsewhere", err)
	}

	state, err := NewService(verify).Get(ctx, id)
	if err != nil {
		t.Fatalf("reading the decision back over a healthy connection: %v", err)
	}
	if state.Title == "" || len(state.Options) != 2 {
		t.Fatalf("premise: the Create was not recorded (state %+v), so no text was lost", state)
	}

	if got := lossy.Content.Text(state.Title); got != "Ship the beta?" {
		t.Fatalf("the recorded entry names the question %q, but the register's table holds %q for it: "+
			"the text a commit-lost request stored was deleted even though its entry exists", state.Title, got)
	}
	for i, want := range []string{"yes", "not yet"} {
		if got := lossy.Content.Text(state.Options[i].Title); got != want {
			t.Fatalf("the recorded entry names option %d's title %q, but the register's table holds %q for it",
				i, state.Options[i].Title, got)
		}
	}
}
