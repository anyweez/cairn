#!/usr/bin/env bash
# zzqa: the release procedure's "nothing to ship" signal is the one commitizen
# actually exits with, in the state a release session starts in.
#
# docs/RELEASING.md step 1 and .claude/skills/cairn-release/SKILL.md step 2 both
# say an exit of 21 with `NO_COMMITS_TO_BUMP` means "nothing since the last tag
# warrants a release", and the skill's only branch for a no-release tree is that
# code. Commitizen 4.19.2, the version CI pins, has two such codes:
#
#   * no commits since the last tag   -> exit 3,  [NO_COMMITS_FOUND]
#   * commits, none of them bumpable  -> exit 21, [NO_COMMITS_TO_BUMP]
#
# The first is what an up-to-date main looks like right after a release, and it
# is the state the procedure tells a session to start from, so following the
# procedure gives an error the skill does not cover: the session cannot tell
# "nothing to ship" from "the release tooling broke".
#
# This measures both codes with the pinned commitizen and fails while the two
# files that state the procedure do not name the code the no-commit tree exits
# with. It passes when each names both, so the fix cannot simply swap one code
# for the other.
set -euo pipefail
cd "$(dirname "$0")/.."

work=$(mktemp -d)
trap 'rm -rf "$work"' EXIT

# commitizen 4.19.2 into a throwaway venv. The image's python has no pip, so
# fall back to get-pip when the venv cannot bootstrap itself, as it cannot here.
if ! python3 -m venv "$work/venv" >/dev/null 2>&1; then
  python3 -m venv --without-pip "$work/venv" >/dev/null
  curl -sS -o "$work/get-pip.py" https://bootstrap.pypa.io/get-pip.py
  "$work/venv/bin/python" "$work/get-pip.py" --quiet
fi
"$work/venv/bin/pip" install --quiet 'commitizen==4.19.2' >/dev/null
cz="$work/venv/bin/cz"
echo "commitizen $("$cz" version)"

# A released repository configured the way this one is.
repo=$work/repo
mkdir "$repo"
git -C "$repo" init -q -b main
git -C "$repo" config user.email zzqa@example.com
git -C "$repo" config user.name zzqa
cp .cz.toml "$repo/.cz.toml"
printf 'released\n' > "$repo/a.txt"
git -C "$repo" add -A
git -C "$repo" commit -qm 'feat: the released work'
git -C "$repo" tag -a v0.5.0 -m v0.5.0

bump() { (cd "$repo" && "$cz" bump --get-next --yes 2>&1); }

# The state a release session starts in on an up-to-date main: the tag is at
# HEAD, so nothing has landed since the release.
set +e
nothing=$(bump); nothing_code=$?
set -e
echo "no commits since v0.5.0: exit $nothing_code: $(printf '%s' "$nothing" | head -1)"

# Commits since the tag, none of them bumpable.
printf 'tidied\n' > "$repo/b.txt"
git -C "$repo" add -A
git -C "$repo" commit -qm 'chore: tidy up'
set +e
unbumpable=$(bump); unbumpable_code=$?
set -e
echo "a chore since v0.5.0:   exit $unbumpable_code: $(printf '%s' "$unbumpable" | head -1)"

status=0
if [ "$nothing_code" -ne 3 ] || ! printf '%s' "$nothing" | grep -q 'NO_COMMITS_FOUND'; then
  echo "FAIL: a tree with no commits since the tag should exit 3 with NO_COMMITS_FOUND" >&2
  status=1
fi
if [ "$unbumpable_code" -ne 21 ] || ! printf '%s' "$unbumpable" | grep -q 'NO_COMMITS_TO_BUMP'; then
  echo "FAIL: commits that are not bumpable should exit 21 with NO_COMMITS_TO_BUMP" >&2
  status=1
fi

# Both statements of the procedure have to name both codes.
for f in docs/RELEASING.md .claude/skills/cairn-release/SKILL.md; do
  for word in NO_COMMITS_FOUND NO_COMMITS_TO_BUMP; do
    if ! grep -q "$word" "$f"; then
      echo "FAIL: $f does not name $word, so it does not tell a session what exit $nothing_code means" >&2
      status=1
    fi
  done
done

if [ "$status" -ne 0 ]; then
  echo >&2
  echo "FAIL: the procedure names only exit 21 for 'nothing to ship', but a main" >&2
  echo "      with no commits since the last tag exits $nothing_code; the release" >&2
  echo "      skill has no branch for it, so a session starting there reports a" >&2
  echo "      failure instead of 'nothing to ship'." >&2
  exit 1
fi

echo "ok: both no-release exit codes are what the procedure says they are"
