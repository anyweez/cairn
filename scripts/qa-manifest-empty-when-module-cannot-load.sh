#!/usr/bin/env bash
# qa: cairn-manifest exits 0 with a manifest that lists no rules, no codecs and
# no tables, when it could not load the module's packages at all.
#
# The module below declares an fsm table and a rule. Its go.mod is not tidied
# for the cairn module it replaces (a routine state: add an import, run the
# documented command, tidy afterwards), so `go list ./...` fails with "updates
# to go.mod needed". x/tools' loader reports no packages and no error for that
# failure, the extractor walks an empty package list, and the tool writes
# `rules: [] codecs: [] tables: []` over the caller's spec.manifest.yaml and
# exits 0, so "read nothing" is indistinguishable from "declares nothing" --
# the opposite of Extract's own promise that a manifest it cannot state exactly
# is an error rather than a guess.
#
# Passes once the tool reports the load failure instead of writing an empty
# manifest.
set -euo pipefail

cd "$(dirname "$0")/.."
root=$(pwd)

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# Sanity: the tool itself works here, and reads this repository's manifest. A
# failure before this point is the environment's, not the defect's.
if ! go -C cmd/cairn-manifest run . -dir ../.. -o - | grep -q 'examples/decisions.Lifecycle'; then
  echo "the manifest tool does not read this repository, so the check below proves nothing" >&2
  exit 2
fi

cat > "$work/go.mod" <<EOF
module example.com/unloadable

go 1.21

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => $root
EOF

cat > "$work/register.go" <<'EOF'
package unloadable

import (
	"github.com/anyweez/cairn/blocks/fsm"
	"github.com/anyweez/cairn/spec"
)

type Status string

const (
	Draft Status = "draft"
	Live  Status = "live"
)

var Lifecycle = fsm.New(
	fsm.Edge[Status, string]{From: Draft, On: "publish", To: Live},
)

var NeverLiveWithoutPublishing = spec.Rule{ID: "U1", Kind: spec.Never, Status: spec.Established,
	Statement: "a decision is live without being published."}
EOF

if ! grep -q 'fsm.New' "$work/register.go"; then
  echo "the fixture stopped declaring a table" >&2
  exit 2
fi

set +e
go -C cmd/cairn-manifest run . -dir "$work" -o - >"$work/manifest.yaml" 2>"$work/stderr"
code=$?
set -e

if [ "$code" -eq 0 ]; then
  if grep -q 'Lifecycle' "$work/manifest.yaml"; then
    echo "ok: the module's table is in the manifest"
    exit 0
  fi
  echo "FAIL: the tool exited 0 after reading nothing; the manifest says the module declares no table:" >&2
  cat "$work/manifest.yaml" >&2
  exit 1
fi

# Failing is the right answer. The sanity check above already proved the tool
# builds and reads a module, so a non-zero exit here is the load failure being
# reported rather than the tool breaking.
echo "ok: the tool reported the load failure (exit $code) instead of an empty manifest"
exit 0
