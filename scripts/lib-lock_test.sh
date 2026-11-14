#!/usr/bin/env bash
# Checks scripts/lib-lock.sh hands a lock over when its holder is gone, and
# does not hand it over while the holder is alive. A first-time `docker pull`
# inside `docker run` can hold the lock for more than a minute, so a caller let
# in at that point races the same container name the lock exists to protect.
# It also checks that a caller whose lock was taken over while it worked does
# not delete the new holder's lock when it releases.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
tmp=$(mktemp -d)
name=cairn-lock-test
lock=$tmp/$name.lock
holder=
old_holder=

cleanup() {
  [ -z "$holder" ] || kill "$holder" 2>/dev/null || true
  [ -z "$old_holder" ] || kill "$old_holder" 2>/dev/null || true
  rm -rf "$tmp"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# wait_for PID SECONDS succeeds only when PID exits within SECONDS.
wait_for() {
  local pid=$1 tries=$(( $2 * 10 ))
  while kill -0 "$pid" 2>/dev/null && [ "$tries" -gt 0 ]; do
    sleep 0.1
    tries=$((tries - 1))
  done
  ! kill -0 "$pid" 2>/dev/null
}

# A live holder keeps the lock however long it has held it: its PID is alive,
# and the time it took the lock is long past.
sleep 30 &
holder=$!
mkdir "$lock"
printf '%s\n' "$holder" >"$lock/pid"
printf '%s\n' 0 >"$lock/at"

TMPDIR=$tmp bash -c ". '$here/lib-lock.sh'; lock_acquire $name" >/dev/null 2>&1 &
waiter=$!
sleep 2
if ! kill -0 "$waiter" 2>/dev/null; then
  wait "$waiter"
  fail "took the lock from live holder $holder (caller exited $?)"
fi
kill "$waiter" 2>/dev/null
wait "$waiter" 2>/dev/null
[ "$(cat "$lock/pid" 2>/dev/null)" = "$holder" ] ||
  fail "the live holder's lock was replaced"

# A holder killed before it recorded itself leaves no PID to check, so the lock
# is taken over once it has stayed empty for more than a minute. A date that
# jumps 100 seconds per call reaches that bound without the test sleeping.
mkdir -p "$tmp/bin"
cat >"$tmp/bin/date" <<'EOF'
#!/usr/bin/env bash
n=$(cat "$FAKE_DATE_COUNTER")
n=$((n + 1))
printf '%s\n' "$n" >"$FAKE_DATE_COUNTER"
printf '%s\n' "$((1000 + n * 100))"
EOF
chmod +x "$tmp/bin/date"

rm -rf "$lock"
mkdir "$lock"
: >"$tmp/counter"
TMPDIR=$tmp PATH="$tmp/bin:$PATH" FAKE_DATE_COUNTER="$tmp/counter" \
  bash -c ". '$here/lib-lock.sh'; lock_acquire $name" >/dev/null 2>&1 &
acquirer=$!
wait_for "$acquirer" 5 || { kill "$acquirer" 2>/dev/null; fail "an empty lock was never taken over"; }
wait "$acquirer" || fail "the caller exited non-zero after taking over an empty lock"
[ -d "$lock" ] && fail "the caller did not release the lock"

# A holder whose PID is gone is taken over at once, without waiting a minute.
# The PID is beyond any pid_max, so it cannot belong to a running process.
dead=99999999
mkdir "$lock"
printf '%s\n' "$dead" >"$lock/pid"
printf '%s\n' 0 >"$lock/at"

TMPDIR=$tmp bash -c ". '$here/lib-lock.sh'; lock_acquire $name" >/dev/null 2>&1 &
acquirer=$!
wait_for "$acquirer" 5 || { kill "$acquirer" 2>/dev/null; fail "a dead holder's lock was never taken over"; }
wait "$acquirer" || fail "the caller exited non-zero after taking over a dead holder's lock"
[ -d "$lock" ] && fail "the caller did not release the lock"

echo "ok: a live holder keeps the lock, and a gone or unrecorded one is taken over"

# A lock taken over while its holder is still working belongs to its new
# holder. The old holder's release must remove the directory it created, not
# whatever the name holds when it is called.
taken=$tmp/$name-taken.lock
new_pid=$$ # this test's own PID, alive for as long as it matters
cat >"$tmp/taken.sh" <<EOF
. '$here/lib-lock.sh'
lock_acquire $name-taken
: >"$tmp/taken-acquired"
while [ ! -e "$tmp/taken-released" ]; do sleep 0.05; done
lock_release
EOF
TMPDIR=$tmp bash "$tmp/taken.sh" &
old_holder=$!
for _ in $(seq 1 50); do
  [ -e "$tmp/taken-acquired" ] && break
  sleep 0.1
done
[ -e "$tmp/taken-acquired" ] || { kill "$old_holder" 2>/dev/null; fail "the caller never took the lock"; }
mv "$taken" "$taken.stale"
mkdir "$taken"
printf '%s\n' "$new_pid" >"$taken/pid"
printf '%s\n' 0 >"$taken/at"
: >"$tmp/taken-released"
wait "$old_holder" 2>/dev/null
old_holder=
[ "$(cat "$taken/pid" 2>/dev/null)" = "$new_pid" ] ||
  fail "a caller whose lock was taken over deleted the new holder's lock"

echo "ok: a taken-over caller's release leaves the new holder's lock alone"
