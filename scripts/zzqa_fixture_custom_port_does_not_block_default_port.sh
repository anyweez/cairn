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

# check_pgbouncer WHAT URL PORT: the URL points at PORT and something accepts a
# connection there, so a run that prints an address nothing listens on fails.
check_pgbouncer() {
  local what=$1 url=$2 port=$3
  case $url in
    *"@127.0.0.1:${port}/"*) : ;;
    *)
      echo "FAIL: the ${what} run printed '${url}', not port ${port}" >&2
      exit 1
      ;;
  esac
  if ! (exec 3<>"/dev/tcp/127.0.0.1/${port}") 2>/dev/null; then
    echo "FAIL: nothing accepts a connection on 127.0.0.1:${port}, which the" >&2
    echo "      ${what} run printed" >&2
    exit 1
  fi
}

# bench_address OUT NAME prints the value bench-postgres.sh exported for NAME.
bench_address() { sed -n "s/^export ${2}='\(.*\)'\$/\1/p" <<<"$1"; }

# check_toxiproxy WHAT OUT PROXY_PORT: the printed addresses point at a
# toxiproxy this run started, listening on the requested proxy port.
check_toxiproxy() {
  local what=$1 out=$2 want_proxy=$3 api api_port proxy
  api=$(bench_address "$out" CAIRN_BENCH_TOXIPROXY)
  proxy=$(bench_address "$out" CAIRN_BENCH_PROXY_ADDR)
  api_port=$(sed -E 's#^http://127\.0\.0\.1:([0-9]+)/.*#\1#' <<<"$api")
  if [ "$proxy" != "127.0.0.1:${want_proxy}" ]; then
    echo "FAIL: the ${what} run printed CAIRN_BENCH_PROXY_ADDR='${proxy}'," >&2
    echo "      not 127.0.0.1:${want_proxy}" >&2
    exit 1
  fi
  if ! curl -sf "http://127.0.0.1:${api_port}/version" >/dev/null; then
    echo "FAIL: the ${what} run printed CAIRN_BENCH_TOXIPROXY='${api}'," >&2
    echo "      which does not answer" >&2
    exit 1
  fi
  if ! (exec 3<>"/dev/tcp/${proxy%:*}/${proxy#*:}") 2>/dev/null; then
    echo "FAIL: the ${what} run printed CAIRN_BENCH_PROXY_ADDR='${proxy}'," >&2
    echo "      which nothing listens on" >&2
    exit 1
  fi
}

# PgBouncer: the custom-port session publishes a host port of its own.
custom_url=$(CAIRN_TEST_PORT=$custom_pg scripts/test-pgbouncer.sh)
check_pgbouncer "custom-port" "$custom_url" "$pgb_custom_port"
if ! out=$(scripts/test-pgbouncer.sh 2>&1); then
  printf '%s\n' "$out" >&2
  echo "FAIL: a default-port run cannot start its PgBouncer while the" >&2
  echo "      custom-port run's PgBouncer ${pgb_custom} is still running" >&2
  exit 1
fi
check_pgbouncer "default-port" "$out" 55873
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
custom_out=$(CAIRN_TEST_PORT=$custom_pg scripts/bench-postgres.sh)
check_toxiproxy "custom-port" "$custom_out" "$toxi_custom_port"
if ! out=$(scripts/bench-postgres.sh 2>&1); then
  printf '%s\n' "$out" >&2
  echo "FAIL: a default-port run cannot start its toxiproxy while the" >&2
  echo "      custom-port run's toxiproxy ${toxi_custom} is still running" >&2
  exit 1
fi
check_toxiproxy "default-port" "$out" 55872
published=$(docker port "$toxi_custom" "$toxi_custom_port" 2>/dev/null | head -1)
if [ "$published" != "127.0.0.1:${toxi_custom_port}" ]; then
  echo "FAIL: the default-port run left the custom-port toxiproxy ${toxi_custom}" >&2
  echo "      publishing '${published}', not 127.0.0.1:${toxi_custom_port}" >&2
  exit 1
fi

echo "ok: a custom CAIRN_TEST_PORT does not block a later default-port run"
