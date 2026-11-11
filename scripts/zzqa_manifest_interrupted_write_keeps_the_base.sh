#!/usr/bin/env bash
# qa: a cairn-manifest regeneration whose write fails part-way (a full disk, a
# killed process) destroys the spec.manifest.yaml it was writing over. The next
# run with -base -- the documented compatibility check -- then reads the
# truncated file as the base and reports a stored type as removed that is still
# in the code, or fails to parse it at all, and no amount of free space makes
# the check pass again until the file is restored by hand.
#
# The write is built against a tiny tmpfs so the failure is deterministic: the
# tool truncates the existing manifest, writes all the space there is, and
# exits non-zero. Space is freed before the retry, so the only thing left
# broken is the file the failed run destroyed.
#
# Passes once a failed or interrupted write leaves the previous output
# byte-identical, so the retry compares against the real previous manifest.
set -euo pipefail

cd "$(dirname "$0")/.."
root=$(pwd)

bin=$(mktemp -d)/cairn-manifest
go -C cmd/cairn-manifest build -o "$bin" .

work=$(mktemp -d)
trap 'rm -rf "$(dirname "$bin")" "$work"' EXIT

module=$work/module
mkdir -p "$module"
cat >"$module/go.mod" <<EOF
module example.com/good

go 1.26.0

require github.com/anyweez/cairn v0.0.0

replace github.com/anyweez/cairn => $root
EOF
{
	echo 'package good'
	echo
	echo 'import "github.com/anyweez/cairn/codec"'
	echo
	echo 'type Event interface{ isEvent() }'
	echo
	echo 'type Ping struct {'
	echo '	ID string `json:"id"`'
	echo '}'
	echo
	echo 'func (Ping) isEvent() {}'
	echo
	echo 'func Codecs() *codec.Codec[Event] {'
	echo '	c := codec.New[Event]()'
	for i in $(seq 1 120); do
		printf '\tcodec.Register[Ping](c, "good.ping%d", 1)\n' "$i"
	done
	echo '	return c'
	echo '}'
} >"$module/good.go"

# Sanity: the tool reads the module, so a failure below is the write's and not
# the fixture's.
if ! "$bin" -dir "$module" -o - >"$work/good.yaml" || ! grep -q 'good.ping120' "$work/good.yaml"; then
	echo "the manifest tool does not read the fixture module, so this check proves nothing" >&2
	exit 2
fi
if [ "$(wc -c <"$work/good.yaml")" -lt 8192 ]; then
	echo "the fixture manifest is too small to overflow the test filesystem" >&2
	exit 2
fi

# The module outside the namespace is already readable; the tmpfs, the failed
# write and the retry all happen inside one mount namespace.
set +e
unshare -Ur -m bash -s "$bin" "$module" "$work/tiny" <<'INNER'
set -u
bin=$1
module=$2
mountpoint=$3

mkdir -p "$mountpoint"
if ! mount -t tmpfs -o size=16k tmpfs "$mountpoint"; then
	echo "no tmpfs available to make the write fail deterministically" >&2
	exit 2
fi

# Leave less free space than the manifest needs, without filling the whole
# filesystem: the file the tool truncates still fits, the manifest does not.
head -c 8192 /dev/zero >"$mountpoint/filler"
printf 'rules: []\ncodecs: []\ntables: []\n' >"$mountpoint/spec.manifest.yaml"
before=$(wc -c <"$mountpoint/spec.manifest.yaml")

out=$(realpath --relative-to="$module" "$mountpoint")/spec.manifest.yaml
export HOME=/tmp GOCACHE=/tmp/gocache
"$bin" -dir "$module" -o "$out" \
	-base "$mountpoint/spec.manifest.yaml" >"$mountpoint/first.log" 2>&1
first=$?
after=$(wc -c <"$mountpoint/spec.manifest.yaml") || after=0
if [ "$first" -eq 0 ]; then
	echo "the write succeeded, so the filesystem was not full enough for this check" >&2
	exit 2
fi
echo "the failed write replaced a $before byte base with $after bytes"

# Free the space: the condition that made the write fail is gone.
rm -f "$mountpoint/filler"

set +e
"$bin" -dir "$module" -o "$out" \
	-base "$mountpoint/spec.manifest.yaml" >"$mountpoint/second.log" 2>&1
second=$?
set -e
echo "the retry with space available exited $second"
sed 's/^/  /' "$mountpoint/second.log"

if [ "$second" -ne 0 ]; then
	echo "FAIL: the failed write destroyed the previous manifest, so the retry cannot" >&2
	echo "      run the -base check even though the disk has room again" >&2
	exit 1
fi
echo "ok: the previous manifest survived the failed write and the retry passed"
INNER
code=$?
set -e
exit "$code"
