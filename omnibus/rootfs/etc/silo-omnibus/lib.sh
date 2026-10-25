#!/usr/bin/env bash
# Shared helpers for the omnibus init and service scripts. Sourced, not run.

STATE_DIR=/run/silo-omnibus
CONTAINER_ENV=/run/s6/container_environment

log() { printf '[omnibus] %s\n' "$*"; }
warn() { printf '[omnibus] warning: %s\n' "$*" >&2; }

# truthy VALUE: accepts the usual spellings of yes.
truthy() {
  case "${1,,}" in
    1|true|yes|on) return 0 ;;
    *) return 1 ;;
  esac
}

# enabled NAME: whether init-config turned the named bundled service on.
enabled() { [[ -f "$STATE_DIR/$1.enabled" ]]; }

# export_env NAME VALUE: make a variable visible to every later service.
export_env() {
  printf '%s' "$2" > "$CONTAINER_ENV/$1"
  export "$1=$2"
}

# own_tree USER GROUP PATH: hand a tree to a service account, touching only
# entries that are not already owned correctly so large trees stay fast.
own_tree() {
  find "$3" \( ! -user "$1" -o ! -group "$2" \) -exec chown -h "$1:$2" {} + 2>/dev/null || true
}

# notify_ready: tell s6 a longrun is ready on the fd named in notification-fd.
notify_ready() { printf '\n' >&3; }
