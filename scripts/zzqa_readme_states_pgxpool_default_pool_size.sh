#!/usr/bin/env bash
# README.md says pgxpool's default pool is 4 connections. pgxpool's own
# documentation says the default is the greater of 4 and runtime.NumCPU(), and
# docs/PERFORMANCE.md says the same. On any host with more than four CPUs the
# README's figure is wrong, so a reader who trusts it leaves throughput on the
# table or sizes a deployment from a number the library does not use.
#
# This fails while README.md states the default is a fixed 4, and passes once it
# names the same rule as docs/PERFORMANCE.md and the package.
set -euo pipefail
cd "$(dirname "$0")/.."

if ! grep -q "pgxpool's default of 4 connections" README.md; then
  echo "ok: README.md does not state a fixed pgxpool default of 4"
  exit 0
fi

if go doc github.com/jackc/pgx/v5/pgxpool.ParseConfig | grep -q 'greater of 4 or'; then
  echo "FAIL: README.md says pgxpool's default pool is 4 connections" >&2
  echo "      pgxpool says the default is the greater of 4 or runtime.NumCPU():" >&2
  go doc github.com/jackc/pgx/v5/pgxpool.ParseConfig | grep 'pool_max_conns' >&2
  exit 1
fi

echo "ok: the installed pgxpool does not contradict README.md"
