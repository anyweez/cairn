#!/usr/bin/env bash
# scripts/test-postgres.sh, scripts/test-pgbouncer.sh and
# scripts/bench-postgres.sh each publish a port when they create their
# container, and reuse a running container on a later run. The URL they print
# is built from the port asked for (CAIRN_TEST_PORT, CAIRN_TEST_PGBOUNCER_PORT,
# CAIRN_TOXIPROXY_PORT), not the port the reused container publishes, so a run
# that changes the port prints an address nothing listens on and every test or
# benchmark against it is refused.
#
# t2574 is the same defect in scripts/test-postgres.sh. This covers the other
# two fixtures, whose ports have their own variables.
#
#   bash scripts/qa-reused-container-changed-port.sh
set -uo pipefail

cd "$(dirname "$0")/.."

pgbouncer_port=${CAIRN_QA_PGBOUNCER_PORT:-55893}
proxy_port=${CAIRN_QA_PROXY_PORT:-55894}

is_open() { (exec 3<>/dev/tcp/127.0.0.1/"$1") >/dev/null 2>&1; }

# port_of_url prints the port of a postgres:// URL.
port_of_url() { sed -E 's#^[^@]*@[^:/]*:([0-9]+)(/|$).*#\1#' <<<"$1"; }

failures=0
fail() {
  echo "FAIL: $*" >&2
  failures=$((failures + 1))
}

# The pgbouncer fixture.
scripts/test-pgbouncer.sh >/dev/null 2>&1 || fail "scripts/test-pgbouncer.sh failed on the default port"
url=$(CAIRN_TEST_PGBOUNCER_PORT=$pgbouncer_port scripts/test-pgbouncer.sh 2>/dev/null)
port=$(port_of_url "$url")
echo "test-pgbouncer.sh with CAIRN_TEST_PGBOUNCER_PORT=$pgbouncer_port printed: $url"
if [ "$port" != "$pgbouncer_port" ]; then
  fail "test-pgbouncer.sh printed port $port, not the requested $pgbouncer_port"
fi
if ! is_open "$port"; then
  fail "nothing listens on $port, so every test pointed at that URL is refused"
fi

# The toxiproxy fixture the benchmarks use.
scripts/bench-postgres.sh >/dev/null 2>&1 || fail "scripts/bench-postgres.sh failed on the default port"
out=$(CAIRN_TOXIPROXY_PORT=$proxy_port scripts/bench-postgres.sh 2>/dev/null)
addr=$(sed -nE "s/^export CAIRN_BENCH_PROXY_ADDR='([^']+)'\$/\1/p" <<<"$out")
echo "bench-postgres.sh with CAIRN_TOXIPROXY_PORT=$proxy_port printed: CAIRN_BENCH_PROXY_ADDR=$addr"
if [ -z "$addr" ]; then
  fail "bench-postgres.sh printed no CAIRN_BENCH_PROXY_ADDR"
elif ! is_open "${addr##*:}"; then
  fail "nothing listens on ${addr##*:}, so every benchmark over the proxy is refused"
fi

if [ "$failures" -gt 0 ]; then
  echo
  echo "$failures fixture(s) printed an address on a port it never publishes."
  echo "Recreate the container when the requested port differs from the one it"
  echo "publishes, or print the port the running container actually maps."
  exit 1
fi
echo "both fixtures printed a port they publish"
