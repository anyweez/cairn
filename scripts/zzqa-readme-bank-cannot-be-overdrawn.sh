#!/usr/bin/env bash
# The bank account in README.md's "A short demo" is the decider a reader
# copies, and the README states its rule: B1, Never, "An account's balance goes
# below zero." Its Deposit branch accepts any positive amount and adds it to the
# balance with no bound, so two deposits that overflow int leave a negative
# balance: the message is accepted, the wrong balance is folded into the state,
# and the ledger (which stores events append-only) would keep it.
#
# This extracts the demo's own Go from README.md, so it fails while the
# documented decider can be overdrawn and passes once the README's branch
# refuses a deposit the balance cannot hold.
set -euo pipefail

repo=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT

awk '
  /^```go$/ { inblock = 1; buf = ""; next }
  inblock && /^```$/ {
    if (buf ~ /type Account struct/) { printf "%s", buf; exit }
    inblock = 0
    next
  }
  inblock { buf = buf $0 "\n" }
' "$repo/README.md" > "$tmp/demo.part"

if ! grep -q 'type Account struct' "$tmp/demo.part"; then
  echo "could not find the README's demo decider" >&2
  exit 2
fi

{
  cat <<'GO'
package main

import (
	"fmt"
	"math"
	"os"

	"github.com/anyweez/cairn"
)

GO
  cat "$tmp/demo.part"
  cat <<'GO'

type Msg interface{ isMsg() }
type Event interface{ isEvent() }
type Effect interface{ isEffect() }

func (Deposit) isMsg()         {}
func (Withdraw) isMsg()        {}
func (Deposited) isEvent()     {}
func (Withdrew) isEvent()      {}
func (WarnLowBalance) isEffect() {}

func main() {
	state := Bank.Initial()
	for _, m := range []Msg{Deposit{Amount: math.MaxInt64}, Deposit{Amount: 1}} {
		d := Bank.Decide(state, m)
		if d.Refused() {
			fmt.Println("PASS: the demo refused the deposit:", d.Refusal)
			return
		}
		state = Bank.Fold(state, d.Events)
	}
	if state.Balance < 0 {
		fmt.Printf("FAIL: the README's Bank accepted two deposits and holds %d, below the zero its own B1 rule forbids\n", state.Balance)
		os.Exit(1)
	}
	fmt.Println("PASS: balance", state.Balance)
}
GO
} > "$tmp/main.go"

cat > "$tmp/go.mod" <<EOF
module readme.demo

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => $repo
EOF
cp "$repo/go.sum" "$tmp/go.sum"

cd "$tmp"
go run .
