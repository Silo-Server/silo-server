#!/usr/bin/env bash
set -euo pipefail

usage() {
	printf 'usage: %s [--cached]\n' "${0##*/}" >&2
}

cached=0
case "${1:-}" in
	"")
		;;
	--cached)
		cached=1
		;;
	-h|--help)
		usage
		exit 0
		;;
	*)
		usage
		exit 2
		;;
esac

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

failed=0
t3_worktree_dir='\.t3'/'worktrees'
t3_worktree_id_prefix='t3''code-'

check_pattern() {
	local label=$1
	local pattern=$2
	shift 2

	local matches
	if [[ "$cached" -eq 1 ]]; then
		matches=$(git grep --cached -n -I -E "$pattern" -- "$@" 2>/dev/null) || return 0
	else
		matches=$(git grep -n -I -E "$pattern" -- "$@" 2>/dev/null) || return 0
	fi

	if [[ -n "$matches" ]]; then
		printf '%s\n' "local path leak check failed: $label" >&2
		printf '%s\n\n' "$matches" >&2
		failed=1
	fi
}

check_pattern \
	"T3 worktree path or generated worktree id" \
	"(${t3_worktree_dir}|/Users/[^[:space:]]*/${t3_worktree_dir}/|${t3_worktree_id_prefix}[0-9a-f]{8})" \
	docs/superpowers/specs docs/superpowers/plans

check_pattern \
	"absolute local filesystem path in generated superpowers docs" \
	'(/Users/[^[:space:]]+|/home/[^[:space:]]+|/Volumes/[^[:space:]]+|/var/folders/[^[:space:]]+|/private/tmp/[^[:space:]]+|[A-Za-z]:\\Users\\[^[:space:]]+)' \
	docs/superpowers/specs docs/superpowers/plans

# Scenario catalogs are public fixtures: no machine paths, no hosts outside
# the reserved set, no IPv4 literals other than loopback, no credential-looking
# values. Fixture identities use reserved origins (silo.example.test) and
# runtime ${...} placeholders. Only the catalog bodies are scanned; the schema
# file carries $schema/$id URLs (json-schema.org, siloserver.org) by design.
catalog_files=':(glob)contracts/api/v2/scenarios/*/*.json'

check_pattern \
	"absolute local filesystem path in a scenario catalog" \
	'(/Users/[^[:space:]"]+|/home/[^[:space:]"]+|/Volumes/[^[:space:]"]+|/var/folders/[^[:space:]"]+|/private/tmp/[^[:space:]"]+|[A-Za-z]:\\Users\\[^[:space:]"]+)' \
	"$catalog_files"

# reserved_host succeeds for hosts a public fixture may name: RFC 2606 / RFC
# 6761 reserved names, localhost, and the IPv4 loopback address.
reserved_host() {
	local host=$1
	host=${host//\\/}
	host=${host#*://}
	host=${host#*@}
	host=${host%%:*}
	host=${host%%/*}
	host=$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')
	host=${host%.}
	case "$host" in
		127.0.0.1|localhost|*.test|*.example|*.invalid|*.localhost)
			return 0
			;;
	esac
	return 1
}

# check_hosts is an allowlist: every host-shaped token the line pattern finds
# is extracted with the token pattern and must satisfy reserved_host. ERE has
# no lookaround, so the filtering happens here rather than in the regex.
check_hosts() {
	local label=$1
	local line_pattern=$2
	local token_pattern=$3
	shift 3

	local lines
	if [[ "$cached" -eq 1 ]]; then
		lines=$(git grep --cached -n -I -E "$line_pattern" -- "$@" 2>/dev/null) || return 0
	else
		lines=$(git grep -n -I -E "$line_pattern" -- "$@" 2>/dev/null) || return 0
	fi

	local matches=""
	local line file lineno text token
	while IFS= read -r line; do
		file=${line%%:*}
		line=${line#*:}
		lineno=${line%%:*}
		text=${line#*:}
		while IFS= read -r token; do
			[[ -z "$token" ]] && continue
			# Strip the one-character context the token pattern needs.
			token=$(printf '%s' "$token" | sed -E 's/^[^A-Za-z0-9@]//; s/[^A-Za-z0-9.\\:-]+$//')
			if ! reserved_host "$token"; then
				matches+="${file}:${lineno}: ${token}"$'\n'
			fi
		done < <(printf '%s\n' "$text" | grep -o -E "$token_pattern" || true)
	done <<< "$lines"

	if [[ -n "$matches" ]]; then
		printf '%s\n' "local path leak check failed: $label" >&2
		printf '%s\n' "$matches" >&2
		failed=1
	fi
}

# Any scheme (http, https, ws, wss, rtsp, ...) followed by an authority.
check_hosts \
	"non-reserved URL host in a scenario catalog" \
	'[A-Za-z][A-Za-z0-9+.-]*://[^/?#"[:space:]]+' \
	'[A-Za-z][A-Za-z0-9+.-]*://[^/?#"[:space:]]+' \
	"$catalog_files"

# Mail-style @domain (fixture accounts, invitations).
check_hosts \
	"non-reserved mail domain in a scenario catalog" \
	'@([A-Za-z0-9-]+\.)+[A-Za-z0-9-]+' \
	'@([A-Za-z0-9-]+\.)+[A-Za-z0-9-]+' \
	"$catalog_files"

# Schemeless FQDN: three or more dotted labels ending in an alphabetic TLD,
# not preceded by a path/URL/placeholder character and not in JSON-key
# position (settings keys such as catalog.search.provider are dotted too).
fqdn='([A-Za-z0-9-]+\.){2,}[A-Za-z]{2,63}'
check_hosts \
	"non-reserved bare hostname in a scenario catalog" \
	"(^|[^A-Za-z0-9._\$/@:\\\\-])${fqdn}(\"([^:]|\$)|[^A-Za-z0-9._\":-]|\$)" \
	"(^|[^A-Za-z0-9._\$/@:\\\\-])${fqdn}(\"([^:]|\$)|[^A-Za-z0-9._\":-]|\$)" \
	"$catalog_files"

# Non-loopback IPv4 literal (first octet 1-126 or 128-255), anchored on
# non-digit context rather than \b so BSD and GNU regex agree; backslashes
# before a dot allow regex-escaped literals inside "matches" predicates.
check_pattern \
	"non-loopback IPv4 literal in a scenario catalog" \
	'(^|[^0-9.])([1-9]|[1-9][0-9]|1[0-1][0-9]|12[0-689]|1[3-9][0-9]|2[0-4][0-9]|25[0-5])([\\]*\.[0-9]{1,3}){3}([^0-9.]|$)' \
	"$catalog_files"

check_pattern \
	"credential-looking literal in a scenario catalog" \
	'(sa_[0-9a-f]{64}|eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----)' \
	"$catalog_files"

if [[ "$failed" -ne 0 ]]; then
	printf '%s\n' "Remove local machine paths from committed content. Use repository-relative paths instead." >&2
	exit 1
fi
