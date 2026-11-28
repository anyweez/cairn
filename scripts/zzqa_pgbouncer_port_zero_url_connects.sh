#!/usr/bin/env bash
# scripts/test-pgbouncer.sh accepts CAIRN_TEST_PGBOUNCER_PORT=0. Docker treats
# host port 0 as "publish on a free port", so the container ends up on an
# ephemeral port while the script prints the requested 0. A caller following
# the documented use gets a URL nothing listens on.
#
# Passes once the printed URL names the port the container actually publishes
# (or the script refuses port 0 with a clear error instead of printing an
# unusable URL).
set -euo pipefail

cd "$(dirname "$0")/.."
. scripts/lib-test.sh
require_docker "the PgBouncer port-0 test"

name=cairn-test-pgbouncer-0
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

url=$(CAIRN_TEST_PGBOUNCER_PORT=0 scripts/test-pgbouncer.sh)
echo "printed: $url"

printed_port=$(printf '%s' "$url" | sed -E 's#.*@127\.0\.0\.1:([0-9]+)/.*#\1#')
if [ -z "$printed_port" ]; then
  echo "FAIL: could not read a port from the printed URL" >&2
  exit 1
fi

mapping=$(docker port "$name" 5432 | head -1)
actual_port=${mapping##*:}
echo "container publishes: 127.0.0.1:${actual_port}"

if [ "$printed_port" != "$actual_port" ]; then
  echo "FAIL: the printed URL says port ${printed_port}, but the container is on ${actual_port}" >&2
  exit 1
fi
if ! timeout 3 bash -c "echo > /dev/tcp/127.0.0.1/${printed_port}" 2>/dev/null; then
  echo "FAIL: nothing is listening on the printed port ${printed_port}" >&2
  exit 1
fi
echo "ok: the printed URL names the port the container publishes"
