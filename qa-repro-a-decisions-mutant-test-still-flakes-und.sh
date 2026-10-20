#!/usr/bin/env bash
# QA reproduction for A decisions mutant test still flakes under -short after the aimed generator fix,
# recorded by spine finding. It failed against 4949b5d5a0b444434d430c8be518007cd78a8f1d;
# the fix is judged by this script passing.
go test -short ./examples/decisions/ -run '^TestFreezeRuleCatchesOptionsChangedWhileLeavingOpen$' -count=1 -rapid.seed=1
