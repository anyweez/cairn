#!/usr/bin/env bash
# Starts (or reuses) a throwaway Postgres for the ledger tests and prints the
# URL to export. Each test creates and drops its own database inside it. A
# changed CAIRN_TEST_PORT gets its own container, so sessions on different
# ports do not disturb each other. Starting or reusing it rolls back prepared
# transactions a killed test run left behind, which would otherwise hold the
# server's max_prepared_transactions slots.
#
#   export CAIRN_TEST_DATABASE_URL=$(scripts/test-postgres.sh)
set -euo pipefail

source "$(dirname "$0")/containers.sh"
. "$(dirname "$0")/lib-lock.sh"

port=${CAIRN_TEST_PORT:-55871}
name=$(fixture_name cairn-test-postgres 55871 "$port")

# The check and the creation are not atomic on their own: callers that start
# together both see no container, and one caller's `docker rm -f` can delete the
# container another just started. Serialise them, so the loser finds the
# winner's container instead of colliding with it on the name.
lock_acquire cairn-test-postgres
if ! container_publishes "$name" 5432 "$port"; then
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

# A run killed between PREPARE TRANSACTION and COMMIT PREPARED leaves its
# prepared transaction behind, and each one holds a slot of
# max_prepared_transactions, so a reused container carries them into every
# later run until no slot is left. This suite names the prepared transactions
# it creates cairn_test_prepared*, so clear those. Postgres finishes a prepared
# transaction only from the database that prepared it, so each is rolled back
# over its own connection. A database with another live connection belongs to
# a test still running, and is left alone.
cleared=0
while IFS=$'\t' read -r db gid; do
  [ -n "${db:-}" ] || continue
  printf "ROLLBACK PREPARED :'gid';\n" |
    docker exec -i "$name" psql -U cairn -d "$db" -v gid="$gid" -At >/dev/null 2>&1 || continue
  cleared=$((cleared + 1))
done < <(docker exec "$name" psql -U cairn -d postgres -At -F $'\t' -c "
  SELECT p.database, p.gid FROM pg_prepared_xacts p
  WHERE p.gid LIKE 'cairn_test_prepared%'
    AND NOT EXISTS (SELECT 1 FROM pg_stat_activity a
                    WHERE a.datname = p.database AND a.pid <> pg_backend_pid())")
if [ "$cleared" -gt 0 ]; then
  echo "cleared $cleared prepared transaction(s) left by interrupted runs" >&2
fi

echo "postgres://cairn:cairn@127.0.0.1:${port}/postgres?sslmode=disable"
