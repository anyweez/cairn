#!/usr/bin/env bash
# The fixture says it "starts (or reuses) a throwaway Postgres for the ledger
# tests". Its reuse check asks only that a container of the fixture's name is
# running and publishes 5432 on the requested host port; it never asks whether
# that container was created with the settings the tests need. A container the
# fixture itself created before -c max_prepared_transactions=16 was added is
# reused, the script reports success, and the gate then fails on every run
# with:
#
#     max_prepared_transactions is 0, so PREPARE TRANSACTION is disabled;
#     set it above 0 (scripts/test-postgres.sh does) ...
#
# which points at the script that just reused the container. The container
# outlives the checkout (that is why the fixture reuses instead of
# recreating), so this is the state of every developer machine that ran the
# fixture before that flag existed.
#
# This starts the container with exactly the command the script used before
# the flag was added, then runs the fixture and asks whether the server it
# hands back can prepare transactions. It passes once the fixture either
# recreates a container that is missing the setting, or refuses to reuse it and
# says why; it fails while the script reports a server that cannot run the
# gate's prepared-transaction test.
set -euo pipefail

cd "$(dirname "$0")/.."

name=cairn-test-postgres
port=${CAIRN_TEST_PORT:-55871}

restore() {
  docker rm -f "$name" >/dev/null 2>&1 || true
  # Put the fixture back the way the run found it.
  scripts/test-postgres.sh >/dev/null 2>&1 || true
}
trap restore EXIT

docker rm -f "$name" >/dev/null 2>&1 || true
# The command scripts/test-postgres.sh ran before 2641db5 added
# -c max_prepared_transactions=16.
docker run -d --name "$name" -p "127.0.0.1:${port}:5432" \
  -e POSTGRES_PASSWORD=cairn -e POSTGRES_USER=cairn postgres:17-alpine >/dev/null
for _ in $(seq 1 60); do
  docker exec "$name" pg_isready -h 127.0.0.1 -U cairn >/dev/null 2>&1 && ready=1 && break
  sleep 0.5
done
if [ -z "${ready:-}" ]; then
  echo "the stale fixture did not become ready" >&2
  exit 2
fi

if ! out=$(scripts/test-postgres.sh 2>&1); then
  echo "$out"
  # Refusing to reuse the container is a fix, as long as it says what is wrong.
  if grep -qi 'max_prepared_transactions' <<<"$out"; then
    echo "ok: the fixture refused the container and named the setting it lacks"
    exit 0
  fi
  echo "FAIL: the fixture failed without naming the setting the reused container lacks" >&2
  exit 1
fi
echo "the fixture handed back: $out"

got=$(docker exec "$name" psql -U cairn -d postgres -Atc 'SHOW max_prepared_transactions')
echo "the reused container has max_prepared_transactions=${got}"
if [ "$got" = "0" ]; then
  cat >&2 <<'MSG'
FAIL: the fixture reuses a container that cannot run the gate's
      prepared-transaction test, and reports success anyway. The gate then
      fails on every run with a message saying scripts/test-postgres.sh sets
      max_prepared_transactions, which it does not do for a container it
      reuses.
MSG
  exit 1
fi
echo "ok: the container the fixture reuses can prepare transactions"
