#!/bin/sh
# Create the source-development .env for the local PostgreSQL and Redis
# services in docker-compose.yml.
#
# A new password is generated unless POSTGRES_PASSWORD is set, which reuses the
# current password of an existing local database:
#
#   POSTGRES_PASSWORD='current password' scripts/init-dev-env.sh
#
# Values are single-quoted so Compose and godotenv read them literally, and
# the DATABASE_URL copy of the password is percent-encoded.
set -eu

root=$(cd "$(dirname "$0")/.." && pwd)
env_file=${1:-$root/.env}

if [ -e "$env_file" ]; then
	echo "$env_file already exists; add POSTGRES_PASSWORD to it or move it aside" >&2
	exit 1
fi

password=${POSTGRES_PASSWORD:-}
if [ -z "$password" ]; then
	password=$(openssl rand -hex 24)
fi
# Single quotes keep every other byte literal, but Compose and godotenv both
# read a backslash before the closing quote as escaping it.
case $password in
*"'"* | *"
"*)
	echo "POSTGRES_PASSWORD cannot contain a single quote or a newline" >&2
	exit 1
	;;
*\\)
	echo "POSTGRES_PASSWORD cannot end with a backslash" >&2
	exit 1
	;;
esac

# Percent-encode every byte outside the RFC 3986 unreserved set.
encoded_password=$(printf '%s' "$password" | od -An -v -tu1 | awk '{
	for (i = 1; i <= NF; i++) {
		c = $i
		if ((c >= 48 && c <= 57) || (c >= 65 && c <= 90) || (c >= 97 && c <= 122) ||
			c == 45 || c == 46 || c == 95 || c == 126)
			printf "%c", c
		else
			printf "%%%02X", c
	}
}')

# Generate the key in its own assignment so set -e stops on an openssl failure
# instead of writing an empty SECRET_KEY.
secret_key=$(openssl rand -base64 48)
if [ -z "$secret_key" ] || [ -z "$encoded_password" ]; then
	echo "failed to generate the bootstrap values" >&2
	exit 1
fi

# Write the complete file beside the destination, then hard-link it into place.
# ln fails if the destination appeared in the meantime, so a concurrent run
# can never replace generated credentials, and an interrupted run leaves no
# partial .env behind.
umask 077
tmp_file=$(mktemp "$(dirname "$env_file")/.env.init.XXXXXX")
trap 'rm -f "$tmp_file"' EXIT
cp "$root/.env.example" "$tmp_file"
chmod 600 "$tmp_file"
printf "\nPOSTGRES_PASSWORD='%s'\nSECRET_KEY='%s'\nDATABASE_URL='%s'\nREDIS_URL='%s'\n" \
	"$password" \
	"$secret_key" \
	"postgres://silo:${encoded_password}@localhost:5432/silo?sslmode=disable" \
	'redis://localhost:6379' >>"$tmp_file"
if ! ln "$tmp_file" "$env_file" 2>/dev/null; then
	echo "$env_file already exists; add POSTGRES_PASSWORD to it or move it aside" >&2
	exit 1
fi
echo "Created $env_file"
