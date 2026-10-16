#!/usr/bin/env bash
# QA reproduction for examples/decisions mutant test also fails on full-length runs, not only under -short,
# recorded by spine finding. It failed against 4735fd71abc82ca037953a4127cb8f998ef04db5;
# the fix is judged by this script passing.
go test ./examples/decisions/ -run TestRegisterSpecCatchesMutants -rapid.seed=116
