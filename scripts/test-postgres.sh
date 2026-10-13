#!/usr/bin/env bash
# Starts (or reuses) a throwaway Postgres for the ledger tests and prints the
# URL to export. Each test creates and drops its own database inside it.
#
#   export CAIRN_TEST_DATABASE_URL=$(scripts/test-postgres.sh)
set -euo pipefail

name=cairn-test-postgres
port=${CAIRN_TEST_PORT:-55871}

if ! docker ps --format '{{.Names}}' | grep -qx "$name"; then
  docker rm -f "$name" >/dev/null 2>&1 || true
  docker run -d --name "$name" -p "127.0.0.1:${port}:5432" \
    -e POSTGRES_PASSWORD=cairn -e POSTGRES_USER=cairn postgres:17-alpine >/dev/null
  for _ in $(seq 1 60); do
    docker exec "$name" pg_isready -U cairn >/dev/null 2>&1 && break
    sleep 0.5
  done
fi

echo "postgres://cairn:cairn@127.0.0.1:${port}/postgres?sslmode=disable"
