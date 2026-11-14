#!/usr/bin/env bash
# qa: two invocations of a fixture script that start together are documented to
# both succeed — "The fixture is safe to call concurrently" in factory.yaml, and
# scripts/test-postgres-race.sh drives it. The lock that makes the check-then-act
# safe serialises them, but a caller whose `cat "$dir/pid"` lands after the
# holder has released the lock (the directory is gone) reads an empty pid, takes
# the "holder died between mkdir and recording itself" branch, and
# _lock_takeover_possible refuses a directory that is not there:
#
#   lock /tmp/cairn-test-postgres.lock: cannot remove the stale lock directory;
#   remove it by hand
#
# It then returns 1, so one of the two fixture runs aborts — with an instruction
# to remove a lock directory that has already been removed. The window is small
# but real: scripts/qa-test-pgbouncer-race.sh failed on round 5 of 5 twice on
# this tree, and pass-the-holder-its-pid traces show the directory released
# between the `[ ! -d ]` test and the read.
#
# This test holds that window open deliberately rather than racing for it: the
# acquiring caller's `cat` waits until the live holder has released the lock,
# which is what the untraced race does when its timing lands that way. The real
# scripts/lib-lock.sh runs unmodified; only the acquirer's PATH is shimmed.
#
# It passes once a caller that finds the lock released mid-check acquires it,
# which is what happens on any later iteration of the loop: mkdir succeeds.
#
# Needs a sticky lock parent, which is the default: TMPDIR is unset, so the lock
# is /tmp/<name>.lock and /tmp is world-writable with the sticky bit, as it is on
# Linux and macOS. A caller cannot move a directory it does not own out of such a
# parent, which is why _lock_takeover_possible refuses it.
set -uo pipefail

here=$(cd "$(dirname "$0")" && pwd)
real_cat=$(command -v cat) || { echo "no cat on PATH" >&2; exit 2; }

# The default lock parent, the one the fixture scripts use when TMPDIR is unset.
if [ -n "${TMPDIR:-}" ] || [ ! -d /tmp ] || [ ! -k /tmp ]; then
  echo "this check needs the default sticky lock parent /tmp; TMPDIR=${TMPDIR:-<unset>}" >&2
  exit 2
fi

tmp=$(mktemp -d)
name=zzqa-lock-released-$$
lock=/tmp/$name.lock
holder=
shim=

cleanup() {
  [ -z "$holder" ] || kill "$holder" 2>/dev/null || true
  rm -rf "$lock" "$tmp"
}
trap cleanup EXIT

# The acquirer's cat waits for the holder to release the lock first, so the read
# of the pid file is the one that lands after the release. Everything else is
# exec'd through to the real cat.
mkdir -p "$tmp/bin"
cat >"$tmp/bin/cat" <<'SHIM'
#!/usr/bin/env bash
case ${1:-} in
  *.lock/pid)
    lock=${1%/pid}
    for _ in $(seq 1 200); do
      [ -d "$lock" ] || break
      sleep 0.05
    done
    ;;
esac
exec "$REAL_CAT" "$@"
SHIM
chmod +x "$tmp/bin/cat"

# A live holder, released in the middle of the acquirer's check: it holds the
# lock with its own PID recorded, then removes the directory as lock_release
# does, 0.5s in.
bash -c 'd=$1; mkdir "$d" || exit 1; printf "%s\n" "$$" >"$d/pid";
         printf "%s\n" 0 >"$d/at"; sleep 0.5; rm -rf "$d"' _ "$lock" &
holder=$!
for _ in $(seq 1 100); do
  [ -f "$lock/pid" ] && break
  sleep 0.02
done
if [ ! -f "$lock/pid" ]; then
  echo "the holder never took the lock, so this check proves nothing" >&2
  exit 2
fi

acquirer_log=$tmp/acquirer.log
# The fixture scripts run with set -euo pipefail, so a lock_acquire that returns
# non-zero ends the script; the acquirer is run the same way.
PATH="$tmp/bin:$PATH" REAL_CAT="$real_cat" \
  bash -c 'set -euo pipefail; . "$1"; lock_acquire "$2"; echo acquired' _ "$here/lib-lock.sh" "$name" \
  >"$acquirer_log" 2>&1
status=$?
wait "$holder" 2>/dev/null
holder=

if [ "$status" -ne 0 ]; then
  sed 's/^/  /' "$acquirer_log" >&2
  cat >&2 <<'MSG'
FAIL: a caller that found the lock released while it was checking it returned
      non-zero, so one of two concurrent fixture runs aborts. Nothing is left to
      remove by hand, and the next run of the same script would not see the lock
      either.
MSG
  exit 1
fi
grep -qx acquired "$acquirer_log" ||
  { echo "FAIL: the caller did not acquire the lock:" >&2; sed 's/^/  /' "$acquirer_log" >&2; exit 1; }
[ -d "$lock" ] && { echo "FAIL: the acquired lock was not released on exit" >&2; exit 1; }

echo "ok: a caller acquires the lock when the holder releases it mid-check"
