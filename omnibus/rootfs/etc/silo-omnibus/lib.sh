#!/usr/bin/env bash
# Shared helpers for the omnibus init and service scripts. Sourced, not run.

STATE_DIR=/run/silo-omnibus
CONTAINER_ENV=/run/s6/container_environment
# Present while the image, not the deployment, chose Meilisearch for search.
# Markers live in /config itself, which only root can write: these scripts run
# as root and must not write where a service account could plant a symlink.
# shellcheck disable=SC2034 # read by init-config and svc-search-marker
SEARCH_MARKER=/config/.silo-search-provider

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

# write_as USER PATH, append_as USER PATH: write or append stdin to PATH as
# USER. For files in a directory a service account owns: written as root, a
# symlink that account planted at PATH would redirect the write.
write_as() { s6-setuidgid "$1" tee "$2" >/dev/null; }
append_as() { s6-setuidgid "$1" tee -a "$2" >/dev/null; }

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

# PG_LOCK is held, as a flock(2) lock, by the PostgreSQL server for its whole
# life. postmaster.pid only guards against servers in one PID namespace; a
# second container on the same /config sees a stale-looking pid file and would
# start a second server on the same data directory. It lives in /config, which
# only root writes, so the postgres account cannot swap it for a new file. It
# is root:postgres 0660 and opened read-write: NFS emulates flock(2) with a
# POSIX lock, and an exclusive one needs a writable descriptor.
# shellcheck disable=SC2034 # used by init-config, init-postgres, svc-postgres
PG_LOCK=/config/.postgres.lock

# read_small PATH: the first 16 bytes of PATH, read without following a link
# and without blocking on a FIFO, for files root reads in a directory a service
# account controls.
read_small() { dd if="$1" bs=16 count=1 iflag=nofollow,nonblock status=none 2>/dev/null; }

# silo_port: the port Silo listens on. Silo reads /config/silo/.env without
# overriding variables already set, so PORT from the container wins, then a
# PORT line in that file, then Silo's default.
silo_port() {
  local port="${PORT:-}"
  if [[ -z "$port" && -f /config/silo/.env ]]; then
    port="$(s6-setuidgid silo sed -nE "s/^[[:space:]]*(export[[:space:]]+)?PORT[[:space:]]*=[[:space:]]*['\"]?([0-9]+)['\"]?.*/\\2/p" /config/silo/.env | tail -n 1)"
  fi
  printf '%s' "${port:-8080}"
}
