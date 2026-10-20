#!/usr/bin/env bash
# qa: scripts/test-postgres.sh reuses a running container without checking the
# port it was published on, so a run that changes CAIRN_TEST_PORT is handed a
# URL that does not connect.
#
# It starts the fixture on a non-default port, then runs it again with the
# default port. The second invocation reuses the first container and prints the
# default port, which nothing is listening on. The check at the end fails when
# the printed URL is unreachable, which is the defect.
set -euo pipefail

cd "$(dirname "$0")/.."

name=cairn-test-postgres
custom_port=55899
default_port=55871

restore() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  # Put the fixture back the way the run found it.
  scripts/test-postgres.sh >/dev/null 2>&1 || true
}
trap restore EXIT

docker rm -f "$name" >/dev/null 2>&1 || true
CAIRN_TEST_PORT=$custom_port scripts/test-postgres.sh >/dev/null

# Second run: the container is already running, so this reuses it.
url=$(scripts/test-postgres.sh)
echo "second run printed: $url"

printed_port=$(printf '%s' "$url" | sed -E 's#.*@127\.0\.0\.1:([0-9]+)/.*#\1#')
if [ -z "$printed_port" ]; then
  echo "could not read a port from the URL" >&2
  exit 2
fi

actual_port=$(docker inspect -f \
  '{{(index (index .NetworkSettings.Ports "5432/tcp") 0).HostPort}}' "$name")
echo "container is published on $actual_port, the printed URL says $printed_port"

# The URL the script printed is only useful if something answers on its port.
if timeout 3 bash -c "echo > /dev/tcp/127.0.0.1/$printed_port" 2>/dev/null; then
  echo "OK: the printed URL accepts connections"
  exit 0
fi
echo "FAIL: nothing is listening on the printed port $printed_port" >&2
exit 1
