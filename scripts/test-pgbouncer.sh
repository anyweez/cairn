#!/usr/bin/env bash
# Starts (or reuses) a PgBouncer in transaction mode in front of the test
# Postgres from scripts/test-postgres.sh, and prints the URL to export. With it
# the ledger tests run the way a pooled deployment connects, such as Supabase
# behind its pooler: a server connection is held only for a transaction, and
# pgx describes statements rather than preparing them.
#
#   export CAIRN_TEST_DATABASE_URL=$(scripts/test-pgbouncer.sh)
set -euo pipefail

source "$(dirname "$0")/containers.sh"

cd "$(dirname "$0")/.."
. scripts/lib-lock.sh

scripts/test-postgres.sh >/dev/null

network=cairn-test
pg_port=${CAIRN_TEST_PORT:-55871}
postgres=$(fixture_name cairn-test-postgres 55871 "$pg_port")
port=${CAIRN_TEST_PGBOUNCER_PORT:-55873}
name=$(fixture_name cairn-test-pgbouncer 55873 "$port")
# DB_HOST is fixed when the container is created, so a PgBouncer in front of a
# different Postgres must be a different container.
[ "$pg_port" = 55871 ] || name="${name}-pg-${pg_port}"

# Creating the network and the container is a check-then-act: callers that
# start together both see neither, and one caller's `docker rm -f` can delete
# the container another just started. Serialise them, so the loser reuses the
# winner's network and container instead of colliding with it on the name.
lock_acquire cairn-test-pgbouncer
docker network inspect "$network" >/dev/null 2>&1 || docker network create "$network" >/dev/null
docker network connect "$network" "$postgres" 2>/dev/null || true

if ! container_publishes "$name" 5432 "$port"; then
  docker rm -f "$name" >/dev/null 2>&1 || true
  # No DB_NAME: every database name is passed through, since each test
  # creates its own.
  docker run -d --name "$name" --network "$network" -p "127.0.0.1:${port}:5432" \
    -e DB_HOST="$postgres" -e DB_USER=cairn -e DB_PASSWORD=cairn \
    -e AUTH_TYPE=scram-sha-256 -e POOL_MODE=transaction \
    -e MAX_CLIENT_CONN=1000 -e DEFAULT_POOL_SIZE=40 \
    edoburu/pgbouncer:v1.24.1-p1 >/dev/null
fi
lock_release
for _ in $(seq 1 60); do
  docker exec "$name" sh -c 'nc -z 127.0.0.1 5432' >/dev/null 2>&1 && ready=1 && break
  sleep 0.5
done
if [ -z "${ready:-}" ]; then
  echo "pgbouncer in $name did not become ready" >&2
  exit 1
fi

echo "postgres://cairn:cairn@127.0.0.1:${port}/postgres?sslmode=disable&default_query_exec_mode=cache_describe"
