#!/usr/bin/env bash
# scripts/fixture-port_test.sh is the regression test for
# scripts/containers.sh's fixture_port and fixture_port_beside: it pins the
# documented default ports and container names, keeps a custom CAIRN_TEST_PORT's
# companions off the defaults, and refuses an out-of-range port. It needs no
# Docker and runs in milliseconds, but scripts/check.sh never invokes it, so a
# regression in that helper passes the gate even though the test that catches
# it is committed beside the script.
#
# This fails while scripts/check.sh does not run the test, and passes once the
# gate invokes it the way it invokes the other shell tests.
set -euo pipefail
cd "$(dirname "$0")/.."

# The gate may name a test outright or run it through a glob, so every
# scripts/<name>.sh token in check.sh is expanded the way the shell would
# expand it.
covered=()
while IFS= read -r token; do
  for path in $token; do
    [ -f "$path" ] && covered+=("$path")
  done
done < <(grep -oE 'scripts/[A-Za-z0-9_.*-]+\.sh' scripts/check.sh || true)

case " ${covered[*]-} " in
*" scripts/fixture-port_test.sh "*) ;;
*)
  echo "FAIL: scripts/check.sh does not run scripts/fixture-port_test.sh" >&2
  echo "      a regression in scripts/containers.sh's fixture_port helper passes the gate" >&2
  exit 1
  ;;
esac

# The gate must also accept the test: running it directly is what the gate will
# do, and it is the check that the invocation above is worth adding.
bash scripts/fixture-port_test.sh >/dev/null
echo "ok: scripts/check.sh runs scripts/fixture-port_test.sh"
