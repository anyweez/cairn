#!/usr/bin/env bash
# qa: a fixture run with a custom CAIRN_TEST_PORT leaves a companion container
# (PgBouncer or toxiproxy) behind. A later run with the default CAIRN_TEST_PORT
# picks a different container name, so it used to ask Docker for the same
# default host port and fail with "port is already allocated".
#
# The per-port container names (a3702fd) keep two Postgres fixtures apart, and
# the companion host ports now follow the Postgres fixture port, so the custom
# session's PgBouncer publishes a port of its own instead of 55873 and its
# toxiproxy instead of 8474/55872. A default run works while they are still
# around, and leaves them alone.
#
#   bash scripts/zzqa_fixture_custom_port_does_not_block_default_port.sh
set -euo pipefail
cd "$(dirname "$0")/.."

source scripts/containers.sh

custom_pg=56099
postgres_default=cairn-test-postgres
postgres_custom=cairn-test-postgres-$custom_pg
pgb_default=cairn-test-pgbouncer
# The custom session's companions publish host ports keyed by the Postgres port
# they front, and carry those ports in their names.
pgb_custom_port=$(fixture_port 55873 55871 "$custom_pg")
toxi_custom_port=$(fixture_port 55872 55871 "$custom_pg")

# custom_name BASE DEFAULT_PORT PORT prints the name a fixture script gives the
# companion in front of the custom Postgres.
custom_name() { printf '%s-pg-%s' "$(fixture_name "$1" "$2" "$3")" "$custom_pg"; }

pgb_custom=$(custom_name cairn-test-pgbouncer 55873 "$pgb_custom_port")
toxi_default=cairn-toxiproxy
toxi_custom=$(custom_name cairn-toxiproxy 55872 "$toxi_custom_port")

cleanup() {
  docker rm -f "$pgb_default" "$pgb_custom" "$toxi_default" "$toxi_custom" \
    "$postgres_custom" >/dev/null 2>&1 || true
  scripts/test-postgres.sh >/dev/null 2>&1 || true
}
trap cleanup EXIT
cleanup

# PgBouncer: the custom-port session publishes a host port of its own.
CAIRN_TEST_PORT=$custom_pg scripts/test-pgbouncer.sh >/dev/null
if ! out=$(scripts/test-pgbouncer.sh 2>&1); then
  printf '%s\n' "$out" >&2
  echo "FAIL: a default-port run cannot start its PgBouncer while the" >&2
  echo "      custom-port run's PgBouncer ${pgb_custom} is still running" >&2
  exit 1
fi
# The fix must not be to remove the other session's container.
published=$(docker port "$pgb_custom" 5432 2>/dev/null | head -1)
if [ "$published" != "127.0.0.1:${pgb_custom_port}" ]; then
  echo "FAIL: the default-port run left the custom-port PgBouncer ${pgb_custom}" >&2
  echo "      publishing '${published}', not 127.0.0.1:${pgb_custom_port}" >&2
  exit 1
fi
docker rm -f "$pgb_default" "$pgb_custom" >/dev/null 2>&1 || true

# The benchmark fixture has the same shape: toxiproxy's API and proxy ports
# follow the Postgres fixture port too.
CAIRN_TEST_PORT=$custom_pg scripts/bench-postgres.sh >/dev/null
if ! out=$(scripts/bench-postgres.sh 2>&1); then
  printf '%s\n' "$out" >&2
  echo "FAIL: a default-port run cannot start its toxiproxy while the" >&2
  echo "      custom-port run's toxiproxy ${toxi_custom} is still running" >&2
  exit 1
fi
published=$(docker port "$toxi_custom" "$toxi_custom_port" 2>/dev/null | head -1)
if [ "$published" != "127.0.0.1:${toxi_custom_port}" ]; then
  echo "FAIL: the default-port run left the custom-port toxiproxy ${toxi_custom}" >&2
  echo "      publishing '${published}', not 127.0.0.1:${toxi_custom_port}" >&2
  exit 1
fi

echo "ok: a custom CAIRN_TEST_PORT does not block a later default-port run"
