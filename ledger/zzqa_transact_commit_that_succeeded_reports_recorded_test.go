package ledger_test

import (
	"encoding/binary"
	"errors"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

// A transaction whose COMMIT reached the server comes back from
// Store.Transact as a plain error when the connection dies after the server
// reports COMMIT but before the client is told the connection is ready. The
// transaction is durable, but the error does not wrap ErrRecorded, which is
// the signal the README's "Told when a message was recorded" guarantee gives a
// caller for exactly this window ("closing the batch on a lost connection
// fails after the commit has happened"). A caller that follows the usual rule
// for a failed transaction -- replay it -- records the message twice.
//
// Handle reports this case through Batch.send. Transact commits through
// pgx.BeginFunc's tx.Commit, which does not read the COMMIT tag's outcome, so
// the same window is not reported.
func TestATransactWhoseCommitSucceededReportsTheMessageRecorded(t *testing.T) {
	t.Parallel()

	// A migrated database, reached directly for setup and for looking at what
	// the transaction actually did.
	direct := ledgertest.NewStore(t, ledger.RequireActor(false))

	// The same database, reached through a proxy that drops the connection
	// immediately after the server's CommandComplete for COMMIT: the commit is
	// durable, and the client never reads ReadyForQuery.
	proxy := newCommitDropProxy(t, direct.Pool().Config())
	cfg := direct.Pool().Config().Copy()
	cfg.ConnConfig.Host, cfg.ConnConfig.Port = proxy.host, proxy.port
	cfg.MaxConns = 1
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	proxied := ledger.New(pool, ledger.RequireActor(false))
	proxiedAgg := counterOn(proxied)

	proxy.arm()
	applied := false
	err = proxied.Transact(ctx, []ledger.Stream{proxiedAgg.Stream("a")}, func(tx *ledger.Tx) error {
		at, err := proxiedAgg.LoadLocked(ctx, tx, "a")
		if err != nil {
			return err
		}
		_, err = proxiedAgg.Apply(ctx, tx, &at, add{1, 10})
		applied = err == nil
		return err
	})
	if !applied {
		t.Fatalf("the message was not applied inside the transaction: %v", err)
	}
	if err == nil {
		t.Fatal("Transact returned nil after the commit's connection was dropped")
	}

	// Confirm the transaction committed: the entry Apply queued is in the
	// ledger, seen over a connection the proxy did not touch.
	var entries int
	if err := direct.Pool().QueryRow(ctx,
		`SELECT count(*) FROM cairn.entries WHERE stream = $1`,
		proxiedAgg.Stream("a").String()).Scan(&entries); err != nil {
		t.Fatal(err)
	}
	if entries != 1 {
		t.Fatalf("the entry the transaction applied is not in the ledger (%d entries); "+
			"the test did not reproduce a transaction that committed", entries)
	}

	if !errors.Is(err, ledger.ErrRecorded) {
		t.Fatalf("Transact returned %v; the transaction committed, so the error must report that "+
			"the message was recorded, the way Handle does for the same lost connection", err)
	}
}

// commitDropProxy forwards PostgreSQL's wire protocol and, once armed, drops
// the client connection immediately after forwarding the server's
// CommandComplete for COMMIT. The server has committed at that point; the
// client has read the command tag but not ReadyForQuery.
type commitDropProxy struct {
	host string
	port uint16
	ln   net.Listener
	up   string

	mu    sync.Mutex
	armed bool
	conns []net.Conn
}

func newCommitDropProxy(t *testing.T, upstream *pgxpool.Config) *commitDropProxy {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	p := &commitDropProxy{
		host: "127.0.0.1",
		port: uint16(ln.Addr().(*net.TCPAddr).Port),
		ln:   ln,
		up:   net.JoinHostPort(upstream.ConnConfig.Host, strconv.Itoa(int(upstream.ConnConfig.Port))),
	}
	t.Cleanup(func() {
		_ = ln.Close()
		p.mu.Lock()
		for _, c := range p.conns {
			_ = c.Close()
		}
		p.mu.Unlock()
	})
	go p.serve()
	return p
}

// arm makes the next COMMIT observed drop its connection.
func (p *commitDropProxy) arm() {
	p.mu.Lock()
	p.armed = true
	p.mu.Unlock()
}

func (p *commitDropProxy) serve() {
	for {
		client, err := p.ln.Accept()
		if err != nil {
			return
		}
		server, err := net.Dial("tcp", p.up)
		if err != nil {
			_ = client.Close()
			continue
		}
		p.track(client, server)
		go func() { _, _ = io.Copy(server, client); _ = server.Close() }()
		go p.relay(client, server)
	}
}

func (p *commitDropProxy) track(conns ...net.Conn) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.conns = append(p.conns, conns...)
}

// relay copies the server's messages to the client, framed as PostgreSQL's
// protocol frames them, and drops both connections after a COMMIT's
// CommandComplete once armed.
func (p *commitDropProxy) relay(client, server net.Conn) {
	defer client.Close()
	defer server.Close()
	var header [5]byte
	for {
		if _, err := io.ReadFull(server, header[:]); err != nil {
			return
		}
		length := binary.BigEndian.Uint32(header[1:])
		if length < 4 {
			return
		}
		body := make([]byte, length-4)
		if _, err := io.ReadFull(server, body); err != nil {
			return
		}
		if _, err := client.Write(header[:]); err != nil {
			return
		}
		if _, err := client.Write(body); err != nil {
			return
		}
		if header[0] == 'C' && string(body) == "COMMIT\x00" {
			p.mu.Lock()
			drop := p.armed
			p.mu.Unlock()
			if drop {
				// The command tag is on its way; ReadyForQuery is not. The
				// server has committed.
				return
			}
		}
	}
}
