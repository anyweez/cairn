#!/usr/bin/env bash
# The manifest cross-check names a sum type twice: once with go/types in
# cmd/cairn-manifest, once with reflect in codec.goTypeString. An unnamed
# interface sum whose method carries a parameter name is spelled
# `interface{Accept(x int) string}` by go/types and `interface{Accept(int)
# string}` by reflect, so codec.CheckRegistrations reports one correct codec as
# both missing from the manifest and extra in the codecs.
set -euo pipefail

root=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/go.mod" <<EOF
module example.com/ifacesum

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => $root
EOF

cat > "$tmp/ifacesum.go" <<'EOF'
package ifacesum

import "github.com/anyweez/cairn/codec"

// Accepted is one value of the sum, which is an unnamed interface so its
// method's parameter name is part of the type expression go/types records.
type Accepted struct{ X int }

func (Accepted) Accept(x int) string { return "" }

func Codecs() *codec.Codec[interface{ Accept(x int) string }] {
	c := codec.New[interface{ Accept(x int) string }]()
	codec.Register[Accepted](c, "ifacesum.accepted", 1)
	return c
}
EOF

mkdir -p "$tmp/cmd/print"
cat > "$tmp/cmd/print/main.go" <<'EOF'
package main

import (
	"fmt"

	ifacesum "example.com/ifacesum"
)

func main() {
	fmt.Println(ifacesum.Codecs().Registrations()[0].Sum)
}
EOF

runtime_sum=$(cd "$tmp" && go run ./cmd/print)
manifest_sum=$(cd "$root" && go -C cmd/cairn-manifest run . -dir "$tmp" -o - |
	awk '/^  - sum: /{sub(/^  - sum: /, ""); print; exit}')
# A YAML scalar with spaces may be quoted. The type has no quote of its own,
# so removing one surrounding pair is enough to compare the scalar's value.
manifest_sum=${manifest_sum#\'}
manifest_sum=${manifest_sum%\'}
manifest_sum=${manifest_sum#\"}
manifest_sum=${manifest_sum%\"}

echo "codec.Registrations: $runtime_sum"
echo "spec.manifest.yaml:  $manifest_sum"
if [ "$runtime_sum" != "$manifest_sum" ]; then
	echo "FAIL: the runtime and the static extractor name the same unnamed interface"
	echo "      sum differently, so the documented cross-check reports one correct"
	echo "      registration as both missing from the manifest and extra in the codecs."
	exit 1
fi
echo "PASS: both halves name the sum type identically"
