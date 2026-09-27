#!/usr/bin/env bash
# Preflight and optional cutover helper for Docker installs migrating from
# Continuum to Silo.
set -euo pipefail

ROOT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"

MODE="check"
APPLY="false"
SKIP_DB_DUMP="false"
KEEP_OLD_DATA_ROOT="false"
NO_COMPAT_SYMLINK="false"

COMPOSE_FILE="${COMPOSE_FILE:-${ROOT_DIR}/docker-compose.yml}"
ENV_FILE="${ENV_FILE:-${ROOT_DIR}/.env}"
CONTINUUM_COMPOSE_FILE="${CONTINUUM_COMPOSE_FILE:-}"
CONTINUUM_ENV_FILE="${CONTINUUM_ENV_FILE:-}"
CONTINUUM_PROJECT="${CONTINUUM_PROJECT:-continuum}"
CONTINUUM_DATA_ROOT="${CONTINUUM_DATA_ROOT:-/opt/continuum}"
SILO_DATA_ROOT="${SILO_DATA_ROOT:-/opt/silo}"
BACKUP_DIR="${BACKUP_DIR:-${CONTINUUM_DATA_ROOT}/db-backups}"

usage() {
	cat <<'USAGE'
Usage:
  scripts/migrate-continuum-docker.sh check
  sudo scripts/migrate-continuum-docker.sh migrate --apply
  scripts/migrate-continuum-docker.sh db-fix --apply

Modes:
  check    Read-only preflight. Prints detected containers, paths, env, and risks.
  migrate  Backs up the DB, stops the old Continuum stack, moves bind-mounted
           state from /opt/continuum to /opt/silo, starts Silo, and applies
           narrow DB compatibility updates.
  db-fix   Only applies DB compatibility updates to a running Silo compose stack.

Important environment overrides:
  COMPOSE_FILE=/path/to/silo/docker-compose.yml
  ENV_FILE=/path/to/silo/.env
  CONTINUUM_COMPOSE_FILE=/path/to/old/docker-compose.yml
  CONTINUUM_ENV_FILE=/path/to/old/.env
  CONTINUUM_DATA_ROOT=/opt/continuum
  SILO_DATA_ROOT=/opt/silo
  BACKUP_DIR=/opt/continuum/db-backups

Flags:
  --apply               Required for modes that modify the host or database.
  --skip-db-dump        Do not run pg_dump before cutover.
  --keep-old-data-root  Do not move CONTINUUM_DATA_ROOT to SILO_DATA_ROOT.
  --no-compat-symlink   Do not leave CONTINUUM_DATA_ROOT as a symlink to Silo.
  -h, --help            Show this help.
USAGE
}

log() {
	printf '==> %s\n' "$*"
}

warn() {
	printf 'WARN: %s\n' "$*" >&2
}

die() {
	printf 'ERROR: %s\n' "$*" >&2
	exit 1
}

while [ "$#" -gt 0 ]; do
	case "$1" in
	check|migrate|db-fix)
		MODE="$1"
		;;
	--apply)
		APPLY="true"
		;;
	--skip-db-dump)
		SKIP_DB_DUMP="true"
		;;
	--keep-old-data-root)
		KEEP_OLD_DATA_ROOT="true"
		;;
	--no-compat-symlink)
		NO_COMPAT_SYMLINK="true"
		;;
	-h|--help)
		usage
		exit 0
		;;
	*)
		usage >&2
		die "unknown argument: $1"
		;;
	esac
	shift
done

strip_quotes() {
	local value="$1"
	value="${value%$'\r'}"
	if [[ "${value}" == \"*\" && "${value}" == *\" ]]; then
		value="${value:1:${#value}-2}"
	elif [[ "${value}" == \'*\' && "${value}" == *\' ]]; then
		value="${value:1:${#value}-2}"
	fi
	printf '%s' "${value}"
}

env_value() {
	local key="$1"
	local default_value="$2"
	local line

	if [ -f "${ENV_FILE}" ]; then
		line="$(grep -E "^[[:space:]]*${key}=" "${ENV_FILE}" | tail -n 1 || true)"
		if [ -n "${line}" ]; then
			strip_quotes "${line#*=}"
			return
		fi
	fi
	printf '%s' "${default_value}"
}

compose() {
	if [ -f "${ENV_FILE}" ]; then
		docker compose --env-file "${ENV_FILE}" -f "${COMPOSE_FILE}" "$@"
	else
		docker compose -f "${COMPOSE_FILE}" "$@"
	fi
}

continuum_compose_down() {
	if [ -n "${CONTINUUM_COMPOSE_FILE}" ] && [ -f "${CONTINUUM_COMPOSE_FILE}" ]; then
		if [ -n "${CONTINUUM_ENV_FILE}" ] && [ -f "${CONTINUUM_ENV_FILE}" ]; then
			docker compose --env-file "${CONTINUUM_ENV_FILE}" -p "${CONTINUUM_PROJECT}" -f "${CONTINUUM_COMPOSE_FILE}" down --remove-orphans
		else
			docker compose -p "${CONTINUUM_PROJECT}" -f "${CONTINUUM_COMPOSE_FILE}" down --remove-orphans
		fi
		return
	fi

	old_containers=()
	while IFS= read -r container; do
		old_containers+=("${container}")
	done < <(docker ps -a --format '{{.Names}}' | grep -E '(^continuum$|^continuum-|^continuum_)' || true)
	if [ "${#old_containers[@]}" -gt 0 ]; then
		docker rm -f "${old_containers[@]}"
	else
		warn "no Continuum compose file or continuum-* containers found to stop"
	fi
}

find_continuum_postgres_container() {
	local container

	container="$(docker ps -a \
		--filter "label=com.docker.compose.project=${CONTINUUM_PROJECT}" \
		--filter "label=com.docker.compose.service=postgres" \
		--format '{{.Names}}' | head -n 1)"
	if [ -n "${container}" ]; then
		printf '%s' "${container}"
		return
	fi

	container="$(docker ps -a --format '{{.Names}}' | grep -E '(^continuum-postgres$|^continuum_postgres_|^continuum-postgres-)' | head -n 1 || true)"
	if [ -n "${container}" ]; then
		printf '%s' "${container}"
	fi
}

require_apply() {
	if [ "${APPLY}" != "true" ]; then
		die "${MODE} modifies state; rerun with --apply after reviewing check output"
	fi
}

require_tools() {
	command -v docker >/dev/null 2>&1 || die "docker is required"
	docker compose version >/dev/null 2>&1 || die "docker compose is required"
}

require_docker_daemon() {
	docker info >/dev/null 2>&1 || die "docker daemon is not reachable"
}

print_preflight() {
	local postgres_user postgres_db media_root media_container_root image old_pg docker_ready

	require_tools
	postgres_user="$(env_value POSTGRES_USER continuum)"
	postgres_db="$(env_value POSTGRES_DB continuum)"
	media_root="$(env_value MEDIA_ROOT '')"
	media_container_root="$(env_value MEDIA_CONTAINER_ROOT /mnt/media)"
	image="$(env_value SILO_IMAGE 'ghcr.io/silo-server/silo-server:latest')"
	docker_ready="false"
	if docker info >/dev/null 2>&1; then
		docker_ready="true"
		old_pg="$(find_continuum_postgres_container || true)"
	else
		old_pg=""
		warn "docker daemon is not reachable; skipping live container and DB probes"
	fi

	log "Silo compose"
	printf 'compose file:        %s\n' "${COMPOSE_FILE}"
	printf 'env file:            %s\n' "${ENV_FILE}"
	printf 'image:               %s\n' "${image}"
	printf 'media root:          %s\n' "${media_root:-<unset>}"
	printf 'media container root: %s\n' "${media_container_root}"
	printf 'silo data root:      %s\n' "${SILO_DATA_ROOT}"
	printf 'postgres user/db:    %s / %s\n' "${postgres_user}" "${postgres_db}"
	printf '\n'

	log "Continuum source"
	printf 'old data root:       %s\n' "${CONTINUUM_DATA_ROOT}"
	printf 'old compose file:    %s\n' "${CONTINUUM_COMPOSE_FILE:-<not set>}"
	printf 'old postgres:        %s\n' "${old_pg:-<not detected>}"
	printf '\n'

	log "Detected containers"
	if [ "${docker_ready}" = "true" ]; then
		docker ps -a --format 'table {{.Names}}\t{{.Status}}\t{{.Image}}' | grep -E '(^NAMES|continuum|silo)' || true
	else
		printf '<skipped; docker daemon unavailable>\n'
	fi
	printf '\n'

	log "Filesystem state"
	if [ -d "${CONTINUUM_DATA_ROOT}" ]; then
		printf 'found old data root: %s\n' "${CONTINUUM_DATA_ROOT}"
	else
		warn "old data root not found: ${CONTINUUM_DATA_ROOT}"
	fi
	if [ -e "${SILO_DATA_ROOT}" ]; then
		printf 'found Silo data root: %s\n' "${SILO_DATA_ROOT}"
	fi
	if [ -n "${media_root}" ] && [ ! -d "${media_root}" ]; then
		warn "MEDIA_ROOT does not exist on this host: ${media_root}"
	fi
	if [ "${media_container_root}" = "/mnt/media" ]; then
		warn "MEDIA_CONTAINER_ROOT is /mnt/media. For migrations, set it to the old in-container media path if existing library records use another absolute path."
	fi
	printf '\n'

	log "Silo compose validation"
	if [ -f "${COMPOSE_FILE}" ]; then
		if compose config >/dev/null; then
			printf 'compose config: ok\n'
		else
			warn "compose config failed; check ${ENV_FILE} and required variables such as MEDIA_ROOT"
		fi
	else
		warn "compose file not found: ${COMPOSE_FILE}"
	fi
	printf '\n'

	if [ "${docker_ready}" = "true" ] && [ -n "${old_pg}" ]; then
		log "Continuum DB probe"
		if docker start "${old_pg}" >/dev/null 2>&1; then
			docker exec "${old_pg}" psql -U "${postgres_user}" -d "${postgres_db}" -Atc \
				"SELECT 'users', count(*) FROM users UNION ALL SELECT 'media_items', count(*) FROM media_items;" 2>/dev/null || \
				warn "could not query old DB with POSTGRES_USER=${postgres_user} POSTGRES_DB=${postgres_db}"
			docker exec "${old_pg}" psql -U "${postgres_user}" -d "${postgres_db}" -Atc \
				"SELECT id || '|' || plugin_id || '|' || enabled FROM plugin_installations ORDER BY id;" 2>/dev/null || true
		else
			warn "could not start detected old postgres container ${old_pg} for DB probe"
		fi
		printf '\n'
	fi

	log "Recommended next step"
	printf 'Review the output above, make sure you have a snapshot or backup, then run:\n'
	printf '  sudo %s migrate --apply\n' "$0"
}

dump_database() {
	local old_pg postgres_user postgres_db backup_file

	if [ "${SKIP_DB_DUMP}" = "true" ]; then
		warn "skipping DB dump because --skip-db-dump was provided"
		return
	fi

	old_pg="$(find_continuum_postgres_container || true)"
	if [ -z "${old_pg}" ]; then
		die "could not find Continuum PostgreSQL container; set CONTINUUM_COMPOSE_FILE or use --skip-db-dump only if you have another backup"
	fi

	postgres_user="$(env_value POSTGRES_USER continuum)"
	postgres_db="$(env_value POSTGRES_DB continuum)"
	backup_file="${BACKUP_DIR}/continuum-before-silo-$(date -u +%Y%m%dT%H%M%SZ).dump"

	log "Creating PostgreSQL dump at ${backup_file}"
	mkdir -p "${BACKUP_DIR}"
	docker start "${old_pg}" >/dev/null
	docker exec "${old_pg}" pg_dump -U "${postgres_user}" -d "${postgres_db}" -Fc > "${backup_file}"
}

move_data_root() {
	if [ "${KEEP_OLD_DATA_ROOT}" = "true" ]; then
		warn "leaving old data root in place because --keep-old-data-root was provided"
		return
	fi

	if [ ! -d "${CONTINUUM_DATA_ROOT}" ]; then
		warn "old data root does not exist, skipping move: ${CONTINUUM_DATA_ROOT}"
		return
	fi

	if [ -e "${SILO_DATA_ROOT}" ] && [ ! -L "${SILO_DATA_ROOT}" ]; then
		die "Silo data root already exists: ${SILO_DATA_ROOT}; move or merge it manually before running migrate"
	fi

	log "Moving ${CONTINUUM_DATA_ROOT} to ${SILO_DATA_ROOT}"
	mkdir -p "$(dirname "${SILO_DATA_ROOT}")"
	mv "${CONTINUUM_DATA_ROOT}" "${SILO_DATA_ROOT}"

	if [ "${NO_COMPAT_SYMLINK}" != "true" ]; then
		ln -s "${SILO_DATA_ROOT}" "${CONTINUUM_DATA_ROOT}"
		log "Left compatibility symlink ${CONTINUUM_DATA_ROOT} -> ${SILO_DATA_ROOT}"
	fi
}

wait_for_postgres() {
	local postgres_user
	postgres_user="$(env_value POSTGRES_USER continuum)"

	log "Waiting for PostgreSQL"
	for _ in $(seq 1 60); do
		if compose exec -T postgres pg_isready -U "${postgres_user}" >/dev/null 2>&1; then
			return
		fi
		sleep 2
	done
	die "PostgreSQL did not become ready"
}

apply_db_compatibility_updates() {
	local postgres_user postgres_db
	postgres_user="$(env_value POSTGRES_USER continuum)"
	postgres_db="$(env_value POSTGRES_DB continuum)"

	log "Applying Continuum-to-Silo DB compatibility updates"
	compose exec -T postgres psql -U "${postgres_user}" -d "${postgres_db}" -v ON_ERROR_STOP=1 <<'SQL'
DO $$
BEGIN
	IF to_regclass('public.plugin_installations') IS NOT NULL THEN
		UPDATE plugin_installations
		SET install_path = replace(install_path, '/var/lib/continuum/plugins/', '/var/lib/silo/plugins/'),
		    enabled = CASE WHEN plugin_id LIKE 'continuum.%' THEN false ELSE enabled END,
		    updated_at = NOW()
		WHERE install_path LIKE '/var/lib/continuum/plugins/%'
		   OR plugin_id LIKE 'continuum.%';
	END IF;

	IF to_regclass('public.plugin_repositories') IS NOT NULL THEN
		UPDATE plugin_repositories
		SET display_name = replace(display_name, 'Continuum', 'Silo'),
		    url = replace(url, 'https://raw.githubusercontent.com/ContinuumApp/continuum-plugins/', 'https://raw.githubusercontent.com/Silo-Server/silo-plugins/'),
		    enabled = false,
		    updated_at = NOW()
		WHERE display_name LIKE '%Continuum%'
		   OR url LIKE '%ContinuumApp/continuum-plugins/%'
		   OR url LIKE '%Silo-Server/silo-plugins/%';
	END IF;
END $$;
SQL

	if compose exec -T postgres psql -U "${postgres_user}" -d "${postgres_db}" -Atc "SELECT to_regclass('public.plugin_installations')" | grep -q plugin_installations; then
		compose exec -T postgres psql -U "${postgres_user}" -d "${postgres_db}" -P pager=off -c \
			"SELECT id, plugin_id, version, enabled FROM plugin_installations ORDER BY id;"
	fi
}

wait_for_silo() {
	local port
	port="$(env_value PORT 8090)"

	# /api/v1/health and /api/v1/ready are retained operational probes; they keep
	# these paths after the /api/v1 contract is retired.
	log "Waiting for Silo readiness on localhost:${port}"
	for _ in $(seq 1 60); do
		if curl -fsS "http://localhost:${port}/api/v1/ready" >/dev/null 2>&1; then
			curl -fsS "http://localhost:${port}/api/v1/health" || true
			printf '\n'
			curl -fsS "http://localhost:${port}/api/v1/ready" || true
			printf '\n'
			return
		fi
		sleep 2
	done
	warn "Silo did not report ready within the wait window; inspect logs with docker compose logs silo"
}

run_migration() {
	require_tools
	require_apply
	require_docker_daemon

	[ -f "${COMPOSE_FILE}" ] || die "compose file not found: ${COMPOSE_FILE}"
	[ -f "${ENV_FILE}" ] || die "env file not found: ${ENV_FILE}; copy .env.example to .env and set MEDIA_ROOT plus old POSTGRES_* values first"

	if [ "$(env_value POSTGRES_USER silo)" = "silo" ] || [ "$(env_value POSTGRES_DB silo)" = "silo" ]; then
		warn "POSTGRES_USER/POSTGRES_DB are set to Silo defaults. If reusing a Continuum PostgreSQL data directory, set them to the old DB values before migrating."
	fi

	dump_database
	log "Stopping old Continuum stack"
	continuum_compose_down
	move_data_root

	log "Starting Silo stack"
	compose up -d
	wait_for_postgres
	apply_db_compatibility_updates
	wait_for_silo

	log "Migration complete"
	printf 'Old Continuum plugin installations were disabled if present. Install Silo-native plugin packages from the admin UI, then re-enable provider chains as needed.\n'
}

case "${MODE}" in
	check)
		print_preflight
		;;
	migrate)
		run_migration
		;;
	db-fix)
		require_tools
		require_docker_daemon
		require_apply
		wait_for_postgres
		apply_db_compatibility_updates
		;;
	*)
		usage >&2
		die "unknown mode: ${MODE}"
		;;
esac
