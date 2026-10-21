#!/usr/bin/env bash
# QA: an interrupted run of the ledger's prepared-transaction test leaves its
# prepared transaction behind, and each one holds a slot of the fixture's
# max_prepared_transactions (scripts/test-postgres.sh sets it to 16). Once all
# 16 slots are held, every later run of TestAnOlderPreparedTransactionIsNotSkipped
# fails at its own PREPARE with:
#
#     ERROR: maximum number of prepared transactions reached (SQLSTATE 53200)
#
# The test derives its transaction identifier from the database name, so a
# killed run no longer collides by name (t2739), but nothing rolls the leaked
# transactions back: the fixture reuses its container and never inspects
# pg_prepared_xacts. The only way out is `docker rm -f cairn-test-postgres`,
# which neither the gate nor the fixture's output says.
#
# This seeds the state interrupted runs leave -- one prepared transaction per
# slot, named exactly as the test names them -- then runs the fixture and the
# required test. It exits non-zero while starting the fixture leaves that
# state in place, and passes once starting the fixture clears prepared
# transactions left in its databases.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -z "${CAIRN_TEST_DATABASE_URL:-}" ]; then
  CAIRN_TEST_DATABASE_URL=$(scripts/test-postgres.sh)
fi
export CAIRN_TEST_DATABASE_URL
export CAIRN_REQUIRE_DATABASE=1

psql() { docker exec -i cairn-test-postgres psql -U cairn "$@"; }

if ! psql -d postgres -Atc 'SELECT 1' >/dev/null 2>&1; then
  echo "SKIP: this reproduction drives the docker fixture cairn-test-postgres" >&2
  exit 2
fi

slots=$(psql -d postgres -Atc 'SHOW max_prepared_transactions')
if [ "$slots" -le 0 ]; then
  echo "SKIP: max_prepared_transactions is 0, so there are no slots to exhaust" >&2
  exit 2
fi

seed=qa_prepared_slot
cleanup() {
  for i in $(seq 1 "$slots"); do
    db=${seed}_$i
    psql -d "$db" -Atc "ROLLBACK PREPARED 'cairn_test_prepared_$db'" >/dev/null 2>&1 || true
    psql -d postgres -Atc "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null 2>&1 || true
  done
}
trap cleanup EXIT

# Leave behind exactly what each interrupted run leaves: a database whose
# prepared transaction is still open.
for i in $(seq 1 "$slots"); do
  db=${seed}_$i
  psql -d postgres -Atc "DROP DATABASE IF EXISTS $db WITH (FORCE)" >/dev/null 2>&1 || true
  psql -d postgres -Atc "CREATE DATABASE $db" >/dev/null
  psql -d "$db" -Atc \
    "CREATE TABLE t(x int); BEGIN; INSERT INTO t VALUES (1); PREPARE TRANSACTION 'cairn_test_prepared_$db'" >/dev/null
done

held=$(psql -d postgres -Atc 'SELECT count(*) FROM pg_prepared_xacts')
echo "seeded $held of $slots prepared-transaction slots"

# Start (or reuse) the fixture, which is what an interrupted run is followed
# by. A fixture that recovers from the leaked state clears the slots here.
scripts/test-postgres.sh >/dev/null

held=$(psql -d postgres -Atc 'SELECT count(*) FROM pg_prepared_xacts')
log=$(mktemp)
set +e
go test -count=1 -timeout 2m -run '^TestAnOlderPreparedTransactionIsNotSkipped$' ./ledger/ >"$log" 2>&1
code=$?
set -e

if [ "$code" -eq 0 ]; then
  echo "PASS: the required test ran with $held leaked prepared transactions still held"
  exit 0
fi
cat "$log"
echo
if grep -q 'maximum number of prepared transactions reached' "$log"; then
  echo "FAIL: $held prepared transactions left by interrupted runs fill every"
  echo "      max_prepared_transactions slot, so the required test can no longer"
  echo "      PREPARE; starting the fixture did not clear them"
  exit 1
fi
echo "FAIL: the test failed for a reason other than exhausted prepared-transaction slots" >&2
exit 1
