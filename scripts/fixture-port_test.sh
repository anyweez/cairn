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

# assert_port WHAT PORT
assert_port() {
  case $2 in
    '' | *[!0-9]*) fail "$1: '$2' is not a port number" ;;
  esac
  [ "$2" -ge 1 ] && [ "$2" -le 65535 ] || fail "$1: $2 is outside 1-65535"
}

# assert_refuses WHAT COMMAND...: the helper must refuse, printing nothing on
# stdout and the reason on stderr.
assert_refuses() {
  local what=$1 err_file err out status=0
  shift
  err_file=$(mktemp)
  out=$("$@" 2>"$err_file") || status=$?
  err=$(cat "$err_file")
  rm -f "$err_file"
  [ "$status" -ne 0 ] || fail "$what: accepted it and printed '$out'"
  [ -z "$out" ] || fail "$what: refused but printed '$out' on stdout"
  case $err in
    *1-65535* | *'is not a port number'*) : ;;
    *) fail "$what: the error does not say what is wrong: '$err'" ;;
  esac
}

# companion_ports PG_PORT [TOXIPROXY_PORT] prints "pgbouncer proxy api", the
# ports test-pgbouncer.sh and bench-postgres.sh derive for that CAIRN_TEST_PORT.
companion_ports() {
  local pg=$1 pgb proxy
  pgb=$(fixture_port 55873 55871 "$pg") || return 1
  proxy=$(fixture_port 55872 55871 "$pg" "${2:-}") || return 1
  printf '%s %s %s' "$pgb" "$proxy" \
    "$(fixture_port_beside 8474 55872 "$proxy" '' "$pgb")"
}

# The default Postgres port keeps every documented host port and container name.
assert_eq 55873 "$(fixture_port 55873 55871 55871)" "default PgBouncer port"
assert_eq 55872 "$(fixture_port 55872 55871 55871)" "default toxiproxy proxy port"
# The API publishes beside the proxy, not at its own distance from the Postgres
# port, so the default pair is the documented 55872 and 8474.
assert_eq 8474 "$(fixture_port_beside 8474 55872 55872 '' 55873)" \
  "default toxiproxy API port"
assert_eq cairn-test-pgbouncer "$(fixture_name cairn-test-pgbouncer 55873 55873)" \
  "default PgBouncer container name"
assert_eq cairn-toxiproxy "$(fixture_name cairn-toxiproxy 55872 55872)" \
  "default toxiproxy container name"

# Any other Postgres port gets host ports of its own, none of them a default,
# and the toxiproxy's two ports stay apart: the API steps over the port
# PgBouncer publishes for the same Postgres, since one session may run both.
pg_port=56099
read -r pgb_port proxy_port api_port <<<"$(companion_ports "$pg_port")"
assert_eq 56101 "$pgb_port" "PgBouncer port for CAIRN_TEST_PORT=$pg_port"
assert_eq 56100 "$proxy_port" "toxiproxy proxy port for CAIRN_TEST_PORT=$pg_port"
assert_eq 56102 "$api_port" "toxiproxy API port for CAIRN_TEST_PORT=$pg_port"

# Every key keeps its companions inside 1-65535 and apart from each other: a
# Postgres port below the default (5432) once put the API port at -41965, and a
# companion port cannot be published twice.
for pg_port in 55871 56099 5432 15432 65532; do
  read -r pgb_port proxy_port api_port <<<"$(companion_ports "$pg_port")"
  for port in "$pgb_port" "$proxy_port" "$api_port"; do
    assert_port "CAIRN_TEST_PORT=$pg_port" "$port"
  done
  [ "$pgb_port" != "$proxy_port" ] && [ "$proxy_port" != "$api_port" ] &&
    [ "$pgb_port" != "$api_port" ] ||
    fail "CAIRN_TEST_PORT=$pg_port shares a host port between companions:" \
      "pgbouncer $pgb_port, proxy $proxy_port, api $api_port"
  if [ "$pg_port" != 55871 ]; then
    for port in "$pgb_port" "$proxy_port" "$api_port"; do
      case $port in
        55871 | 55872 | 55873 | 8474)
          fail "CAIRN_TEST_PORT=$pg_port still asks for the default host port $port"
          ;;
      esac
    done
  fi
done

# An explicit proxy port still moves the API port with it (a42a101): the low
# end of the range is a port like any other, not a negative one.
assert_eq 15432 "$(fixture_port 55872 55871 55871 15432)" \
  "explicit CAIRN_TOXIPROXY_PORT"
assert_eq 15433 "$(fixture_port_beside 8474 55872 15432 '' 55873)" \
  "API port for an explicit CAIRN_TOXIPROXY_PORT=15432"

# The companion's name is derived from the port it publishes, so a second
# Postgres port gets a different name as well as a different port: two
# sessions cannot ask Docker for the same host port under different names.
other_pgb_port=$(fixture_port 55873 55871 56199)
other_pgb_name=$(fixture_name cairn-test-pgbouncer 55873 "$other_pgb_port")
pgb_name=$(fixture_name cairn-test-pgbouncer 55873 56101)
[ "$other_pgb_port" != 56101 ] ||
  fail "two CAIRN_TEST_PORT values publish the PgBouncer on 56101"
[ "$other_pgb_name" != "$pgb_name" ] ||
  fail "two CAIRN_TEST_PORT values share the PgBouncer container name"

# An explicit port still wins, on either fixture and on the default Postgres
# port as well, and decides the container name with it: a session that asks for
# a different port gets a container of its own whatever CAIRN_TEST_PORT is.
assert_eq 56073 "$(fixture_port 55873 55871 56099 56073)" \
  "explicit CAIRN_TEST_PGBOUNCER_PORT"
assert_eq 56073 "$(fixture_port 55873 55871 55871 56073)" \
  "explicit CAIRN_TEST_PGBOUNCER_PORT on the default Postgres port"
assert_eq cairn-test-pgbouncer-56073 "$(fixture_name cairn-test-pgbouncer 55873 56073)" \
  "container name for an explicit CAIRN_TEST_PGBOUNCER_PORT"
assert_eq 9000 "$(fixture_port_beside 8474 55872 56100 9000 '' 56101)" \
  "explicit CAIRN_TOXIPROXY_API_PORT"

# A port that leaves 1-65535 is refused with the reason, not handed to Docker,
# which only says "invalid port range" once a container is being created.
assert_refuses "PgBouncer port for CAIRN_TEST_PORT=65534" \
  fixture_port 55873 55871 65534
assert_refuses "API port beside CAIRN_TEST_PORT=65533's proxy" \
  fixture_port_beside 8474 55872 65534 '' 65535
assert_refuses "API port beside proxy port 65535" \
  fixture_port_beside 8474 55872 65535
assert_refuses "explicit CAIRN_TEST_PGBOUNCER_PORT=70000" \
  fixture_port 55873 55871 55871 70000
assert_refuses "explicit CAIRN_TOXIPROXY_API_PORT=70000" \
  fixture_port_beside 8474 55872 55872 70000
assert_refuses "CAIRN_TEST_PORT that is not a number" \
  fixture_port 55873 55871 pgbouncer

echo "ok: companion host ports are keyed by the fixture port, in range, and the defaults are unchanged"
