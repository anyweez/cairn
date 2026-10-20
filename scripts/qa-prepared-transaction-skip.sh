#!/usr/bin/env bash
# The gate exports CAIRN_REQUIRE_DATABASE=1 so the ledger's Postgres-backed
# tests fail instead of skipping. But TestAnOlderPreparedTransactionIsNotSkipped
# skips when the server's max_prepared_transactions is 0, which every Postgres
# that CAIRN_TEST_DATABASE_URL is documented to point at (the README and
# factory.yaml both allow an external server) has by default. The skip is
# silent, so scripts/check.sh reports success with the LEAST fence that test
# proves untested.
#
# This starts a throwaway Postgres with max_prepared_transactions=0, points the
# gate at it, and fails while the gate exits 0. It passes once the gate refuses
# to accept the skip, whether that guard lives in the test or in check.sh.
set -euo pipefail

cd "$(dirname "$0")/.."

name=cairn-qa-prepared-skip
port=${QA_PREPARED_SKIP_PORT:-56171}
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

docker run -d --name "$name" -p "127.0.0.1:${port}:5432" \
  -e POSTGRES_PASSWORD=cairn -e POSTGRES_USER=cairn postgres:17-alpine >/dev/null
for _ in $(seq 1 60); do
  docker exec "$name" pg_isready -h 127.0.0.1 -U cairn >/dev/null 2>&1 && ready=1 && break
  sleep 0.5
done
if [ -z "${ready:-}" ]; then
  echo "postgres in $name did not become ready" >&2
  exit 2
fi

got=$(docker exec "$name" psql -U cairn -d postgres -Atc 'SHOW max_prepared_transactions')
if [ "$got" != "0" ]; then
  echo "fixture has max_prepared_transactions=$got, want 0" >&2
  exit 2
fi

export CAIRN_TEST_DATABASE_URL="postgres://cairn:cairn@127.0.0.1:${port}/postgres?sslmode=disable"

log=$(mktemp)
if scripts/check.sh >"$log" 2>&1; then
  cat "$log"
  echo
  echo "FAIL: scripts/check.sh exited 0 while max_prepared_transactions=0"
  echo "      made TestAnOlderPreparedTransactionIsNotSkipped skip"
  exit 1
fi

echo "PASS: the gate refused to pass with the prepared-transaction fence test skipped"
