#!/usr/bin/env bash
# Checks that scripts/test-pgbouncer.sh prints a URL on the port the container
# it uses actually publishes, and that asking for one port does not remove the
# container another session is using on a different port.
set -euo pipefail
cd "$(dirname "$0")/.."

name=cairn-test-pgbouncer
custom_port=56073
custom_name=${name}-${custom_port}

cleanup() { docker rm -f "$name" "$custom_name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

# Start it once on a non-default port, then ask for the default.
CAIRN_TEST_PGBOUNCER_PORT=$custom_port scripts/test-pgbouncer.sh >/dev/null
url=$(scripts/test-pgbouncer.sh)

published=$(docker port "$name" 5432 | head -1)
published=${published##*:}

echo "printed:   $url"
echo "published: 127.0.0.1:${published}"
if [[ "$url" != *"@127.0.0.1:${published}/"* ]]; then
  echo "FAIL: the printed URL does not point at the port the container publishes" >&2
  exit 1
fi

running=$(docker inspect -f '{{.State.Running}}' "$custom_name" 2>/dev/null || true)
if [ "$running" != true ]; then
  echo "FAIL: asking for the default port removed the container on ${custom_port}" >&2
  exit 1
fi
echo "ok: the printed URL points at the container's published port,"
echo "    and the container on ${custom_port} is still running"
