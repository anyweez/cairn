#!/usr/bin/env bash
# A taker killed while it is taking a stale lock over leaves its claim
# directory behind. scripts/lib-lock.sh documents that as a state it recovers
# from: "A taker killed while taking the lock over leaves a claim that nothing
# would ever clear, so a claim whose taker is gone is removed and retried."
#
# With several callers starting together -- two fixture scripts launched at
# once, which factory.yaml says is safe -- the recovery is not idempotent. Each
# caller that finds the dead claim removes it with a blind `rm -rf`, and two
# removals racing over the same directory make one of them fail. That failure
# is reported as a lock this caller cannot remove:
#
#     lock .../cairn-test-postgres.lock: cannot remove the stale lock directory;
#     remove it by hand
#
# and lock_acquire returns 1, which under `set -e` ends the fixture script.
# What it names is not a leftover to remove: another caller has already taken
# the lock over and is holding it. The run has to be retried, and the message
# sends a person looking for a directory that is, by then, the live lock.
#
# Each round lays down the state a killed taker leaves (a lock whose holder is
# gone, with its claim's taker gone too) and starts ten callers at once. Every
# caller must acquire the lock and release it; none may exit instead, and no
# two may be inside the critical section together.
set -u

here=$(cd "$(dirname "$0")" && pwd)
rounds=${1:-30}
work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

cat >"$work/worker.sh" <<WORKER
#!/usr/bin/env bash
set -u
. "$here/lib-lock.sh"
i=\$1
if ! lock_acquire qa-dead-claim 2>>"\$TMPDIR/stderr"; then
  echo "caller \$i (pid \$\$) exited instead of acquiring the lock" >>"\$TMPDIR/fail"
  exit 0
fi
# mkdir is atomic, so this notices two callers in the critical section at once.
if ! mkdir "\$TMPDIR/crit" 2>/dev/null; then
  echo "caller \$i (pid \$\$) entered while another caller held the lock" >>"\$TMPDIR/violations"
fi
sleep 0.05
rmdir "\$TMPDIR/crit" 2>/dev/null
lock_release
WORKER

fail() {
  echo "FAIL: $*"
  exit 1
}

for round in $(seq 1 "$rounds"); do
  tmp="$work/r$round"
  mkdir -p "$tmp/qa-dead-claim.lock/claim"
  export TMPDIR="$tmp"
  # A holder that is gone, and a taker that died taking it over: exactly the
  # directory state a SIGKILL between those steps leaves.
  echo 99999999 >"$tmp/qa-dead-claim.lock/pid"
  date +%s >"$tmp/qa-dead-claim.lock/at"
  echo 99999998 >"$tmp/qa-dead-claim.lock/claim/pid"

  for i in $(seq 1 10); do bash "$work/worker.sh" "$i" & done
  wait

  if [ -s "$tmp/violations" ]; then
    cat "$tmp/violations"
    fail "two callers held the lock at once"
  fi
  if [ -s "$tmp/fail" ]; then
    cat "$tmp/fail"
    if [ -s "$tmp/stderr" ]; then
      echo "stderr from the callers:"
      sort -u "$tmp/stderr" | sed 's/^/  /'
    fi
    fail "a caller exited instead of acquiring a lock whose stale claim is recoverable (round $round of $rounds)"
  fi
done

echo "ok: every caller acquired a lock left with a dead taker's claim, in all $rounds rounds"
