#!/usr/bin/env bash
# A fixture run killed between taking the lock and releasing it leaves the lock
# directory behind with its PID in it, and scripts/lib-lock.sh takes that lock
# over when `kill -0` says the PID is gone. But a PID is only unique while the
# process lives: after the holder dies the number is handed out again, and if
# the lock's recorded PID now belongs to an unrelated live process, every later
# fixture run waits on it forever. Nothing ages a lock that records a PID, so
# there is no window after which it is taken over, and no fixture script has a
# timeout: the run hangs with no message until a person removes the directory.
#
# Needs only bash and the standard utilities; no Docker, database or network.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
name=zzqa-pidreuse-$$
lock=$tmp/$name.lock
unrelated=
waiter=

cleanup() {
  [ -z "$waiter" ] || kill "$waiter" 2>/dev/null
  [ -z "$unrelated" ] || kill "$unrelated" 2>/dev/null
  rm -rf "$tmp"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# The unrelated live process whose PID the dead holder's lock now records. A
# sleep is enough: all the lock checks is whether the PID is alive.
sleep 300 &
unrelated=$!

mkdir "$lock"
printf '%s\n' "$unrelated" >"$lock/pid"
# The lock was taken long ago, by a caller that is gone: only the reused PID
# says otherwise.
printf '0\n' >"$lock/at"

TMPDIR=$tmp bash -c ". '$here/lib-lock.sh'; lock_acquire $name; printf 'acquired\n'" \
  >"$tmp/out" 2>&1 &
waiter=$!

# Five seconds is far longer than a takeover of a lock nobody holds takes; the
# empty-lock window is the only wait the script documents, and it is a minute
# of *observation* that starts now, so a lock taken over on the first pass
# acquires at once.
for _ in $(seq 1 50); do
  kill -0 "$waiter" 2>/dev/null || break
  sleep 0.1
done

if kill -0 "$waiter" 2>/dev/null; then
  kill "$waiter" 2>/dev/null
  wait "$waiter" 2>/dev/null
  fail "lock_acquire never took over the lock: its recorded PID ($unrelated) is alive but is not the holder," \
    "and no fixture script would ever get past it"
fi
if ! wait "$waiter"; then
  fail "lock_acquire exited non-zero: $(cat "$tmp/out")"
fi
grep -qx acquired "$tmp/out" || fail "lock_acquire did not acquire the lock: $(cat "$tmp/out")"
echo PASS
