#!/usr/bin/env bash
# Helpers shared by the fixture scripts, which start or reuse a container.

# fixture_name BASE DEFAULT_PORT PORT prints the container name a fixture uses
# for PORT. The default port keeps BASE, because factory.yaml's `down` and
# callers name that container; every other port gets a container of its own, so
# a session on one port never removes a container another session is using.
fixture_name() {
  local base=$1 default_port=$2 port=$3
  if [ "$port" = "$default_port" ]; then
    printf '%s' "$base"
  else
    printf '%s-%s' "$base" "$port"
  fi
}

# container_publishes NAME CONTAINER_PORT HOST_PORT succeeds only when NAME is
# running and publishes CONTAINER_PORT on 127.0.0.1:HOST_PORT. A container
# found by name alone may have been created with a different port, and then
# the URL a script prints points somewhere nothing is listening.
container_publishes() {
  local name=$1 container_port=$2 host_port=$3 mapping
  [ "$(docker inspect -f '{{.State.Running}}' "$name" 2>/dev/null)" = true ] || return 1
  mapping=$(docker port "$name" "$container_port" 2>/dev/null | head -1) || return 1
  [ "$mapping" = "127.0.0.1:${host_port}" ]
}
