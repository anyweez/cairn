#!/usr/bin/env bash
# A lock_acquire takeover decides from the PID it read and then moves the lock
# by path. Another caller can take the stale lock and replace that path with a
# fresh, live lock in between, so the first caller moves the new holder's lock
# aside and both callers enter the critical section the lock exists to
# serialise. The fixture scripts then run their check-then-act (docker run by
# container name, docker rm -f on a port mismatch) at the same time.
#
# One caller's `mv` is delayed here so the window is open for a known time; the
# lock itself is unmodified. A correct takeover removes or renames only the
# lock it inspected, so the delayed caller either waits for the live holder or
# takes over after it releases, and the two never hold the lock together.
set -u

root=$(cd "$(dirname "$0")/.." && pwd)
tmp=$(mktemp -d)
real_mv=$(command -v mv)
lock_name=zzqa-takeover-$$
lock_dir=$tmp/$lock_name.lock
pids=

cleanup() {
  for p in $pids; do
    kill "$p" 2>/dev/null || true
  done
  rm -rf "$tmp"
}
trap cleanup EXIT

# A stale lock, as a caller killed while holding it leaves behind.
mkdir -p "$lock_dir"
printf '%s\n' 99999999 >"$lock_dir/pid"

# The delayed mv: announce that the takeover is about to happen, give the other
# caller time to replace the lock, then move whatever is at the path now.
mkdir "$tmp/shim"
cat >"$tmp/shim/mv" <<EOF
#!/usr/bin/env bash
if [ ! -e "\$MARKER" ]; then
  : >"\$MARKER"
  sleep 2
fi
exec "$real_mv" "\$@"
EOF
chmod +x "$tmp/shim/mv"

cat >"$tmp/taker.sh" <<'EOF'
. "$ROOT/scripts/lib-lock.sh"
lock_acquire "$LOCK"
# mkdir is the atomic claim of the critical section.
if mkdir "$TMP/critical" 2>/dev/null; then
  rmdir "$TMP/critical"
else
  : >"$TMP/two-holders"
fi
lock_release
EOF

cat >"$tmp/holder.sh" <<'EOF'
. "$ROOT/scripts/lib-lock.sh"
lock_acquire "$LOCK"
if mkdir "$TMP/critical" 2>/dev/null; then
  # Stay inside long enough for the delayed takeover to land.
  : >"$TMP/holder-entered"
  sleep 4
  rmdir "$TMP/critical"
else
  : >"$TMP/two-holders"
fi
lock_release
EOF

failed() {
  echo "FAIL: $*" >&2
  exit 1
}

# The taker reads the stale PID and reaches its delayed mv.
(
  TMPDIR=$tmp ROOT=$root LOCK=$lock_name TMP=$tmp MARKER=$tmp/taker-in-mv \
    PATH=$tmp/shim:$PATH bash "$tmp/taker.sh"
  echo $? >"$tmp/taker.status"
) &
taker=$!
pids="$pids $taker"
for _ in $(seq 1 50); do
  [ -e "$tmp/taker-in-mv" ] && break
  sleep 0.1
done

# The holder takes over the still-stale lock, installs its own live lock and
# enters the critical section before the taker's mv lands.
(
  TMPDIR=$tmp ROOT=$root LOCK=$lock_name TMP=$tmp bash "$tmp/holder.sh"
  echo $? >"$tmp/holder.status"
) &
holder=$!
pids="$pids $holder"

# Neither work depends on a timer beyond the two sleeps above, but a fix that
# makes the taker wait forever must fail rather than hang the run.
for _ in $(seq 1 200); do
  [ -e "$tmp/taker.status" ] && [ -e "$tmp/holder.status" ] && break
  sleep 0.1
done
[ -e "$tmp/taker.status" ] || failed "the taker never returned from lock_acquire"
[ -e "$tmp/holder.status" ] || failed "the holder never returned from lock_acquire"
[ "$(cat "$tmp/taker.status")" = 0 ] || failed "the taker's lock_acquire failed"
[ "$(cat "$tmp/holder.status")" = 0 ] || failed "the holder's lock_acquire failed"
[ -e "$tmp/holder-entered" ] || failed "the holder never entered the critical section; the test proved nothing"

if [ -e "$tmp/two-holders" ]; then
  failed "two callers held the lock at once: the delayed takeover moved the live holder's lock"
fi
echo "PASS: the delayed takeover did not put two callers inside the lock"
