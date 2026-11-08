#!/usr/bin/env bash
# Starts (or reuses) the test Postgres and a toxiproxy in front of it, and
# prints the environment the ledger benchmarks read. The benchmarks reach
# Postgres through the proxy and set its latency themselves.
#
#   eval "$(scripts/bench-postgres.sh)"
#   go test ./ledger -run '^$' -bench Handle -benchtime 5s
set -euo pipefail

source "$(dirname "$0")/containers.sh"

cd "$(dirname "$0")/.."
. scripts/lib-lock.sh
direct=$(scripts/test-postgres.sh)

network=cairn-bench
pg_port=${CAIRN_TEST_PORT:-55871}
postgres=$(fixture_name cairn-test-postgres 55871 "$pg_port")
api_port=${CAIRN_TOXIPROXY_API_PORT:-8474}
proxy_port=${CAIRN_TOXIPROXY_PORT:-55872}
proxy=$(fixture_name cairn-toxiproxy 55872 "$proxy_port")
# The proxy's upstream is fixed when the container is created, so a toxiproxy
# in front of a different Postgres must be a different container.
[ "$pg_port" = 55871 ] || proxy="${proxy}-pg-${pg_port}"

# Creating the network and the proxy is a check-then-act: callers that start
# together both see neither, and one caller's `docker rm -f` can delete the
# container another just started. Serialise them, so the loser reuses the
# winner's network and container instead of colliding with it on the name.
lock_acquire cairn-bench-postgres
docker network inspect "$network" >/dev/null 2>&1 || docker network create "$network" >/dev/null
docker network connect "$network" "$postgres" 2>/dev/null || true

if ! container_publishes "$proxy" 8474 "$api_port" ||
   ! container_publishes "$proxy" "$proxy_port" "$proxy_port"; then
  docker rm -f "$proxy" >/dev/null 2>&1 || true
  docker run -d --name "$proxy" --network "$network" \
    -p "127.0.0.1:${api_port}:8474" -p "127.0.0.1:${proxy_port}:${proxy_port}" \
    ghcr.io/shopify/toxiproxy:2.9.0 >/dev/null
fi
lock_release
api="http://127.0.0.1:${api_port}"
for _ in $(seq 1 40); do
  curl -sf "$api/version" >/dev/null 2>&1 && up=1 && break
  sleep 0.25
done
if [ -z "${up:-}" ]; then
  echo "toxiproxy in $proxy did not come up" >&2
  exit 1
fi

# Create the proxy unless it exists already, under the same lock so two
# callers cannot both see it missing and race the POST.
lock_acquire cairn-bench-postgres
if ! curl -sf "$api/proxies/cairn-pg" >/dev/null; then
  curl -sf -X POST "$api/proxies" -d "{\"name\":\"cairn-pg\",\"listen\":\"0.0.0.0:${proxy_port}\",\"upstream\":\"${postgres}:5432\",\"enabled\":true}" >/dev/null
fi
lock_release

echo "export CAIRN_TEST_DATABASE_URL='${direct}'"
echo "export CAIRN_BENCH_PROXY_ADDR='127.0.0.1:${proxy_port}'"
echo "export CAIRN_BENCH_TOXIPROXY='${api}/proxies/cairn-pg'"
