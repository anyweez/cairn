#!/usr/bin/env bash
# A rendezvous lock for the fixture scripts. Each of them checks for a container
# and then creates it, which is a check-then-act: two callers that start
# together both see none, both run docker under the same name, and Docker fails
# one of them with a name conflict instead of letting it reuse the other's
# container. Serialise that with a lock directory, so exactly one caller is
# inside the check and the creation at a time.
#
# A directory under ${TMPDIR:-/tmp} is the lock: mkdir succeeds for one caller
# and fails for the rest, so it needs nothing beyond what macOS and Linux both
# ship. flock is util-linux-only and is not on a default macOS PATH, which
# would make every one of these scripts exit 127 there.
#
#   . scripts/lib-lock.sh
#   lock_acquire cairn-test-postgres
#   ...the check-then-act...
#   lock_release
#
# The holder's PID and the time it took the lock are written inside, so a
# caller killed while holding it does not wedge the fixture: a lock whose PID is
# gone, or that has been held for more than a minute, is taken over. An EXIT
# trap releases the lock when the critical section fails or the script is
# interrupted.

_lock_dir=
_lock_held=

lock_acquire() {
  local dir=${TMPDIR:-/tmp}/$1.lock
  local pid at now ref seen=
  while ! mkdir "$dir" 2>/dev/null; do
    now=$(date +%s)
    pid=$(cat "$dir/pid" 2>/dev/null || true)
    at=$(cat "$dir/at" 2>/dev/null || true)
    ref=$at
    if [ -z "$ref" ]; then
      # A holder killed between mkdir and recording itself leaves no time to
      # age, so age the lock from when this caller first saw it empty.
      [ -n "$seen" ] || seen=$now
      ref=$seen
    else
      seen=
    fi
    if [ -n "$pid" ] && ! kill -0 "$pid" 2>/dev/null; then
      echo "lock $dir: holder $pid is gone, taking it" >&2
      rm -rf "$dir"
    elif [ "$((now - ref))" -gt 60 ]; then
      echo "lock $dir: held for more than 60s, taking it" >&2
      rm -rf "$dir"
    fi
    sleep 0.1
  done
  printf '%s\n' "$$" >"$dir/pid"
  date +%s >"$dir/at"
  _lock_dir=$dir
  _lock_held=1
  trap lock_release EXIT
}

lock_release() {
  if [ -n "${_lock_held:-}" ]; then
    rm -rf "$_lock_dir"
    _lock_held=
    _lock_dir=
  fi
}
