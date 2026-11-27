#!/usr/bin/env bash
# The gate is the only thing that runs the fixture scripts' tests: they are
# shell, so no Go test reaches them, and nothing else invokes them. A test in
# scripts/ that check.sh never runs is a regression test that cannot fail, so a
# change to scripts/containers.sh, scripts/lib-lock.sh or the manifest tool's
# write path passes every local run and every CI run.
#
# This fails while scripts/check.sh does not run every committed shell test in
# scripts/, and passes once the gate runs them and fails the gate when one
# fails. A file counts as a shell test when its name ends in _test.sh, or when
# it is named zzqa_*.sh (the regression tests a QA pass commits beside the
# scripts they cover). qa-*.sh and *-race.sh are reproductions of findings that
# a person runs while reading one, not tests of the scripts, and are not
# required here.
#
# The gate may name a test outright or run it through a glob, so both forms
# count as running it: every scripts/<name>.sh token in check.sh is expanded
# the way the shell would expand it.
set -euo pipefail
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
seen=()
for path in scripts/*_test.sh scripts/zzqa_*.sh; do
  [ -f "$path" ] || continue
  # A file matching both patterns is one test, not two.
  case " ${seen[*]-} " in *" $path "*) continue ;; esac
  seen+=("$path")
  is_covered "$path" || missing+=("$path")
done

if [ "${#missing[@]}" -gt 0 ]; then
  echo "FAIL: scripts/check.sh does not run ${#missing[@]} committed shell test(s):" >&2
  for path in "${missing[@]}"; do
    echo "  $path" >&2
  done
  echo "a regression in what they cover passes the gate, which CI runs too" >&2
  exit 1
fi

echo "ok: scripts/check.sh runs every committed shell test in scripts/"
