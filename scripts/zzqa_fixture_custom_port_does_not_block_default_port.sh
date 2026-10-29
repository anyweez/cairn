#!/usr/bin/env bash
# qa: a fixture run with a custom CAIRN_TEST_PORT leaves a companion container
# (PgBouncer or toxiproxy) publishing the default host port. A later run with
# the default CAIRN_TEST_PORT picks a different container name but asks Docker
# for that same host port, so it fails with "port is already allocated".
#
# The per-port container names (a3702fd) keep two Postgres fixtures apart, but
# the companion host ports stay at their defaults, so the custom-port session's
# PgBouncer holds 55873 and its toxiproxy holds 8474/55872. The next default
# run cannot bind them, even though the custom session is idle.
#
# Passes once a default run works while a custom-port run's companion
# containers are still around (whether by choosing a free host port, reusing
# the one that already fronts the requested Postgres, or refusing early with a
# clear explanation).
set -euo pipefail
cd "$(dirname "$0")/.."

custom_pg=56099
postgres_default=cairn-test-postgres
postgres_custom=cairn-test-postgres-$custom_pg
pgb_default=cairn-test-pgbouncer
pgb_custom=cairn-test-pgbouncer-pg-$custom_pg
toxi_default=cairn-toxiproxy
toxi_custom=cairn-toxiproxy-pg-$custom_pg

cleanup() {
  docker rm -f "$pgb_default" "$pgb_custom" "$toxi_default" "$toxi_custom" \
    "$postgres_custom" >/dev/null 2>&1 || true
  scripts/test-postgres.sh >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

# PgBouncer: the custom-port session starts it on the default PgBouncer port.
CAIRN_TEST_PORT=$custom_pg scripts/test-pgbouncer.sh >/dev/null
if ! out=$(scripts/test-pgbouncer.sh 2>&1); then
  printf '%s\n' "$out" >&2
  echo "FAIL: a default-port run cannot start its PgBouncer while the" >&2
  echo "      custom-port run's PgBouncer still publishes 127.0.0.1:55873" >&2
  exit 1
fi
docker rm -f "$pgb_default" "$pgb_custom" >/dev/null 2>&1 || true

# The benchmark fixture has the same shape: toxiproxy's API and proxy ports do
# not move with CAIRN_TEST_PORT.
CAIRN_TEST_PORT=$custom_pg scripts/bench-postgres.sh >/dev/null
if ! out=$(scripts/bench-postgres.sh 2>&1); then
  printf '%s\n' "$out" >&2
  echo "FAIL: a default-port run cannot start its toxiproxy while the" >&2
  echo "      custom-port run's toxiproxy still publishes 127.0.0.1:8474/55872" >&2
  exit 1
fi

echo "ok: a custom CAIRN_TEST_PORT does not block a later default-port run"
