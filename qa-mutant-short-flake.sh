#!/usr/bin/env bash
# The spec's mutant tests ask rapid to search for a counterexample, but under
# `go test -short` rapid runs only a fifth as many checks (20, not 100). The
# generator rarely reaches a decided decision that then moves sideways or
# records again, so the DR5 and DR6 mutants survive some runs and the test
# fails. Run it until that happens.
set -u

iterations=${1:-40}
for i in $(seq 1 "$iterations"); do
  if ! out=$(go test -short -count=1 \
      -run 'TestRegisterSpecCatchesMutants/(no_freeze|outcome_rewritten)' \
      ./examples/decisions/ 2>&1); then
    echo "mutant survived on iteration $i of $iterations:"
    echo "$out"
    exit 1
  fi
done

echo "no mutant survived in $iterations runs"
exit 0
