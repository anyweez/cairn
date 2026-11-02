#!/usr/bin/env bash
# README.md, "What the checker guarantees":
#
#   Vacuity is only enforced on a full-length check (rapid's default 100 runs).
#   Under `-short` or a lowered `-rapid.checks` it is only reported, since a
#   rarely applied rule can miss a short check by chance.
#
# So lowering the check count is a documented way to shorten a spec check, and
# the checker honours it: gateVacuity compares the completed runs with
# vacuityRuns (100) and reports instead of failing below it.
#
# Three of spectest's own tests assert the *full-length* behaviour, and their
# guard covers only one of the two documented ways to shorten a check:
#
#   if testing.Short() { t.Skip("vacuity is only enforced on a full-length check") }
#
# With -rapid.checks=50 the checker correctly reports, the tests' premise is
# false, and the suite goes red with messages that blame the checker:
#
#   --- FAIL: TestVacuousEstablishedRuleFails
#       a rule that never applies must fail the check, got err=<nil> ""
#   --- FAIL: TestVacuousEstablishedRuleStillFailsWhenTheGeneratorRejectsDraws
#       a full-length check must still fail a rule no valid run reached, got err=<nil> ""
#   --- FAIL: TestTwoRulesSharingAnIDAreNotMerged
#       Z1 never applied as first declared, but the check passed: the two rules
#       were merged by ID
#
# The tests should skip (or assert the report-only behaviour) whenever the
# effective check count is below the gate's threshold, not only under -short.
# The flag has to follow the package list: before it, go test passes it and
# everything after it to the default package's test binary, which here has no
# tests and exits 0.
set -euo pipefail
cd "$(dirname "$0")"

go test ./spectest/ -count=1 -rapid.checks=50 \
  -run 'TestVacuousEstablishedRuleFails|TestVacuousEstablishedRuleStillFailsWhenTheGeneratorRejectsDraws|TestTwoRulesSharingAnIDAreNotMerged'
