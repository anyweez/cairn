#!/usr/bin/env bash
# Checks scripts/containers.sh reads the host port a container publishes back
# from the container, instead of trusting the port a script asked Docker for.
# A fixture started with host port 0 is published on a port Docker chooses, and
# printing the requested 0 names a port nothing listens on.
#
#   bash scripts/container-host-port_test.sh
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
. "$here/containers.sh"

name=cairn-test-container-host-port
# The image the Postgres fixture already uses, so this needs no pull of its own.
image=postgres:17-alpine
# A second mapping on a fixed port, published by the same container, so a fixed
# request is answered exactly as asked and not by Docker's choice.
fixed=58999

cleanup() { docker rm -f "$name" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

fail() { echo "FAIL: $*" >&2; exit 1; }

docker run -d --name "$name" -p "127.0.0.1:0:5432" \
  -p "127.0.0.1:${fixed}:5433" -e POSTGRES_PASSWORD=cairn \
  -e POSTGRES_USER=cairn "$image" >/dev/null ||
  fail "could not start $name"

# A container port published with host port 0 comes back on the port Docker
# chose, which is the mapping the container reports.
ephemeral=$(container_host_port "$name" 5432) ||
  fail "no host port reported for container port 5432"
case $ephemeral in
  '' | *[!0-9]*) fail "'$ephemeral' is not a port number" ;;
esac
[ "$ephemeral" -ne 0 ] || fail "reported the requested 0 as the published port"
mapping=$(docker port "$name" 5432 | head -1)
[ "$mapping" = "127.0.0.1:${ephemeral}" ] ||
  fail "reported $ephemeral, but the container publishes '$mapping'"

# And it is a port that connects: the point of reading it back is that a caller
# can use it.
up=
for _ in $(seq 1 60); do
  if timeout 1 bash -c "echo > /dev/tcp/127.0.0.1/${ephemeral}" 2>/dev/null; then
    up=1
    break
  fi
  sleep 0.5
done
[ -n "$up" ] || fail "nothing accepts a connection on 127.0.0.1:${ephemeral}"

# A fixed host port is reported exactly as asked: the helper reads the mapping,
# and the caller's fixed-port URL is unchanged.
[ "$(container_host_port "$name" 5433)" = "$fixed" ] ||
  fail "the fixed mapping on 5433 is not reported as $fixed"

# Nothing is printed and the call fails for a container port the container does
# not publish, and for a container that is not there.
err_file=$(mktemp)
out=$(container_host_port "$name" 5434 2>"$err_file") &&
  fail "reported '$out' for a container port it does not publish"
err=$(cat "$err_file")
rm -f "$err_file"
[ -z "$out" ] || fail "refused but printed '$out' on stdout"
case $err in
  *5434*) : ;;
  *) fail "the error does not name the container port: '$err'" ;;
esac
out=$(container_host_port "${name}-missing" 5432 2>/dev/null) &&
  fail "reported '$out' for a container that does not exist"
[ -z "$out" ] || fail "refused but printed '$out' on stdout"

echo "ok: the published host port is read back from the container"
