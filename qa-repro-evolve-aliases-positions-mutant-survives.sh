#!/usr/bin/env bash
# The D4 ("evolve aliases positions") mutant subtest in
# examples/decisions/spec_test.go proves the Isolated rule has teeth only when
# rapid happens to draw the state its Evolve mutation needs: a second
# PositionSaved on a stream whose Positions map is already non-nil. It uses the
# broad machine() generator, not the aimed one the table mutants use (t2482),
# so a smaller check budget lets the mutant survive.
#
# With the budget lowered to one check, seeds 1..6 all survive. Under the
# -short inner loop (20 checks) the same kill is missed in a fraction of runs:
# `go test -short ./examples/decisions/` failed 1 run in 30 and 1 in 6 while
# writing this, with
#
#     --- FAIL: TestRegisterSpecCatchesMutants/evolve_aliases_positions
#         spec_test.go:394: aliasing Evolve must be caught by D4:
#
# The fix is judged by this script passing: the subtest must catch the mutant
# at any budget it is given, as the aimed mutants already do.
set -u

# Deterministic: one check is not enough for the broad generator to reach the
# mutation, so the mutant survives and the subtest fails.
go test -short ./examples/decisions/ \
	-run 'TestRegisterSpecCatchesMutants/evolve_aliases_positions' \
	-count=1 -rapid.checks=1 -rapid.seed=1
