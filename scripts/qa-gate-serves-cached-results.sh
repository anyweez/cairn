#!/usr/bin/env bash
# scripts/check.sh runs `go test -race ./...` without -count=1, so Go's test
# cache decides whether the ledger's Postgres-backed tests run at all. The
# cache key holds the test binary, the flags and the environment variables the
# tests read — CAIRN_TEST_DATABASE_URL among them — but not the server behind
# that URL. So once a green run has been recorded, a later run with the same
# URL reports success without connecting: the tests never see the database.
#
# That defeats the point of the gate exporting CAIRN_REQUIRE_DATABASE=1, and of
# TestAnOlderPreparedTransactionIsNotSkipped failing when the server cannot
# prepare transactions. Replace the fixture on the same port with a stock
# Postgres (max_prepared_transactions = 0) and the gate still prints `ok`.
#
# This script seeds the cache against a server that can prepare transactions,
# swaps in one that cannot, and then requires the gate to notice. It fails
# today, with scripts/check.sh exiting 0, and passes once the gate makes the
# tests run (for example with `go test -race -count=1 ./...`).
set -euo pipefail
cd "$(dirname "$0")/.."

name=cairn-test-postgres
port=55871
url="postgres://cairn:cairn@127.0.0.1:${port}/postgres?sslmode=disable"

restore() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  # Put the fixture back the way the run found it.
  scripts/test-postgres.sh >/dev/null 2>&1 || true
}
trap restore EXIT

# 1. A green gate run against a server the ledger tests need, so their results
#    are in the cache.
docker rm -f "$name" >/dev/null 2>&1 || true
scripts/test-postgres.sh >/dev/null
export CAIRN_TEST_DATABASE_URL="$url"
unset CAIRN_REQUIRE_DATABASE
if ! scripts/check.sh >/dev/null; then
  echo "SETUP: the gate did not pass against a healthy fixture, so there is nothing to cache" >&2
  exit 2
fi

# 2. Replace the server on the same port and the same URL with one that cannot
#    prepare transactions, the stock configuration.
docker rm -f "$name" >/dev/null
docker run -d --name "$name" -p "127.0.0.1:${port}:5432" \
  -e POSTGRES_PASSWORD=cairn -e POSTGRES_USER=cairn postgres:17-alpine >/dev/null
for _ in $(seq 1 60); do
  docker exec "$name" pg_isready -h 127.0.0.1 -U cairn >/dev/null 2>&1 && ready=1 && break
  sleep 0.5
done
if [ -z "${ready:-}" ]; then
  echo "SETUP: the replacement Postgres did not become ready" >&2
  exit 2
fi
got=$(docker exec "$name" psql -U cairn -d postgres -Atc 'SHOW max_prepared_transactions')
if [ "$got" != 0 ]; then
  echo "SETUP: the replacement server has max_prepared_transactions=$got, want 0" >&2
  exit 2
fi

# The test the gate requires, run for real, cannot pass against this server.
if CAIRN_REQUIRE_DATABASE=1 go test -race ./ledger/ \
    -run '^TestAnOlderPreparedTransactionIsNotSkipped$' -count=1 >/dev/null 2>&1; then
  echo "SETUP: the fence test passed against max_prepared_transactions=0" >&2
  exit 2
fi

# 3. The gate, on the same tree and the same URL, must not report success.
log=$(mktemp)
if scripts/check.sh >"$log" 2>&1; then
  grep -E '^(ok|FAIL|---)' "$log" || true
  echo
  echo "FAIL: scripts/check.sh exited 0 while the server behind CAIRN_TEST_DATABASE_URL"
  echo "      could not run TestAnOlderPreparedTransactionIsNotSkipped; the ledger tests"
  echo "      were served from Go's test cache instead of run"
  exit 1
fi
echo "ok: the gate ran the ledger tests again and refused a server that cannot prepare transactions"
