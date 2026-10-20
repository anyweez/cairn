#!/usr/bin/env bash
# Starts (or reuses) a throwaway Postgres for the ledger tests and prints the
# URL to export. Each test creates and drops its own database inside it.
#
#   export CAIRN_TEST_DATABASE_URL=$(scripts/test-postgres.sh)
set -euo pipefail

. "$(dirname "$0")/lib-lock.sh"

name=cairn-test-postgres
port=${CAIRN_TEST_PORT:-55871}

# The check and the creation are not atomic on their own: callers that start
# together both see no container, and one caller's `docker rm -f` can delete the
# container another just started. Serialise them, so the loser finds the
# winner's container instead of colliding with it on the name.
lock_acquire cairn-test-postgres
if ! docker ps --format '{{.Names}}' | grep -qx "$name"; then
  docker rm -f "$name" >/dev/null 2>&1 || true
  # max_prepared_transactions defaults to 0, which makes PREPARE TRANSACTION
  # fail and the ledger's prepared-transaction test skip. The LEAST fence that
  # test proves only shows itself when a prepared transaction can be in flight.
  docker run -d --name "$name" -p "127.0.0.1:${port}:5432" \
    -e POSTGRES_PASSWORD=cairn -e POSTGRES_USER=cairn postgres:17-alpine \
    -c max_prepared_transactions=16 >/dev/null
fi
lock_release
# Over TCP: during init the image runs a socket-only server that the unix
# socket check would mistake for the real one.
for _ in $(seq 1 60); do
  docker exec "$name" pg_isready -h 127.0.0.1 -U cairn >/dev/null 2>&1 && ready=1 && break
  sleep 0.5
done
if [ -z "${ready:-}" ]; then
  echo "postgres in $name did not become ready" >&2
  exit 1
fi

echo "postgres://cairn:cairn@127.0.0.1:${port}/postgres?sslmode=disable"
