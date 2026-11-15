#!/usr/bin/env bash
# TestTheGlobalLogOrdersTransactionIDsAsNumbers guards the ledger's promise
# that the global log orders transaction IDs as numbers, across a change in
# their number of digits. It skips whenever the next power of ten is more than
# 2,000,000 transactions away (ledger/ledger_test.go), and a run that passes
# leaves the cluster's counter *at* that power of ten, so the run after a
# passing one is guaranteed to skip: 9 * 10^6 is more than the budget. A used
# Postgres therefore reports success without running the test, and
# scripts/check.sh reports the gate as green.
#
# The gate already treats a fence test that cannot run as a failure: it sets
# CAIRN_REQUIRE_DATABASE=1 so the prepared-transaction test fails rather than
# skips (ledger/projection_foreign_tx_test.go). This fence ignores that.
#
# This drives the counter the way ordinary repeated use does — one run per
# decade, which is what the test does to itself — and fails when a run exits 0
# having skipped. It passes once a skipped fence makes the run fail, wherever
# that guard is put.
#
#   bash scripts/zzqa_xid_order_fence_does_not_skip.sh
set -uo pipefail

cd "$(dirname "$0")/.."

if [ -z "${CAIRN_TEST_DATABASE_URL:-}" ]; then
  CAIRN_TEST_DATABASE_URL=$(scripts/test-postgres.sh)
fi
export CAIRN_TEST_DATABASE_URL
# The gate's setting: a fence that cannot run must not report success.
export CAIRN_REQUIRE_DATABASE=1

test_name=TestTheGlobalLogOrdersTransactionIDsAsNumbers
# From a fresh cluster each run crosses one decade; reaching the first decade
# where the skip applies takes at most seven. Anything already past it skips
# on its first run.
for attempt in $(seq 1 10); do
  out=$(go test ./ledger -run "^${test_name}\$" -count=1 -v 2>&1)
  status=$?
  if grep -q -- "--- SKIP: ${test_name}" <<<"$out"; then
    echo "FAIL: run ${attempt} exited ${status} and skipped ${test_name}, so the gate"
    echo "      reports success without checking that the log orders transaction IDs"
    echo "      as numbers. The run below is the whole reason:"
    sed -n '/^=== RUN/,$p' <<<"$out"
    exit 1
  fi
  if grep -q -- "--- FAIL: ${test_name}" <<<"$out"; then
    echo "PASS: run ${attempt} refused to skip ${test_name}; it failed loudly instead"
    exit 0
  fi
  if ! grep -q -- "--- PASS: ${test_name}" <<<"$out"; then
    echo "FAIL: run ${attempt} neither passed, failed nor skipped ${test_name}:" >&2
    echo "$out" >&2
    exit 1
  fi
done

echo "PASS: ${test_name} ran and passed on each of the last 10 runs"
