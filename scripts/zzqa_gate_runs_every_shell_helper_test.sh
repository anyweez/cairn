#!/usr/bin/env bash
# scripts/check.sh runs the shell tests beside the fixture helpers only in part:
# it runs scripts/lib-lock_test.sh and three zzqa lock scripts, and none of the
# other tests in scripts/. A regression in a helper the gate never exercises
# therefore merges green -- the printed-URL and host-port helpers
# (scripts/container-host-port_test.sh, scripts/test-pgbouncer-port_test.sh) and
# the port derivation (scripts/fixture-port_test.sh) have had exactly that
# happen, and each regression was found by QA rather than by the gate.
#
# This asserts the gate runs every *_test.sh in scripts/, which is the
# repository's convention for a test of the shell code the fixtures are built
# from. The gate may name a test outright or run it through a glob, so a
# scripts/<name>.sh token in check.sh is expanded the way the shell would
# expand it. It needs no Docker, database or network: it reads the gate.
set -uo pipefail

cd "$(dirname "$0")/.."

covered=()
while IFS= read -r token; do
  for path in $token; do
    [ -f "$path" ] && covered+=("$path")
  done
done < <(grep -oE 'scripts/[A-Za-z0-9_.*-]+\.sh' scripts/check.sh || true)

is_covered() {
  local file=$1 c
  for c in ${covered[@]+"${covered[@]}"}; do
    [ "$c" = "$file" ] && return 0
  done
  return 1
}

missing=()
for test in scripts/*_test.sh; do
  is_covered "$test" || missing+=("$test")
done

if [ "${#missing[@]}" -gt 0 ]; then
  echo "scripts/check.sh runs no such test:" >&2
  printf '  %s\n' "${missing[@]}" >&2
  echo "a helper regression in any of them passes the gate" >&2
  exit 1
fi

echo "ok: the gate runs every shell test beside the fixture helpers"
