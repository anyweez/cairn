#!/usr/bin/env bash
# QA reproduction for The incomplete_outcome mutant also survives under -short,
# recorded by spine finding. It failed against ebad5e275b6321da2a939b6ce636ff5a07d1689a;
# the fix is judged by this script passing.
go test -short ./examples/decisions/ -run 'TestRegisterSpecCatchesMutants/incomplete_outcome' -rapid.seed=20
