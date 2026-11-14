#!/usr/bin/env bash
# qa: scripts/check.sh — the local gate and CI's test job — never runs
# scripts/zzqa_manifest_interrupted_write_keeps_the_base.sh, the regression test
# for an interrupted cairn-manifest write (t3333: a write killed part-way
# truncated spec.manifest.yaml, and the next -base run reported a stored type as
# removed that was still in the code).
#
# Nothing else in the gate covers it. cairn-manifest's own Go suite passes with
# `writeFile` replaced by a truncating `os.WriteFile(path, data, 0o644)`, which
# is the defect, because no Go test can fill a filesystem; the shell test is the
# only thing that fails. So a regression of the atomic write lands with every
# check green, and the next run that passes -base against the truncated file
# refuses a stored type that never changed.
#
# Passes once scripts/check.sh invokes the test, beside the other shell tests
# whose failure paths no Go test reaches:
#
#   bash scripts/zzqa_manifest_interrupted_write_keeps_the_base.sh
#
# See t3608 for the same hole at scripts/fixture-port_test.sh.
set -euo pipefail

cd "$(dirname "$0")/.."

test=scripts/zzqa_manifest_interrupted_write_keeps_the_base.sh

if [ ! -f "$test" ]; then
	echo "FAIL: $test is gone, so nothing pins an interrupted manifest write" >&2
	exit 1
fi

if ! grep -q 'zzqa_manifest_interrupted_write_keeps_the_base' scripts/check.sh; then
	echo "FAIL: scripts/check.sh does not run $test" >&2
	echo "      the regression for an interrupted manifest write is never run," >&2
	echo "      though no Go test reaches that failure path" >&2
	exit 1
fi

# The test is run by the gate and still catches the regression it was written
# for. It needs no Docker and no database; it exits 2 rather than 0 when the
# host cannot build its tiny filesystem, so the gate's invocation has to tell
# "cannot check here" apart from "the write regressed".
echo "ok: scripts/check.sh runs $test"
