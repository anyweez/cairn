#!/usr/bin/env bash
# QA reproduction: the state a View.Write is handed is the memory the store's
# state cache keeps and decides the next message from, but View.Write's doc
# never says it must not be changed -- while EntryView.Write's doc, in the same
# file, does.
#
# The contract is real and relied on: ledger/cache.go says "Decide, Evolve and
# views are handed it, and must not change it". A view that changes the state it
# is given (sorting the list it is about to store, say) silently changes what
# the next message decides from, and nothing reports it until Verify.
set -uo pipefail
cd "$(dirname "$0")"

fail=0

# The two places that already state the contract this reproduction is about.
grep -q "views are handed it, and must not" ledger/cache.go || {
  echo "FAIL: ledger/cache.go no longer states that views must not change the state"
  fail=1
}
grep -q "must not change the states it is" ledger/entryview.go || {
  echo "FAIL: EntryView.Write no longer states that it must not change the states it is given"
  fail=1
}

# The API a View author implements has to state it as well.
if ! grep -q "must not change" ledger/view.go; then
  echo "FAIL: View.Write's doc does not say the state it is handed must not be changed."
  echo "      A view that changes it changes what the next message decides from, silently."
  fail=1
fi

exit "$fail"
