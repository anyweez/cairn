#!/usr/bin/env bash
# QA reproduction: a codec whose sum type is a defined slice (a result that is
# a list) is named "[]Item" by codec.Registrations and "pkg.Listing" by
# cairn-manifest. The two halves of the cross-check the README documents
# therefore disagree about the same registration: codec.CheckRegistrations
# reports it as both missing from the manifest and registered but not in it,
# so a correct program's manifest test can never pass.
set -euo pipefail

root=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/go.mod" <<EOF
module example.com/namedsum

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => $root
EOF

cat > "$tmp/namedsum.go" <<'EOF'
package namedsum

import "github.com/anyweez/cairn/codec"

// Listing is a result type that is a list: a decider whose accepted message
// returns the current lines.
type Listing []string

func Codecs() *codec.Codec[Listing] {
	c := codec.New[Listing]()
	codec.Register[Listing](c, "namedsum.listing", 1)
	return c
}
EOF

mkdir -p "$tmp/cmd/print"
cat > "$tmp/cmd/print/main.go" <<'EOF'
package main

import (
	"fmt"

	namedsum "example.com/namedsum"
)

func main() {
	fmt.Println(namedsum.Codecs().Registrations()[0].Sum)
}
EOF

runtime_sum=$(cd "$tmp" && go run ./cmd/print)
manifest_sum=$(cd "$root" && go -C cmd/cairn-manifest run . -dir "$tmp" -o - 2>/dev/null |
  awk '/^  - sum: /{print $3; exit}')

echo "codec.Registrations: $runtime_sum"
echo "spec.manifest.yaml:  $manifest_sum"
if [ "$runtime_sum" != "$manifest_sum" ]; then
  echo "FAIL: the runtime and the static extractor name the same sum type differently,"
  echo "      so the documented cross-check reports one registration as both"
  echo "      missing from the manifest and extra in the codecs."
  exit 1
fi
echo "PASS: both halves name the sum type identically"
