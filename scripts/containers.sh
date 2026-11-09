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

# fixture_host_port PORT succeeds when PORT is a host port a container can
# publish. A companion's host port is computed from the fixture port, so a
# fixture port near the end of the range pushes its companions past 65535;
# Docker only refuses that when the container is created.
fixture_host_port() {
  local port=$1
  case $port in
    '' | *[!0-9]*) return 1 ;;
  esac
  [ "$port" -ge 1 ] && [ "$port" -le 65535 ]
}

# fixture_port DEFAULT_PORT DEFAULT_KEY KEY [OVERRIDE] prints the host port a
# companion fixture publishes. A companion's container name is keyed by the
# fixture port it belongs to (fixture_name), so its host port has to be keyed
# the same way: left at the default, a session on another CAIRN_TEST_PORT leaves
# a differently named container holding the port the next default session asks
# for, and Docker refuses to start it. The port keeps its distance from the key
# (55873 is two above the Postgres default 55871), so the default session gets
# the documented port and any other key gets a host port of its own. An
# explicit override still wins. A port outside 1-65535 is refused with a
# message rather than passed to Docker.
fixture_port() {
  local default_port=$1 default_key=$2 key=$3 override=${4:-} port what
  what="the companion of fixture port ${key}"
  if [ -n "$override" ]; then
    port=$override
    what="the companion port asked for"
  else
    case $key in
      '' | *[!0-9]*)
        echo "cairn: fixture port '${key}' is not a port number" >&2
        return 1
        ;;
    esac
    port=$(( default_port + key - default_key ))
  fi
  if ! fixture_host_port "$port"; then
    echo "cairn: ${what} is host port ${port}, outside 1-65535" >&2
    return 1
  fi
  printf '%s' "$port"
}

# fixture_port_beside DEFAULT_PORT DEFAULT_KEY KEY [OVERRIDE] [AVOID] prints
# the host port a companion publishes beside another companion in the same
# container, rather than at a fixed distance from the fixture port. The
# toxiproxy's control API is one: it and the proxy are fixed when its container
# is created, so moving the proxy port has to move the API port with it, and
# the documented pair (55872 and 8474) is not the distance apart the others
# are. KEY at DEFAULT_KEY keeps DEFAULT_PORT; any other KEY takes the port next
# to it, stepping over AVOID, which is the port the other fixture in front of
# the same Postgres publishes: one session may run both, and a host port can
# only be published once. An override still wins, and a port outside 1-65535 is
# refused rather than passed to Docker.
fixture_port_beside() {
  local default_port=$1 default_key=$2 key=$3 override=${4:-} avoid=${5:-} port what
  what="the port beside ${key}"
  if [ -n "$override" ]; then
    port=$override
    what="the companion port asked for"
  elif [ "$key" = "$default_key" ]; then
    port=$default_port
  else
    case $key in
      '' | *[!0-9]*)
        echo "cairn: companion host port '${key}' is not a port number" >&2
        return 1
        ;;
    esac
    port=$(( key + 1 ))
    if [ -n "$avoid" ] && [ "$port" = "$avoid" ]; then
      port=$(( port + 1 ))
    fi
  fi
  if ! fixture_host_port "$port"; then
    echo "cairn: ${what} is host port ${port}, outside 1-65535" >&2
    return 1
  fi
  printf '%s' "$port"
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

# container_setting NAME SETTING prints the value the Postgres running in NAME
# reports for SETTING, or nothing when it cannot be read. A -c flag passed to
# docker run is fixed when the container is created, so a container the fixture
# started before that flag existed keeps the old value: asking the server is
# the only way a reuse check sees it.
container_setting() {
  local name=$1 setting=$2
  docker exec "$name" psql -U cairn -d postgres -Atc "SHOW ${setting}" 2>/dev/null || true
}
