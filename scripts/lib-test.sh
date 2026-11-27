#!/usr/bin/env bash
# Helpers shared by the shell tests in scripts/.

# require_docker WHAT succeeds when a Docker daemon is reachable, and otherwise
# prints why and exits 2. A shell test exits 2 when the host cannot run it at
# all; scripts/check.sh reports that as a skip rather than a failure, so the gate
# keeps working where it is documented to: with CAIRN_TEST_DATABASE_URL pointing
# at an external server and no Docker daemon.
require_docker() {
	docker info >/dev/null 2>&1 && return 0
	echo "skip: no Docker daemon, so $* cannot run here" >&2
	exit 2
}
