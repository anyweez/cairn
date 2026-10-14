package ledger_test

// Throughput of Handle over a slow link. The benchmarks reach Postgres through
// a toxiproxy that adds latency to every response, so each round trip costs
// what it would across a network. Run them with:
//
//	eval "$(scripts/bench-postgres.sh)"
//	go test ./ledger -run '^$' -bench Handle -benchtime 5s
//
// CAIRN_BENCH_RTT_MS picks the added round-trip times (default "0,2,5").
// Each load runs with the state cache off, then on, suffixed /cache. Besides
// ns/op, each reports msgs/s, p50 and p99 latency, round trips and statements
// per message, and the round trip it measured before starting.

import (
	"bytes"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/anyweez/cairn/ledger"
	"github.com/anyweez/cairn/ledger/ledgertest"
)

type benchLoad struct {
	name      string
	callers   int // goroutines calling Handle
	pool      int // connections
	hot       bool
	instances int  // stores, each with its own cache, as separate processes would have
	cache     bool // with the store's state cache on
}

var benchLoads = withCache([]benchLoad{
	// One caller on one stream: one message's full latency.
	{name: "hot/callers=1", callers: 1, pool: 1, hot: true},
	// Callers queued on one stream's lock: the most it can take.
	{name: "hot/callers=8", callers: 8, pool: 8, hot: true},
	// One stream's callers split between two processes, as an unrouted load
	// balancer would leave them: each process's cache keeps going stale.
	{name: "hot/callers=2/instances=2", callers: 2, pool: 2, hot: true, instances: 2},
	// More callers split the same way: the stream's lock caps them however
	// many there are, where routed to one process they'd share transactions.
	{name: "hot/callers=8/instances=2", callers: 8, pool: 8, hot: true, instances: 2},
	// A stream per caller and a connection per caller: no contention.
	{name: "spread/callers=32/pool=32", callers: 32, pool: 32},
	// The same callers on pgxpool's default size on a 4-CPU host.
	{name: "spread/callers=32/pool=4", callers: 32, pool: 4},
})

// withCache adds a cached run of every load after it.
func withCache(loads []benchLoad) []benchLoad {
	out := slices.Clone(loads)
	for _, l := range loads {
		l.name, l.cache = l.name+"/cache", true
		out = append(out, l)
	}
	return out
}

// BenchmarkHandle records messages shaped like an ordinary aggregate's:
// snapshots on, an idempotency key, one effect and one view each.
func BenchmarkHandle(b *testing.B) {
	toxiproxy, proxyAddr := os.Getenv("CAIRN_BENCH_TOXIPROXY"), os.Getenv("CAIRN_BENCH_PROXY_ADDR")
	if toxiproxy == "" || proxyAddr == "" {
		b.Skip("CAIRN_BENCH_TOXIPROXY is not set; run scripts/bench-postgres.sh")
	}
	rtts := os.Getenv("CAIRN_BENCH_RTT_MS")
	if rtts == "" {
		rtts = "0,2,5"
	}
	for _, field := range strings.Split(rtts, ",") {
		ms, err := strconv.Atoi(strings.TrimSpace(field))
		if err != nil {
			b.Fatalf("CAIRN_BENCH_RTT_MS: %v", err)
		}
		for _, load := range benchLoads {
			b.Run(fmt.Sprintf("rtt=%dms/%s", ms, load.name), func(b *testing.B) {
				setLatency(b, toxiproxy, ms)
				benchHandle(b, proxyAddr, load)
			})
		}
	}
	setLatency(b, toxiproxy, 0)
}

func benchHandle(b *testing.B, proxyAddr string, load benchLoad) {
	// Migrate directly, then measure through the proxy.
	direct := ledgertest.NewStore(b)
	cfg := direct.Pool().Config().Copy()
	host, port, err := net.SplitHostPort(proxyAddr)
	if err != nil {
		b.Fatal(err)
	}
	p, err := strconv.ParseUint(port, 10, 16)
	if err != nil {
		b.Fatal(err)
	}
	cfg.ConnConfig.Host, cfg.ConnConfig.Port = host, uint16(p)
	cfg.MaxConns, cfg.MinConns = int32(load.pool), int32(load.pool)
	trips := &tripCounter{}
	cfg.ConnConfig.Tracer = trips
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		b.Fatal(err)
	}
	defer pool.Close()

	opts := []ledger.StoreOption{ledger.RequireActor(false)}
	if !load.cache {
		opts = append(opts, ledger.StateCacheSize(0))
	}
	aggs := make([]*ledger.Aggregate[counter, msg, int, event, effect], max(load.instances, 1))
	for i := range aggs {
		aggs[i] = counterOn(ledger.New(pool, opts...))
		aggs[i].StateVersion = "bench/1"
		if i == 0 {
			aggs[i].Views = []ledger.View[counter]{totalsView(b, aggs[0])}
		} else {
			aggs[i].Views = aggs[0].Views
		}
	}

	stream := func(caller int) string {
		if load.hot {
			return "hot"
		}
		return fmt.Sprintf("s%d", caller)
	}
	var keys atomic.Int64
	handle := func(caller int) {
		key := ledger.IdempotencyKey(fmt.Sprintf("k%d", keys.Add(1)))
		agg := aggs[caller%len(aggs)]
		if _, err := agg.Handle(ctx, stream(caller), add{1, 1 << 30}, key); err != nil {
			b.Error(err)
		}
	}

	// Open every connection and prepare every statement on it before timing.
	run(load.callers, 3*load.pool, func(caller int) { handle(caller) })
	rtt := measureRTT(b, pool)
	trips.reset()

	latencies := make([][]time.Duration, load.callers)
	b.ResetTimer()
	start := time.Now()
	run(load.callers, b.N, func(caller int) {
		t := time.Now()
		handle(caller)
		latencies[caller] = append(latencies[caller], time.Since(t))
	})
	elapsed := time.Since(start)
	b.StopTimer()

	all := slices.Concat(latencies...)
	slices.Sort(all)
	ms := func(d time.Duration) float64 { return float64(d) / float64(time.Millisecond) }
	b.ReportMetric(float64(b.N)/elapsed.Seconds(), "msgs/s")
	b.ReportMetric(ms(all[len(all)/2]), "p50-ms")
	b.ReportMetric(ms(all[(len(all)*99)/100]), "p99-ms")
	b.ReportMetric(float64(trips.trips.Load())/float64(b.N), "trips/op")
	b.ReportMetric(float64(trips.statements.Load())/float64(b.N), "stmts/op")
	b.ReportMetric(ms(rtt), "rtt-ms")
}

// run calls fn n times in all, from callers goroutines, each passing its own
// index.
func run(callers, n int, fn func(caller int)) {
	var next atomic.Int64
	var wg sync.WaitGroup
	for c := range callers {
		wg.Go(func() {
			for next.Add(1) <= int64(n) {
				fn(c)
			}
		})
	}
	wg.Wait()
}

// measureRTT is the median of a few trivial queries: the link's round trip
// plus Postgres's least work.
func measureRTT(b *testing.B, pool *pgxpool.Pool) time.Duration {
	var took []time.Duration
	for range 21 {
		t := time.Now()
		if _, err := pool.Exec(ctx, `SELECT 1`); err != nil {
			b.Fatal(err)
		}
		took = append(took, time.Since(t))
	}
	slices.Sort(took)
	return took[len(took)/2]
}

// setLatency delays every response from Postgres by ms, which adds ms to each
// round trip.
func setLatency(b *testing.B, proxy string, ms int) {
	b.Helper()
	req, err := http.NewRequest(http.MethodDelete, proxy+"/toxics/latency", nil)
	if err != nil {
		b.Fatal(err)
	}
	if res, err := http.DefaultClient.Do(req); err != nil {
		b.Fatalf("toxiproxy: %v", err)
	} else {
		res.Body.Close()
	}
	if ms == 0 {
		return
	}
	body := fmt.Sprintf(`{"name":"latency","type":"latency","stream":"downstream","toxicity":1,"attributes":{"latency":%d,"jitter":0}}`, ms)
	res, err := http.Post(proxy+"/toxics", "application/json", bytes.NewBufferString(body))
	if err != nil {
		b.Fatalf("toxiproxy: %v", err)
	}
	res.Body.Close()
	if res.StatusCode != http.StatusOK {
		b.Fatalf("toxiproxy: adding latency: %s", res.Status)
	}
}

// tripCounter counts round trips: each query, and each batch however many
// statements it carries. BEGIN and COMMIT are queries too.
type tripCounter struct {
	trips, statements atomic.Int64
}

func (c *tripCounter) reset() { c.trips.Store(0); c.statements.Store(0) }

func (c *tripCounter) TraceQueryStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceQueryStartData) context.Context {
	c.trips.Add(1)
	c.statements.Add(1)
	return ctx
}

func (c *tripCounter) TraceQueryEnd(context.Context, *pgx.Conn, pgx.TraceQueryEndData) {}

func (c *tripCounter) TraceBatchStart(ctx context.Context, _ *pgx.Conn, _ pgx.TraceBatchStartData) context.Context {
	c.trips.Add(1)
	return ctx
}

func (c *tripCounter) TraceBatchQuery(context.Context, *pgx.Conn, pgx.TraceBatchQueryData) {
	c.statements.Add(1)
}

func (c *tripCounter) TraceBatchEnd(context.Context, *pgx.Conn, pgx.TraceBatchEndData) {}
