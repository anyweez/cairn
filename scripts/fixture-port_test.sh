#!/usr/bin/env bash
# Checks scripts/containers.sh keys a companion fixture's host port the same way
# it keys the container's name. test-pgbouncer.sh and bench-postgres.sh named
# their container after the Postgres fixture port but published a fixed host
# port, so a session on a custom CAIRN_TEST_PORT left a differently named
# container holding the default port and the next default session failed with
# "port is already allocated".
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
. "$here/containers.sh"

fail() { echo "FAIL: $*" >&2; exit 1; }

# assert_eq WANT GOT WHAT
assert_eq() { [ "$1" = "$2" ] || fail "$3: want '$1', got '$2'"; }

# The default Postgres port keeps every documented host port and container name.
assert_eq 55873 "$(fixture_port 55873 55871 55871)" "default PgBouncer port"
assert_eq 55872 "$(fixture_port 55872 55871 55871)" "default toxiproxy proxy port"
assert_eq 8474 "$(fixture_port 8474 55872 55872)" "default toxiproxy API port"
assert_eq cairn-test-pgbouncer "$(fixture_name cairn-test-pgbouncer 55873 55873)" \
  "default PgBouncer container name"
assert_eq cairn-toxiproxy "$(fixture_name cairn-toxiproxy 55872 55872)" \
  "default toxiproxy container name"

# Any other Postgres port gets host ports of its own, none of them a default.
pg_port=56099
pgb_port=$(fixture_port 55873 55871 "$pg_port")
proxy_port=$(fixture_port 55872 55871 "$pg_port")
api_port=$(fixture_port 8474 55872 "$proxy_port")
assert_eq 56101 "$pgb_port" "PgBouncer port for CAIRN_TEST_PORT=$pg_port"
assert_eq 56100 "$proxy_port" "toxiproxy proxy port for CAIRN_TEST_PORT=$pg_port"
assert_eq 8702 "$api_port" "toxiproxy API port behind proxy port $proxy_port"
for port in "$pgb_port" "$proxy_port" "$api_port"; do
  case $port in
    55871 | 55872 | 55873 | 8474)
      fail "CAIRN_TEST_PORT=$pg_port still asks for the default host port $port"
      ;;
  esac
done
[ "$pgb_port" != "$proxy_port" ] && [ "$proxy_port" != "$api_port" ] &&
  [ "$pgb_port" != "$api_port" ] || fail "CAIRN_TEST_PORT=$pg_port shares a host port between companions"

# The companion's name is derived from the port it publishes, so a second
# Postgres port gets a different name as well as a different port: two
# sessions cannot ask Docker for the same host port under different names.
other_pg=56199
other_pgb_port=$(fixture_port 55873 55871 "$other_pg")
other_pgb_name=$(fixture_name cairn-test-pgbouncer 55873 "$other_pgb_port")
pgb_name=$(fixture_name cairn-test-pgbouncer 55873 "$pgb_port")
[ "$other_pgb_port" != "$pgb_port" ] ||
  fail "two CAIRN_TEST_PORT values publish the PgBouncer on $pgb_port"
[ "$other_pgb_name" != "$pgb_name" ] ||
  fail "two CAIRN_TEST_PORT values share the PgBouncer container name"

# An explicit port still wins, on either fixture and on the default Postgres
# port as well, and decides the container name with it: a session that asks for
# a different port gets a container of its own whatever CAIRN_TEST_PORT is.
assert_eq 56073 "$(fixture_port 55873 55871 "$pg_port" 56073)" \
  "explicit CAIRN_TEST_PGBOUNCER_PORT"
assert_eq 56073 "$(fixture_port 55873 55871 55871 56073)" \
  "explicit CAIRN_TEST_PGBOUNCER_PORT on the default Postgres port"
assert_eq cairn-test-pgbouncer-56073 "$(fixture_name cairn-test-pgbouncer 55873 56073)" \
  "container name for an explicit CAIRN_TEST_PGBOUNCER_PORT"
assert_eq 9000 "$(fixture_port 8474 55872 "$proxy_port" 9000)" \
  "explicit CAIRN_TOXIPROXY_API_PORT"

echo "ok: companion host ports are keyed by the fixture port, and the defaults are unchanged"
