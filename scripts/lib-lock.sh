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
# The holder's PID is written inside, along with when it took the lock, so a
# caller killed while holding it does not wedge the fixture: a lock whose PID is
# gone is taken over, and so is one that records no PID and has stayed empty for
# more than a minute, which is the window between mkdir and writing the
# metadata. A recorded PID that is still alive keeps the lock however long it
# takes; a first-time `docker pull` inside `docker run` can exceed a minute.
# An EXIT trap releases the lock when the critical section fails or the script
# is interrupted.
#
# A lock is identified by the directory, not by its name: the name can be
# reused as soon as a lock is released, so a caller that reads a stale PID and
# then acts on the name could act on a newer lock that took the name in
# between. A takeover therefore claims the lock it inspected with an atomic
# mkdir inside it, and only then moves it, and lock_release removes the
# directory it created rather than whatever the name holds when it is called.

_lock_dir=
_lock_held=
_lock_ino=

# The inode names one lock directory even after its name is reused, so it is
# what a caller compares to tell the lock it inspected from a newer one.
_lock_inode() {
  local listing
  listing=$(ls -di "$1" 2>/dev/null) || true
  printf '%s\n' "${listing%% *}"
}

# A stale lock is moved aside before it is deleted. Renaming is one step, so
# the lock name is free even if the moved directory cannot be deleted, and a
# failed move leaves the pid and at files where the takeover logic reads them.
#
# _lock_takeover DIR PID INODE returns 0 when DIR was still the stale lock and
# is now gone, 2 when another caller is taking it over and this one should
# wait, and 1 when the directory cannot be moved aside at all.
_lock_takeover() {
  local dir=$1 pid=$2 inode=$3 stale="$1.stale.$$" status=0
  _lock_claim "$dir" "$pid" "$inode" || status=$?
  [ "$status" -eq 0 ] || return "$status"
  if ! mv "$dir" "$stale" 2>/dev/null; then
    _lock_unclaim "$dir"
    # A move that failed with the directory still there cannot succeed later,
    # unless the lock went away in between, which the next mkdir settles.
    [ -d "$dir" ] || return 2
    return 1
  fi
  rm -rf "$stale" 2>/dev/null || true
  return 0
}

# Take the right to move DIR aside, but only while it is still the lock the
# caller inspected: the same directory (inode) recording the same PID.
#
# mkdir is atomic, so one caller at a time holds the claim. The claim sits
# inside the directory it claims, so claiming a lock that was replaced in
# between is detected by the re-check below and given back untouched.
#
# Returns 0 when this caller may move DIR, 2 when another caller is part-way
# through the same takeover (wait for it, and the live lock it leaves behind),
# and 1 when the claim cannot be taken or cleared, so DIR cannot be taken over.
_lock_claim() {
  local dir=$1 pid=$2 inode=$3 claim=$1/claim owner
  while :; do
    if mkdir "$claim" 2>/dev/null; then
      break
    fi
    [ -d "$dir" ] || return 2
    owner=$(cat "$claim/pid" 2>/dev/null || true)
    if [ -n "$owner" ] && kill -0 "$owner" 2>/dev/null; then
      return 2
    fi
    # Nothing to clear means the claim cannot be created at all, in a directory
    # the caller cannot write; report that rather than spinning on it.
    [ -e "$claim" ] || return 1
    # A taker killed while taking the lock over leaves a claim that nothing
    # would ever clear, so a claim whose taker is gone is removed and retried.
    if ! rm -rf "$claim" 2>/dev/null; then
      return 1
    fi
  done
  printf '%s\n' "$$" >"$claim/pid" 2>/dev/null || true
  if [ "$(_lock_inode "$dir")" = "$inode" ] &&
     [ "$(cat "$dir/pid" 2>/dev/null || true)" = "$pid" ]; then
    return 0
  fi
  _lock_unclaim "$dir"
  return 2
}

# Give back a claim, and only a claim this caller wrote.
_lock_unclaim() {
  [ "$(cat "$1/claim/pid" 2>/dev/null || true)" = "$$" ] || return 0
  rm -rf "$1/claim" 2>/dev/null || true
}

# An empty lock is aged before takeover, so this checks now whether that
# takeover could ever work. A lock left in a directory the caller cannot write
# is reported in seconds instead of after the one-minute window.
_lock_takeover_possible() {
  local parent=${1%/*}
  [ -w "$parent" ] && [ -x "$parent" ] || return 1
  # A sticky parent such as /tmp only lets its owner move a directory out of
  # it, so a leftover from another user cannot be taken over there.
  if [ -k "$parent" ] && [ ! -O "$1" ]; then
    return 1
  fi
  return 0
}

lock_acquire() {
  local dir=${TMPDIR:-/tmp}/$1.lock
  local pid now seen= missing=0 inode reason stale
  while ! mkdir "$dir" 2>/dev/null; do
    if [ ! -d "$dir" ]; then
      # mkdir failed with no lock directory to inspect. Either the directory
      # holding the lock is unusable, or another caller released the lock
      # between the mkdir and here. Report the former; retry the latter.
      missing=$((missing + 1))
      if [ -e "$dir" ] || [ -L "$dir" ] || [ ! -d "${dir%/*}" ] ||
         [ ! -w "${dir%/*}" ] || [ ! -x "${dir%/*}" ] ||
         [ "$missing" -gt 10 ]; then
        echo "lock $dir: cannot create the lock directory" >&2
        return 1
      fi
      sleep 0.1
      continue
    fi
    missing=0
    now=$(date +%s)
    pid=$(cat "$dir/pid" 2>/dev/null || true)
    stale=
    reason=
    if [ -n "$pid" ]; then
      # A live holder keeps the lock however long it takes. Taking it would
      # let a second caller into the check-then-act and reintroduce the
      # name-conflict race this lock exists to prevent.
      seen=
      if ! kill -0 "$pid" 2>/dev/null; then
        stale=1
        reason="holder $pid is gone"
      fi
    else
      # A holder killed between mkdir and recording itself leaves no PID to
      # check, so age the lock from when this caller first saw it empty.
      [ -n "$seen" ] || seen=$now
      if ! _lock_takeover_possible "$dir"; then
        echo "lock $dir: cannot remove the stale lock directory; remove it by hand" >&2
        return 1
      fi
      if [ "$((now - seen))" -gt 60 ]; then
        stale=1
        reason="no holder recorded for more than 60s"
      fi
    fi
    if [ -n "$stale" ]; then
      inode=$(_lock_inode "$dir")
      if _lock_takeover "$dir" "$pid" "$inode"; then
        echo "lock $dir: $reason, taking it" >&2
      else
        case $? in
          2) ;; # another caller is taking it over; wait for the lock it leaves
          *)
            echo "lock $dir: cannot remove the stale lock directory; remove it by hand" >&2
            return 1
            ;;
        esac
      fi
    fi
    sleep 0.1
  done
  _lock_ino=$(_lock_inode "$dir")
  printf '%s\n' "$$" >"$dir/pid"
  date +%s >"$dir/at"
  _lock_dir=$dir
  _lock_held=1
  trap lock_release EXIT
}

lock_release() {
  if [ -n "${_lock_held:-}" ]; then
    # Remove the lock this caller created, not whatever the name holds now: a
    # lock taken over while this caller worked belongs to its new holder.
    if [ "$(_lock_inode "$_lock_dir")" = "${_lock_ino:-}" ] &&
       [ "$(cat "$_lock_dir/pid" 2>/dev/null || true)" = "$$" ]; then
      rm -rf "$_lock_dir"
    fi
    _lock_held=
    _lock_dir=
    _lock_ino=
  fi
}
