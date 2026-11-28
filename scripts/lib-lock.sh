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
# takes -- a first-time `docker pull` inside `docker run` can exceed a minute --
# but only while it can still be the process that took the lock, because a PID
# outlives the process it names: a holder killed while holding the lock leaves
# its number to be handed out again, and the unrelated process that gets it is
# alive forever as far as `kill -0` is concerned (see _lock_pid_reused).
# A caller whose read straddles a release sees no PID and no directory: it
# retries and acquires, rather than reporting a stale lock that is not there.
# An EXIT trap releases the lock when the critical section fails or the script
# is interrupted.
#
# A lock is identified by the directory, not by its name: the name can be
# reused as soon as a lock is released, so a caller that reads a stale PID and
# then acts on the name could act on a newer lock that took the name in
# between. A takeover therefore claims the lock it inspected with an atomic
# mkdir inside it, and only then moves it, and lock_release removes the
# directory it created rather than whatever the name holds when it is called.
# A dead claim is cleared by renaming it aside, so several callers recovering
# the same one do not race each other's delete and recreate of that path, and
# what was moved is checked before it is deleted: a claim another taker has
# created and recorded in the meantime is not this caller's to clear.

_lock_dir=
_lock_held=
_lock_ino=
_lock_claim_seen=
_lock_claim_ino=

# The inode names one lock directory even after its name is reused, so it is
# what a caller compares to tell the lock it inspected from a newer one.
_lock_inode() {
  local listing
  listing=$(ls -di "$1" 2>/dev/null) || true
  printf '%s\n' "${listing%% *}"
}

# Whether PID names a process at all. `kill -0` fails for a process that
# exists but belongs to another user, so a live holder in a shared TMPDIR would
# read as gone; ps answers for a process this user may not signal.
_lock_process_alive() {
  kill -0 "$1" 2>/dev/null || ps -p "$1" >/dev/null 2>&1
}

# The seconds a process has been running, from ps's POSIX etime field
# ([[dd-]hh:]mm:ss). ps cannot report a process that is gone, and prints
# nothing here either, so a caller never mistakes a missing process for a young
# one.
_lock_process_age() {
  local e days hms h= m= s= age
  e=$(ps -p "$1" -o etime= 2>/dev/null) || return 0
  e=${e#"${e%%[![:space:]]*}"}
  e=${e%"${e##*[![:space:]]}"}
  [ -n "$e" ] || return 0
  case $e in
    *-*) days=${e%%-*}; hms=${e#*-} ;;
    *) days=0; hms=$e ;;
  esac
  case $hms in
    *:*:*) h=${hms%%:*}; hms=${hms#*:}; m=${hms%%:*}; s=${hms#*:} ;;
    *:*) h=0; m=${hms%%:*}; s=${hms#*:} ;;
    *) return 0 ;;
  esac
  case $days$h$m$s in
    '' | *[!0-9]*) return 0 ;;
  esac
  # ps zero-pads each field, and bash reads 08 and 09 as bad octal.
  age=$((10#$days * 86400 + 10#$h * 3600 + 10#$m * 60 + 10#$s))
  printf '%s\n' "$age"
}

# Whether the PID a lock records cannot be the process that took it. A PID is
# unique only while its process lives: a holder killed while holding the lock
# leaves the number to be handed out again, and the process that gets it is
# alive but has never seen this lock, so `kill -0` alone would keep the lock
# from every later caller forever. The holder records when it took the lock and
# started no later than that, so a recorded PID whose process has been running
# for less time than the lock has been held is not the holder. The comparison
# allows two seconds, because ps reports whole seconds.
#
# A lock whose recorded time is missing or unreadable is left alone: nobody can
# tell it from a live holder's, and taking a live holder's lock is the failure
# this lock exists to prevent.
_lock_pid_reused() {
  local pid=$1 since=$2 age
  case ${since:-} in
    '' | *[!0-9]*) return 1 ;;
  esac
  age=$(_lock_process_age "$pid")
  [ -n "$age" ] || return 1
  [ "$((age + 2))" -lt "$(( $(date +%s) - since ))" ]
}

# A stale lock is moved aside before it is deleted. Renaming is one step, so
# the lock name is free even if the moved directory cannot be deleted, and a
# failed move leaves the pid and at files where the takeover logic reads them.
#
# _lock_takeover DIR PID INODE returns 0 when DIR was still the stale lock and
# is now gone, 2 when another caller is taking it over or DIR has gone, and 1
# when the directory cannot be moved aside at all.
_lock_takeover() {
  local dir=$1 pid=$2 inode=$3 stale="$1.stale.$$" status=0
  _lock_claim "$dir" "$pid" "$inode" || status=$?
  [ "$status" -eq 0 ] || return "$status"
  # Move only the lock this caller claimed. A caller that found this claim dead
  # can clear it while this one prepares, so the claim is checked here and
  # again after the move: a takeover whose claim did not travel with the lock
  # must not go on to create one.
  if [ "$(cat "$dir/claim/pid" 2>/dev/null || true)" != "$$" ]; then
    _lock_unclaim "$dir"
    return 2
  fi
  if ! mv "$dir" "$stale" 2>/dev/null; then
    _lock_unclaim "$dir"
    # A move that failed with the directory still there cannot succeed later,
    # unless the lock went away in between, which the next mkdir settles.
    [ -d "$dir" ] || return 2
    return 1
  fi
  if [ "$(cat "$stale/claim/pid" 2>/dev/null || true)" != "$$" ]; then
    # Another caller cleared the claim between the check and the move, so this
    # caller never won the takeover. The stale lock has left the name and
    # nothing was created at it, so the next mkdir settles who takes it.
    rm -rf "$stale" 2>/dev/null || true
    return 2
  fi
  rm -rf "$stale" 2>/dev/null || true
  return 0
}

# Clear a stale claim or a released lock directory. Renaming is atomic, so of
# several callers that found the same one exactly one moves it aside; the rest
# see it already gone and retry. A recursive delete would race another caller's
# mkdir inside the same path, and could fail or remove a claim just taken.
#
# With PID given, the moved directory must record that PID in its own pid file.
# A taker can clear the dead claim, create its own and record itself between
# this caller's read and its move, and clearing by path would take that live
# claim away and let two callers move the lock. Such a claim is deleted rather
# than put back -- its owner checks that the claim is still at the path before
# moving the lock, and restoring it could land it on a fresh empty claim -- and
# the caller must not go on to take the lock.
#
# Returns 0 when the directory is gone, 2 when PID was given and the directory
# moved aside records another PID or none, and 1 when it is still there.
_lock_drop_dir() {
  local target=$1 moved="$1.stale.$$" expect= found= check=
  if [ "$#" -gt 1 ]; then
    check=1
    expect=$2
  fi
  rm -rf "$moved" 2>/dev/null || true
  if ! mv "$target" "$moved" 2>/dev/null; then
    [ -e "$target" ] && return 1
    return 0
  fi
  if [ -n "$check" ]; then
    found=$(cat "$moved/pid" 2>/dev/null || true)
    if [ "$found" != "$expect" ]; then
      rm -rf "$moved" 2>/dev/null || true
      return 2
    fi
  fi
  rm -rf "$moved" 2>/dev/null || true
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
  local dir=$1 pid=$2 inode=$3 claim=$1/claim owner now claim_ino status
  while :; do
    if mkdir "$claim" 2>/dev/null; then
      _lock_claim_seen=
      _lock_claim_ino=
      break
    fi
    [ -d "$dir" ] || return 2
    owner=$(cat "$claim/pid" 2>/dev/null || true)
    if [ -n "$owner" ]; then
      _lock_claim_seen=
      _lock_claim_ino=
      # A claim lives for milliseconds, so its taker is alive unless it was
      # killed part-way; a recorded PID that belonged to somebody else is the
      # same reused number, and waiting on it would leave the claim forever.
      if _lock_process_alive "$owner" &&
         ! _lock_pid_reused "$owner" "$(cat "$claim/at" 2>/dev/null || true)"; then
        return 2
      fi
      # A taker killed while taking the lock over leaves a claim that nothing
      # would ever clear, so a claim whose taker is gone is removed and retried.
      # The claim is identified after it has been moved aside: a contender can
      # clear the dead one, create its own and record itself in between, and
      # that live claim is not this caller's to delete.
      status=0
      _lock_drop_dir "$claim" "$owner" || status=$?
      case $status in
        0) continue ;;
        2) return 2 ;;
      esac
      # Still there in a directory this caller can write: another taker cleared
      # and recreated it while this one looked, so inspect the lock again
      # rather than report a claim that cannot be cleared.
      [ -d "$dir" ] || return 2
      if [ -w "$dir" ] && [ -x "$dir" ]; then
        return 2
      fi
      return 1
    fi
    # Nothing to clear is either a claim another caller just cleared, or one
    # that cannot be created at all in a directory the caller cannot write.
    # Only the latter is a failure; let the caller retry the former.
    if [ ! -e "$claim" ]; then
      # A directory that has gone since the test above was taken over, not
      # left behind; the next mkdir settles which.
      [ -d "$dir" ] || return 2
      if [ -w "$dir" ] && [ -x "$dir" ]; then
        return 2
      fi
      _lock_claim_seen=
      _lock_claim_ino=
      return 1
    fi
    # A claim with no PID is also what a live taker looks like between its
    # mkdir of the claim and writing its PID inside it. Clearing that at once
    # would let this caller take the claim over and both of them move the lock,
    # so an empty claim is held and only becomes stale once it has been seen
    # empty for longer than that window.
    now=$(date +%s)
    claim_ino=$(_lock_inode "$claim")
    if [ "$claim_ino" != "$_lock_claim_ino" ]; then
      _lock_claim_ino=$claim_ino
      _lock_claim_seen=$now
    fi
    if [ "$((now - _lock_claim_seen))" -le 5 ]; then
      return 2
    fi
    # A taker killed before it recorded itself leaves a claim nothing would
    # ever clear, so one that has stayed empty past the window is removed. It
    # must still be empty once it has been moved aside: a taker that recorded
    # itself in the meantime is alive and keeps its claim.
    status=0
    _lock_drop_dir "$claim" "" || status=$?
    _lock_claim_seen=
    _lock_claim_ino=
    case $status in
      0) continue ;;
      2) return 2 ;;
    esac
    [ -d "$dir" ] || return 2
    if [ -w "$dir" ] && [ -x "$dir" ]; then
      return 2
    fi
    return 1
  done
  # The claim's own write can lose a race with another caller clearing it, and
  # the shell reports that redirection failure itself, so silence the group.
  { printf '%s\n' "$$" >"$claim/pid"; date +%s >"$claim/at"; } 2>/dev/null || true
  # Move only while this caller's claim still stands: another caller that
  # cleared it must not leave two callers believing they won the takeover.
  if [ "$(_lock_inode "$dir")" = "$inode" ] &&
     [ "$(cat "$claim/pid" 2>/dev/null || true)" = "$$" ] &&
     [ "$(cat "$dir/pid" 2>/dev/null || true)" = "$pid" ]; then
    return 0
  fi
  _lock_unclaim "$dir"
  return 2
}

# Give back a claim, and only a claim this caller wrote.
_lock_unclaim() {
  [ "$(cat "$1/claim/pid" 2>/dev/null || true)" = "$$" ] || return 0
  _lock_drop_dir "$1/claim"
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
  _lock_claim_seen=
  _lock_claim_ino=
  while ! mkdir "$dir" 2>/dev/null; do
    if [ ! -d "$dir" ]; then
      # mkdir failed with no lock directory to inspect. Anything that is not a
      # lock directory at the name is not a caller that released it, and a
      # parent that cannot be written cannot hold one either: report both at
      # once rather than wait out the window below.
      if [ -e "$dir" ] || [ -L "$dir" ] ||
         [ ! -d "${dir%/*}" ] ||
         [ ! -w "${dir%/*}" ] || [ ! -x "${dir%/*}" ]; then
        echo "lock $dir: cannot create the lock directory" >&2
        return 1
      fi
      # Otherwise another caller released the lock between the mkdir and here.
      # Under contention a run of those is ordinary -- every one of them means
      # a caller held or was taking the lock -- so the bound is the same minute
      # an empty lock is aged for, not a handful of turns of the loop.
      missing=$((missing + 1))
      if [ "$missing" -gt 600 ]; then
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
      if ! _lock_process_alive "$pid"; then
        stale=1
        reason="holder $pid is gone"
      elif _lock_pid_reused "$pid" "$(cat "$dir/at" 2>/dev/null || true)"; then
        # The number is alive but belongs to a process that started after the
        # lock was taken, so the holder is gone and its PID was handed on.
        stale=1
        reason="holder $pid is gone; its PID now names another process"
      fi
    else
      # An empty read is also what a caller sees when lock_release has just
      # removed the directory, so a directory that is gone was released, not
      # left behind: retry mkdir rather than take the stale-lock path, and age
      # any directory created next from when it is first seen empty.
      if [ ! -d "$dir" ]; then
        seen=
        continue
      fi
      # A holder killed between mkdir and recording itself leaves no PID to
      # check, so age the lock from when this caller first saw it empty.
      [ -n "$seen" ] || seen=$now
      if ! _lock_takeover_possible "$dir"; then
        # A lock released between the read and here is not there to remove;
        # report only a directory that is still there, and retry otherwise.
        if [ ! -d "$dir" ]; then
          seen=
          continue
        fi
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
          2)
            # Either another caller is taking it over or the lock went away in
            # between: there is nothing to remove by hand, and the next mkdir
            # settles which.
            seen=
            ;;
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
    # lock taken over while this caller worked belongs to its new holder. The
    # rename in _lock_drop_dir keeps a claim another caller is creating inside
    # the directory from racing its removal.
    if [ "$(_lock_inode "$_lock_dir")" = "${_lock_ino:-}" ] &&
       [ "$(cat "$_lock_dir/pid" 2>/dev/null || true)" = "$$" ]; then
      _lock_drop_dir "$_lock_dir"
    fi
    _lock_held=
    _lock_dir=
    _lock_ino=
  fi
}
