#!/usr/bin/env bash
# The checks CI's test job runs: gofmt, go vet, every test including the
# ledger's Postgres-backed ones, the manifest tool, and manifest freshness.
#
# Uses CAIRN_TEST_DATABASE_URL when it is set, and otherwise starts a
# throwaway Postgres with scripts/test-postgres.sh (which needs Docker).
set -euo pipefail

cd "$(dirname "$0")/.."

unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "gofmt needed on:"
  echo "$unformatted"
  exit 1
fi

go vet ./...

if [ -z "${CAIRN_TEST_DATABASE_URL:-}" ]; then
  CAIRN_TEST_DATABASE_URL=$(scripts/test-postgres.sh)
fi
export CAIRN_TEST_DATABASE_URL
# Fail, rather than skip, a ledger test that needs a database or a server
# that can prepare transactions.
export CAIRN_REQUIRE_DATABASE=1
go test -race ./...

go -C cmd/cairn-manifest vet ./...
go -C cmd/cairn-manifest test ./...

go -C cmd/cairn-manifest run . -dir ../..
# status, not diff: it also catches a manifest that was never committed.
if [ -n "$(git status --porcelain -- spec.manifest.yaml)" ]; then
  git diff -- spec.manifest.yaml
  msg="spec.manifest.yaml is stale: run 'go -C cmd/cairn-manifest run . -dir ../..' and commit it"
  # In GitHub Actions, an ::error:: line becomes an annotation on the run.
  if [ -n "${GITHUB_ACTIONS:-}" ]; then
    echo "::error::$msg"
  else
    echo "$msg" >&2
  fi
  exit 1
fi
