#!/usr/bin/env bash
# Shared helpers for the omnibus init and service scripts. Sourced, not run.

STATE_DIR=/run/silo-omnibus
CONTAINER_ENV=/run/s6/container_environment
# Present while the image, not the deployment, chose Meilisearch for search.
# shellcheck disable=SC2034 # read by init-config and svc-search-marker
SEARCH_MARKER=/config/meilisearch/silo-search-provider

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

# exec_clean PATTERN CMD...: exec CMD with only PATH, HOME, TZ, and the
# variables whose names match the extended regex PATTERN. Bundled services run
# this way so they never see SECRET_KEY or the rest of Silo's environment: a
# SQL superuser can run programs as the postgres account.
exec_clean() {
  local pattern=$1 name
  shift
  local -a keep=("PATH=$PATH" "HOME=$HOME")
  [[ -n "${TZ:-}" ]] && keep+=("TZ=$TZ")
  if [[ -n "$pattern" ]]; then
    for name in $(compgen -e); do
      [[ "$name" =~ $pattern ]] && keep+=("$name=${!name}")
    done
  fi
  exec env -i "${keep[@]}" "$@"
}
