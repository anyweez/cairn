# Releasing

A cairn release is a `vX.Y.Z` tag on main. The tag is the Go module version,
so a project pins cairn with `go get github.com/anyweez/cairn@vX.Y.Z`. There
is no version file to edit.

The `cairn-release` skill (`.claude/skills/cairn-release/`) runs the steps
below and stops for approval before anything is merged or tagged.

## Versions

commitizen (`.cz.toml`) picks the next version from the commit messages
since the last tag. CI checks that every commit is a conventional commit.

| Commits since the last tag | Below v1.0.0 | From v1.0.0 |
|---|---|---|
| a `BREAKING CHANGE:` footer or a `!` after the type | minor | major |
| `feat` | minor | minor |
| `fix`, `perf`, `refactor` | patch | patch |
| anything else | no release | no release |

Below v1.0.0 a breaking change bumps the minor version
(`major_version_zero`), so v1.0.0 is cut on purpose, never by a `!` commit.
A v2 or later would need a `/v2` module path, so cairn means to stay on v0
and v1.

The changelog takes its entries from commit subjects and its breaking-change
notes from `BREAKING CHANGE:` footers. Write both for someone upgrading.

## Steps

From an up-to-date main whose last CI run passed:

1. **See what would ship.**

   ```sh
   cz bump --get-next --yes                                  # e.g. 0.2.0
   cz changelog --dry-run --unreleased-version v0.2.0        # its notes
   ```

   `cz bump --get-next` exits 21 with `NO_COMMITS_TO_BUMP` when nothing since
   the last tag warrants a release.

2. **Open a release PR** that adds the new section to `CHANGELOG.md`:

   ```sh
   git worktree add .worktrees/release-v0.2.0 -b release/v0.2.0 main
   cd .worktrees/release-v0.2.0
   cz changelog --unreleased-version v0.2.0
   git add CHANGELOG.md && git commit -m "chore(release): v0.2.0"
   git push -u origin release/v0.2.0 && gh pr create --fill
   ```

   `cz changelog` regenerates the whole file from the tags and commits, so an
   edit belongs in a commit message, not in `CHANGELOG.md`.

3. **Merge it** with a merge commit once CI passes, as long as nothing else
   has landed on main since the branch was cut. If something has, regenerate
   the changelog on the branch first, or the file misses those commits.

4. **Tag the merge commit** and push the tag:

   ```sh
   git fetch origin main
   git tag -a v0.2.0 -m v0.2.0 origin/main
   git push origin v0.2.0
   ```

5. **The release workflow** (`.github/workflows/release.yml`) runs on the
   tag. It checks that the tag is on main and publishes a GitHub Release with
   that version's changelog section as its notes.

## The manifest tool

`cmd/cairn-manifest` is its own module and isn't tagged. Another project
runs it at the commit of a cairn release with
`go run github.com/anyweez/cairn/cmd/cairn-manifest@<commit>`.
