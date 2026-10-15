#!/usr/bin/env bash
# scripts/test-postgres.sh says it starts or reuses the test Postgres, but the
# reuse check and the creation are not atomic. Two invocations that arrive
# together both see no container and both `docker run --name
# cairn-test-postgres`; Docker serializes the name and the loser exits non-zero
# with a name conflict instead of reusing the winner's container.
#
# This reproduces that: remove the container, start two copies at once, and
# fail if either did not get a URL. The fixture is left running afterwards.
set -u

cd "$(dirname "$0")/.."
name=cairn-test-postgres
docker rm -f "$name" >/dev/null 2>&1 || true

scripts/test-postgres.sh >/dev/null 2>&1 & a=$!
scripts/test-postgres.sh >/dev/null 2>&1 & b=$!
wait "$a"; ra=$?
wait "$b"; rb=$?

# Leave the fixture running for whoever comes next, whichever caller won.
scripts/test-postgres.sh >/dev/null 2>&1 || true

if [ "$ra" -ne 0 ] || [ "$rb" -ne 0 ]; then
  echo "concurrent invocations of scripts/test-postgres.sh: one failed (a=$ra b=$rb)" >&2
  exit 1
fi
