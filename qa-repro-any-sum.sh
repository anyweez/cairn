#!/usr/bin/env bash
# QA reproduction: a codec whose sum type is the empty interface cannot pass
# the manifest cross-check. The source writes `codec.Codec[any]`, so
# cairn-manifest reads the sum with go/types and records `any`; the runtime
# half names the same type with reflect, which has no name for it and renders
# its underlying type as `interface {}`. codec.CheckRegistrations then reports
# one correct registration in both directions, so the program's manifest test
# can never pass.
set -euo pipefail

root=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

cat > "$tmp/go.mod" <<EOF
module example.com/anysum

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => $root
EOF

cat > "$tmp/anysum.go" <<'EOF'
package anysum

import "github.com/anyweez/cairn/codec"

// Item is a stored result.
type Item struct {
	N string `json:"n"`
}

// Codecs uses any as the sum type: different accepted messages return
// different result shapes, so there is no common interface to name.
func Codecs() *codec.Codec[any] {
	c := codec.New[any]()
	codec.Register[Item](c, "anysum.item", 1)
	return c
}
EOF

mkdir -p "$tmp/cmd/print"
cat > "$tmp/cmd/print/main.go" <<'EOF'
package main

import (
	"fmt"

	anysum "example.com/anysum"
)

func main() {
	fmt.Println(anysum.Codecs().Registrations()[0].Sum)
}
EOF

runtime_sum=$(cd "$tmp" && go run ./cmd/print)
manifest_sum=$(cd "$root" && go -C cmd/cairn-manifest run . -dir "$tmp" -o - 2>/dev/null |
  awk '/^  - sum: /{print $3; exit}')

echo "codec.Registrations: $runtime_sum"
echo "spec.manifest.yaml:  $manifest_sum"
if [ "$runtime_sum" != "$manifest_sum" ]; then
  echo "FAIL: the runtime and the static extractor name the same sum type differently,"
  echo "      so codec.CheckRegistrations reports one correct registration both ways:"
  echo "        anysum.item ($manifest_sum) is in the manifest but not registered by the codecs"
  echo "        anysum.item ($runtime_sum) is registered by the codecs but not in the manifest"
  exit 1
fi
echo "PASS: both halves name the sum type identically"
