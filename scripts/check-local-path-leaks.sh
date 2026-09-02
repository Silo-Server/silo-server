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

# Scenario catalogs are treated as public fixtures: no machine paths, no
# non-reserved hosts, no credential-looking values. Fixture identities use
# reserved origins (example.test) and runtime ${...} placeholders.
check_pattern \
	"absolute local filesystem path in a scenario catalog" \
	'(/Users/[^[:space:]"]+|/home/[^[:space:]"]+|/Volumes/[^[:space:]"]+|/var/folders/[^[:space:]"]+|/private/tmp/[^[:space:]"]+|[A-Za-z]:\\Users\\[^[:space:]"]+)' \
	contracts/api/v2/scenarios

# ERE has no lookaround, so the host check is an allowlist in two steps:
# any URL/IP literal outside the reserved set is a failure.
check_pattern \
	"non-reserved URL host in a scenario catalog" \
	'https?://([A-Za-z0-9-]+\.)*(([A-Za-z0-9-]+\.)(com|net|org|io|dev|app|local|lan|internal|home|tv|me|co|uk|de))([/:"]|$)' \
	contracts/api/v2/scenarios

check_pattern \
	"non-loopback IPv4 literal in a scenario catalog" \
	'\b([1-9]|[1-9][0-9]|1[0-1][0-9]|12[0-689]|1[3-9][0-9]|2[0-4][0-9]|25[0-5])(\.[0-9]{1,3}){3}\b' \
	contracts/api/v2/scenarios

check_pattern \
	"credential-looking literal in a scenario catalog" \
	'(sa_[0-9a-f]{64}|eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----)' \
	contracts/api/v2/scenarios

if [[ "$failed" -ne 0 ]]; then
	printf '%s\n' "Remove local machine paths from committed content. Use repository-relative paths instead." >&2
	exit 1
fi
