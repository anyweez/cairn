---
name: cairn-release
description: Cut a cairn release — compute the next semantic version from conventional commits, open a release PR with the regenerated CHANGELOG.md, and after approval merge it and push the vX.Y.Z tag that publishes the GitHub Release. Use when the user says "release cairn", "cut a release", "tag a version", "ship v0.x", or "/cairn-release".
disable-model-invocation: true
---

# Cut a cairn release

Only a person cuts a release, in an interactive session where they invoked
this skill themselves. In a factory run, or any session without a person
answering, stop: don't run any step below.

`docs/RELEASING.md` is the procedure and the versioning policy; read it
first. This skill runs that procedure and adds the checks and approval stops
around it. It never merges, tags or pushes a tag without the user saying yes
to that specific step.

## 1. Preflight — stop on any failure

Run from the main checkout, read-only:

```sh
git fetch origin main --tags
git status --porcelain                      # must be empty
git rev-parse HEAD origin/main              # must match, or `git pull --ff-only` first
cz version                                  # must be 4.19.2, the version CI pins
gh run list --branch main --workflow CI --limit 1 --json headSha,conclusion
```

The last CI run on main must be for `origin/main`'s commit and have
`"conclusion":"success"`. If it's still running, wait for it; if it failed,
stop and report.

## 2. What would ship

```sh
cz bump --get-next --yes
```

- Exit 21 (`NO_COMMITS_TO_BUMP`): nothing since the last tag warrants a
  release. Say so and stop.
- Otherwise it prints the next version, `X.Y.Z`. The tag is `vX.Y.Z`.

Render the notes with
`cz changelog --dry-run --unreleased-version vX.Y.Z | awk '/^## /{n++} n==1'`
and show the user:

- the last tag (`git describe --tags --abbrev=0`, or "none" for the first
  release) and the proposed version, with the reason for the bump (breaking,
  feat, or fix/perf/refactor);
- the notes;
- any commit since the last tag whose subject would read badly in release
  notes. A subject can't be fixed without rewriting main, so only flag it.

If the version would be **v1.0.0 or a new major**, stop and confirm it
explicitly: `major_version_zero` makes this impossible by accident, so seeing
it means `.cz.toml` changed.

Ask: **"Open the release PR for vX.Y.Z?"** Stop if the answer isn't yes.

## 3. Release PR

Follow docs/RELEASING.md step 2 in `.worktrees/release-vX.Y.Z` on branch
`release/vX.Y.Z`. Then:

```sh
cz check --rev-range origin/main..HEAD
```

The PR body lists the version, the bump reason and the notes, and ends with
the attribution line the session's instructions give for pull requests. Report
the PR URL.

## 4. Merge — needs approval

Wait for the PR's CI (`gh pr checks <n> --watch`). Then check that main
hasn't moved since the branch was cut:

```sh
git fetch origin main
git rev-parse origin/main                 # must equal the branch's base
git merge-base origin/main release/vX.Y.Z
```

If main moved, regenerate the changelog on the branch (step 3 again), push,
and show the user the new notes before going on.

Ask: **"CI passed. Merge the vX.Y.Z release PR?"** On yes:
`gh pr merge <n> --merge --delete-branch`.

## 5. Tag — needs approval

```sh
git fetch origin main
git log -1 --format='%H %s' origin/main   # must be the release PR's merge commit
```

Ask: **"Tag <sha> as vX.Y.Z and push the tag? This publishes the version."**
A pushed tag is permanent in practice: the Go module proxy caches it, and
moving it breaks every checksum. On yes:

```sh
git tag -a vX.Y.Z -m vX.Y.Z origin/main
git push origin vX.Y.Z
```

## 6. Confirm

```sh
gh run list --workflow Release --limit 1          # wait for success
gh release view vX.Y.Z
GOPROXY=https://proxy.golang.org GOFLAGS=-mod=mod go list -m github.com/anyweez/cairn@vX.Y.Z
```

The last line fetches the version through the public proxy, which only works
while the repository is public. Remove the release worktree
(`git worktree remove .worktrees/release-vX.Y.Z`) and report the release URL
and the line that pins the version:
`go get github.com/anyweez/cairn@vX.Y.Z`.

## If something goes wrong

- **Release workflow failed:** the tag still stands and the module version
  works. Fix the workflow on main, then publish the notes by hand with
  `gh release create vX.Y.Z --verify-tag --notes-file <notes>`.
- **Wrong commit tagged, not yet pushed:** `git tag -d vX.Y.Z` and redo
  step 5.
- **Wrong commit tagged and pushed:** don't move or delete the tag. Fix
  forward with the next patch version, and tell the user.
