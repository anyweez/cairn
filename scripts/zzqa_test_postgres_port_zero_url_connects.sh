#!/usr/bin/env bash
# QA: scripts/test-postgres.sh accepts CAIRN_TEST_PORT=0 and prints a URL on
# port 0, but Docker treats host port 0 as "choose a free port", so the
# container is published on some other port and the printed URL connects to
# nothing.
#
# The fixture's contract is that its output can be exported:
#
#   export CAIRN_TEST_DATABASE_URL=$(scripts/test-postgres.sh)
#
# Port 0 is a legal value for CAIRN_TEST_PORT (Docker's own "pick one"), so a
# caller who uses it to avoid a port conflict is handed a URL that fails every
# connection. The check below fails while the printed port is not the port the
# container publishes.
set -euo pipefail

cd "$(dirname "$0")/.."

name=cairn-test-postgres-0
cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

url=$(CAIRN_TEST_PORT=0 scripts/test-postgres.sh)
echo "fixture printed: $url"

printed=$(printf '%s' "$url" | sed -E 's#.*@127\.0\.0\.1:([0-9]+)/.*#\1#')
if [ -z "$printed" ]; then
  echo "could not read a port from the URL" >&2
  exit 2
fi

published=$(docker inspect -f \
  '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' "$name")
echo "container publishes: 127.0.0.1:${published}"

if timeout 3 bash -c "echo > /dev/tcp/127.0.0.1/$printed" 2>/dev/null; then
  echo "OK: the printed URL accepts connections on port $printed"
  exit 0
fi
echo "FAIL: nothing is listening on the printed port $printed; the container is on $published" >&2
exit 1
