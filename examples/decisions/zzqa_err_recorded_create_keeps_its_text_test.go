package decisions

// The ledger tells a caller that a batch committed even when the connection
// died before its results could be read: the error wraps ErrRecorded, and
// README says the message is in the ledger. The register's shell keeps the
// words in its own table and deletes them when Handle fails, on the reading
// that a failed request left nothing behind. On ErrRecorded that reading is
// wrong: the entry exists and names refs, and the only rows holding the words
// are the ones the shell just deleted, so the decision is left with a title
// and bodies that name nothing, and nothing can bring them back.
//
// The failure needs the connection to die between the COMMIT and the
// ReadyForQuery that follows it. A net.Conn wrapper below reads the COMMIT's
// CommandComplete and then fails every later read, which is exactly the state
// ledger.ErrRecorded exists to report.

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

// commitComplete is the server's CommandComplete for a COMMIT: message type
// 'C', length 11, and the payload "COMMIT\x00".
var commitComplete = []byte("COMMIT\x00")

var errConnectionLost = errors.New("qa: the connection was lost after COMMIT")

// afterCommitConn delivers bytes until the COMMIT's CommandComplete has been
// read, then fails every read, so the ReadyForQuery that follows it is never
// seen. That is a connection lost after the transaction committed.
type afterCommitConn struct {
	net.Conn
	mu     sync.Mutex
	tail   []byte
	killed bool
}

func (c *afterCommitConn) Read(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.killed {
		return 0, errConnectionLost
	}
	n, err := c.Conn.Read(p)
	if n > 0 {
		// tail holds the end of what earlier reads delivered, so a commit
		// message split across two reads is still found.
		scan := append(append([]byte(nil), c.tail...), p[:n]...)
		if i := bytes.Index(scan, commitComplete); i >= 0 {
			c.killed = true
			delivered := i + len(commitComplete) - len(c.tail)
			if delivered < 0 {
				delivered = 0
			}
			if delivered < n {
				// Drop whatever followed the commit, the ReadyForQuery
				// included: the caller must see the connection go away.
				return delivered, nil
			}
			return n, nil
		}
		if len(scan) > len(commitComplete) {
			c.tail = scan[len(scan)-len(commitComplete):]
		} else {
			c.tail = scan
		}
	}
	return n, err
}

// TestACreateThatWasRecordedKeepsTheTextItsEntryNames drives one Create
// through a connection that dies after its COMMIT. The message is recorded, so
// the text its entry names must still be there for Get and the index to read.
func TestACreateThatWasRecordedKeepsTheTextItsEntryNames(t *testing.T) {
	ctx := context.Background()
	// A store on the same database, over a healthy connection, to read back
	// what the failed call recorded.
	verify := ledgertest.NewStore(t, ledger.RequireActor(false))

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
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("opening the pool that loses its connection: %v", err)
	}
	t.Cleanup(pool.Close)

	svc := NewService(ledger.New(pool, ledger.RequireActor(false)))
	id, err := svc.Create(ctx, "ana", "Ship the beta?", "yes", "not yet")

	// The premise: the ledger says the message was recorded, which is what
	// makes losing the text a loss rather than a tidied-up failed request.
	if !errors.Is(err, ledger.ErrRecorded) {
		t.Fatalf("Create returned %v, want an error wrapping ledger.ErrRecorded; "+
			"without that the request was not recorded and this test proves nothing", err)
	}

	// Read it back over the healthy store: the entry is in the ledger.
	reader := NewService(verify)
	state, err := reader.Get(ctx, id)
	if err != nil {
		t.Fatalf("the recorded decision could not be loaded: %v", err)
	}
	if state.Title == "" {
		t.Fatalf("the recorded entry names no title ref, so this test proves nothing")
	}
	if got := svc.Content.Text(state.Title); got != "Ship the beta?" {
		t.Fatalf("the recorded entry names title %q, but the register's table holds %q for it: "+
			"the text the entry can never carry itself was deleted because the connection failed after the commit", state.Title, got)
	}
	if len(state.Options) != 2 {
		t.Fatalf("want the two seeded options, got %+v", state.Options)
	}
	for i, want := range []string{"yes", "not yet"} {
		if got := svc.Content.Text(state.Options[i].Title); got != want {
			t.Fatalf("option %d's title ref %q holds %q, want %q: the seeded option's text was deleted with the title",
				i, state.Options[i].Title, got, want)
		}
	}
}
