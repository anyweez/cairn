#!/usr/bin/env bash
# scripts/lib-lock.sh promises that "a caller killed while holding it does not
# wedge the fixture": a lock whose holder is gone is taken over. That promise
# only holds while the lock directory can be created and removed. When it
# cannot -- TMPDIR points at a path that is not there, or a leftover lock
# directory sits in a directory the caller cannot write -- the takeover's
# `rm -rf` fails, `mkdir` keeps failing, and lock_acquire's loop has no exit.
#
# What a person then sees depends on which branch the loop is in: with no pid
# recorded it spins silently for a minute and then prints "no holder recorded
# for more than 60s, taking it" ten times a second, forever; with a pid that is
# gone it prints "holder N is gone, taking it" and then the same thing. Either
# way the fixture script that called it never returns and never says why.
#
# Each case below must end within a few seconds: either lock_acquire acquires
# the lock, or it exits non-zero and says which directory it could not use.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
name=cairn-zzqa-lock
logs=$(mktemp -d)
root=
caller=
code=

cleanup() {
  [ -z "$caller" ] || kill "$caller" 2>/dev/null
  [ -z "$root" ] || chmod 700 "$root" 2>/dev/null
  [ -z "$root" ] || rm -rf "$root"
  rm -rf "$logs"
}
trap cleanup EXIT

fail() { echo "FAIL: $*" >&2; exit 1; }

# acquire_for DIR SECONDS runs lock_acquire with TMPDIR=DIR and returns 0 when
# it exited within SECONDS (its status is left in $code, its stderr in
# $logs/err), or 1 when it was still running and had to be killed.
acquire_for() {
  local dir=$1 secs=$2 tries
  : >"$logs/err"
  TMPDIR=$dir bash -c ". '$here/lib-lock.sh'; lock_acquire $name" \
    >"$logs/out" 2>"$logs/err" &
  caller=$!
  tries=$(( $2 * 10 ))
  while kill -0 "$caller" 2>/dev/null && [ "$tries" -gt 0 ]; do
    sleep 0.1
    tries=$((tries - 1))
  done
  if kill -0 "$caller" 2>/dev/null; then
    kill "$caller" 2>/dev/null
    wait "$caller" 2>/dev/null
    caller=
    return 1
  fi
  wait "$caller"
  code=$?
  caller=
  return 0
}

# A TMPDIR that is not there: every mkdir inside it fails with ENOENT, which
# lock_acquire cannot tell apart from "someone else holds the lock".
root=$(mktemp -d)
missing=$root/not-there
acquire_for "$missing" 5 ||
  fail "lock_acquire spun forever when TMPDIR ($missing) does not exist; a fixture script would hang here with nothing printed"
cat "$logs/err"
if [ "$code" -ne 0 ]; then
  grep -q -- "$name.lock" "$logs/err" ||
    fail "lock_acquire exited $code without naming the directory it could not use"
fi
chmod 700 "$root"; rm -rf "$root"; root=

# A lock left behind in a directory the caller cannot write: a takeover cannot
# remove it, so the lock can be neither removed nor created.
root=$(mktemp -d)
mkdir "$root/$name.lock"
printf '%s\n' 99999999 >"$root/$name.lock/pid" # a pid no process has
printf '%s\n' 0 >"$root/$name.lock/at"
chmod 500 "$root"
acquire_for "$root" 5 ||
  fail "lock_acquire spun forever on a lock directory it could not remove ($root/$name.lock); a fixture script would hang here after one 'taking it' line"
cat "$logs/err"
if [ "$code" -ne 0 ]; then
  grep -q -- "$name.lock" "$logs/err" ||
    fail "lock_acquire exited $code without naming the directory it could not use"
  [ "$(cat "$root/$name.lock/pid" 2>/dev/null)" = 99999999 ] ||
    fail "the failed takeover destroyed the lock's pid before reporting"
  [ -f "$root/$name.lock/at" ] ||
    fail "the failed takeover destroyed the lock's at file before reporting"
fi
chmod 700 "$root"; rm -rf "$root"; root=

# A leftover lock with no pid, in a directory the caller cannot write, must be
# reported at once rather than waiting out the one-minute window first.
root=$(mktemp -d)
mkdir "$root/$name.lock"
printf '%s\n' 0 >"$root/$name.lock/at"
chmod 500 "$root"
acquire_for "$root" 5 ||
  fail "lock_acquire spun forever on a lock with no pid it could not remove ($root/$name.lock); a fixture script would hang here after a minute"
cat "$logs/err"
if [ "$code" -ne 0 ]; then
  grep -q -- "$name.lock" "$logs/err" ||
    fail "lock_acquire exited $code without naming the directory it could not use"
  [ -f "$root/$name.lock/at" ] ||
    fail "the failed takeover destroyed the lock's at file before reporting"
fi
chmod 700 "$root"; rm -rf "$root"; root=

echo "ok: lock_acquire ends instead of spinning when its lock directory cannot be created or removed"
