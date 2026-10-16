#!/usr/bin/env bash
# A reproduction: scripts/test-pgbouncer.sh reuses a running container by name
# and then prints the port it would have published, not the one the container
# actually publishes. A caller who exports the printed URL reaches nothing.
#
# Fails today; passes once the script either restarts the container to match
# the requested port or prints the port the container is really on.
set -euo pipefail
cd "$(dirname "$0")/.."

name=cairn-test-pgbouncer
custom_port=56073

cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
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
echo "ok: the printed URL points at the container's published port"
