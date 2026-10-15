#!/usr/bin/env bash
# QA reproduction for A 7.5 MB compiled cairn-manifest binary is committed to the repository,
# recorded by spine finding. It failed against 0feefcbafda6b404e970748869fc35d7d03a9fd7;
# the fix is judged by this script passing.
test -z "$(git ls-files cmd/cairn-manifest/cairn-manifest)"
