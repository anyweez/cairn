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
. "$here/lib-test.sh"
require_docker "the container host-port test"

name=cairn-test-container-host-port
# The fixture container scripts/test-postgres.sh starts for CAIRN_TEST_PORT=0. It
# is not a documented port, so no other session's fixture is using it.
fixture=cairn-test-postgres-0
# The image the Postgres fixture already uses, so this needs no pull of its own.
image=postgres:17-alpine
# A second mapping on a fixed port, published by the same container, so a fixed
# request is answered exactly as asked and not by Docker's choice.
fixed=58999

cleanup() { docker rm -f "$name" "$fixture" >/dev/null 2>&1 || true; }
trap cleanup EXIT
cleanup

fail() { echo "FAIL: $*" >&2; exit 1; }

# assert_connects PORT WHAT: something accepts a TCP connection on 127.0.0.1:PORT.
assert_connects() {
  local port=$1 what=$2 _
  for _ in $(seq 1 60); do
    if timeout 1 bash -c "echo > /dev/tcp/127.0.0.1/${port}" 2>/dev/null; then
      return 0
    fi
    sleep 0.5
  done
  fail "nothing accepts a connection on 127.0.0.1:${port}, the port ${what}"
}

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
assert_connects "$ephemeral" "Docker chose for container port 5432"

# A fixed host port is reported exactly as asked: the helper reads the mapping,
# and the caller's fixed-port URL is unchanged.
[ "$(container_host_port "$name" 5433)" = "$fixed" ] ||
  fail "the fixed mapping on 5433 is not reported as $fixed"

# A requested host port of 0 is satisfied by any 127.0.0.1 mapping, because the
# request asked Docker to choose the port. Every non-zero port keeps matching
# exactly, so a container created for a different host port is not reused.
container_publishes "$name" 5432 0 ||
  fail "did not accept the ephemeral mapping for container port 5432"
container_publishes "$name" 5433 "$fixed" ||
  fail "did not accept the fixed mapping on 5433"
container_publishes "$name" 5433 "$ephemeral" &&
  fail "accepted $ephemeral for container port 5433, which it does not publish"
container_publishes "${name}-missing" 5432 0 &&
  fail "accepted a container that does not exist"

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

# A port-zero fixture is reused rather than replaced. The reuse check compared
# the requested 0 with the mapping Docker reports, which never matched, so every
# call removed and recreated the container. test-pgbouncer.sh and
# bench-postgres.sh run test-postgres.sh themselves, so a caller who exported
# the first URL lost the database behind it when the second fixture started.
first=$(CAIRN_TEST_PORT=0 "$here/test-postgres.sh") ||
  fail "test-postgres.sh with CAIRN_TEST_PORT=0 failed"
first_id=$(docker inspect -f '{{.Id}}' "$fixture") ||
  fail "$fixture was not created"
second=$(CAIRN_TEST_PORT=0 "$here/test-postgres.sh") ||
  fail "the second test-postgres.sh with CAIRN_TEST_PORT=0 failed"
[ "$second" = "$first" ] ||
  fail "the second run printed '$second', not the first URL '$first'"
[ "$(docker inspect -f '{{.Id}}' "$fixture")" = "$first_id" ] ||
  fail "the second run replaced $fixture instead of reusing it"
published=${first#*@127.0.0.1:}
published=${published%%/*}
assert_connects "$published" "the second port-zero run reported"
docker rm -f "$fixture" >/dev/null

echo "ok: the published host port is read back from the container"
