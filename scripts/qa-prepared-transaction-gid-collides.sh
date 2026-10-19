#!/usr/bin/env bash
# Reproduces the state a failed or interrupted run of
# TestAnOlderPreparedTransactionIsNotSkipped leaves behind, and the failure it
# causes on the next run.
#
# The test prepares a transaction with a hard-coded, cluster-global name:
#
#     PREPARE TRANSACTION 'cairn_test_prepared'
#
# It only COMMITs (or rolls back) that name on the happy path. If the test
# fails after the PREPARE, or the process is killed mid-test, the prepared
# transaction stays. The name is global to the Postgres cluster, not to the
# freshly-created database, so every later run of the test — in any database —
# fails at its own PREPARE with:
#
#     ERROR: transaction identifier "cairn_test_prepared" is already in use
#
# The leaked database cannot be dropped either: DROP DATABASE ... WITH (FORCE)
# refuses while a prepared transaction uses it, and ledgertest ignores the
# error. One leaked prepared transaction also consumes one of the cluster's
# max_prepared_transactions slots, so enough of them disable PREPARE
# TRANSACTION cluster-wide.
#
# This script seeds that exact state (a prepared transaction named
# cairn_test_prepared in a scratch database, which is what an interrupted run
# leaves) and then runs the real test. It exits non-zero while the test uses a
# single fixed name, and passes once the name is unique per run.
set -euo pipefail
cd "$(dirname "$0")/.."

if [ -z "${CAIRN_TEST_DATABASE_URL:-}" ]; then
  CAIRN_TEST_DATABASE_URL=$(scripts/test-postgres.sh)
fi
export CAIRN_TEST_DATABASE_URL
export CAIRN_REQUIRE_DATABASE=1

psql() { docker exec -i cairn-test-postgres psql -U cairn "$@"; }

if ! psql -d postgres -Atc 'SELECT 1' >/dev/null 2>&1; then
  echo "SKIP: this reproduction drives the docker fixture cairn-test-postgres"
  exit 0
fi

if [ "$(psql -d postgres -Atc 'SHOW max_prepared_transactions')" = "0" ]; then
  echo "SKIP: max_prepared_transactions is 0, so the test skips and cannot collide"
  exit 0
fi

seed=qa_prepared_gid_seed
cleanup() {
  psql -d "$seed" -Atc "ROLLBACK PREPARED 'cairn_test_prepared'" >/dev/null 2>&1 || true
  psql -d postgres -Atc "DROP DATABASE IF EXISTS $seed WITH (FORCE)" >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

psql -d postgres -Atc "CREATE DATABASE $seed" >/dev/null
psql -d "$seed" -Atc \
  "CREATE TABLE t(x int); BEGIN; INSERT INTO t VALUES (1); PREPARE TRANSACTION 'cairn_test_prepared'" >/dev/null

log=$(mktemp)
if go test -count=1 -run '^TestAnOlderPreparedTransactionIsNotSkipped$' ./ledger/ >"$log" 2>&1; then
  echo "PASS: a stale prepared transaction named cairn_test_prepared no longer breaks the test"
  exit 0
fi
cat "$log"
echo
if grep -q 'transaction identifier "cairn_test_prepared" is already in use' "$log"; then
  echo "FAIL: a prepared transaction left by an earlier run makes the test fail with 'already in use'"
  exit 1
fi
echo "FAIL: the test failed for a reason other than the fixed transaction identifier"
exit 1
