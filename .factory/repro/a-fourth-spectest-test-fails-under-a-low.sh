#!/usr/bin/env bash
# QA reproduction for A fourth spectest test fails under a lowered -rapid.checks, though vacuity is only reported then,
# recorded by spine finding. It failed against 502a7edc648cc3e6292d585f22051304c0a388e4;
# the fix is judged by this script passing.
go test ./spectest/ -rapid.checks=40 -run '^TestWithoutTheAllowVacuousEntryTheSharedIDRuleIsReportedVacuous$' -count=1
