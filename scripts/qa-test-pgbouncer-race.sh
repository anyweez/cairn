#!/usr/bin/env bash
# Reproduction for the check-then-act race in scripts/test-pgbouncer.sh.
#
# The script says it "starts (or reuses)" its PgBouncer, but the reuse check
# and the creation are not atomic:
#
#   if ! docker ps --format '{{.Names}}' | grep -qx "$name"; then
#     docker rm -f "$name" ...
#     docker run -d --name "$name" ...
#   fi
#
# Two invocations that start together both see no container, both remove it,
# and both `docker run --name cairn-test-pgbouncer`. Docker serializes the
# name, so one caller wins and the other exits non-zero. The loser's
# `docker rm -f` can also land after the winner's `docker run`, deleting the
# winner's container.
#
# This drives two concurrent invocations per round and requires both to print
# a URL and exit 0. It fails while the race is present.
set -uo pipefail

cd "$(dirname "$0")/.."

name=cairn-test-pgbouncer
rounds=5

fail() { echo "FAIL: $*" >&2; exit 1; }

for i in $(seq 1 "$rounds"); do
  docker rm -f "$name" >/dev/null 2>&1 || true

  scripts/test-pgbouncer.sh >/tmp/qa-pgbouncer-a.out 2>/tmp/qa-pgbouncer-a.err &
  a=$!
  scripts/test-pgbouncer.sh >/tmp/qa-pgbouncer-b.out 2>/tmp/qa-pgbouncer-b.err &
  b=$!
  wait "$a"; ca=$?
  wait "$b"; cb=$?

  if [ "$ca" -ne 0 ] || [ "$cb" -ne 0 ]; then
    echo "round $i: invocations exited $ca and $cb" >&2
    echo "--- invocation A stderr ---" >&2
    cat /tmp/qa-pgbouncer-a.err >&2
    echo "--- invocation B stderr ---" >&2
    cat /tmp/qa-pgbouncer-b.err >&2
    fail "running scripts/test-pgbouncer.sh twice at once fails one caller"
  fi
done

echo "both concurrent invocations of scripts/test-pgbouncer.sh succeeded in every round"
