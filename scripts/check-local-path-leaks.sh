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
# the reserved set, no IPv4/IPv6 literals other than loopback, no
# credential-looking values. Fixture identities use reserved origins
# (silo.example.test) and runtime ${...} placeholders. The catalog bodies
# and the committed generators under tools/ are both scanned for hosts and
# credentials (a generator comment can carry a LAN host as easily as a
# catalog note can); the whole tree, schema file included, is scanned for
# absolute paths. The schema's $schema/$id URLs (json-schema.org,
# siloserver.org) are the one allowed public origin, listed by name.
scenario_tree='contracts/api/v2/scenarios'
catalog_files=':(glob)contracts/api/v2/scenarios/*/*.json'
generator_files=':(glob)contracts/api/v2/scenarios/tools/*.py'

check_pattern \
	"absolute local filesystem path under contracts/api/v2/scenarios" \
	'(/Users/[^[:space:]"]+|/home/[^[:space:]"]+|/Volumes/[^[:space:]"]+|/var/folders/[^[:space:]"]+|/private/tmp/[^[:space:]"]+|[A-Za-z]:\\Users\\[^[:space:]"]+)' \
	"$scenario_tree"

# Public TLDs a schemeless dotted token is judged by: when its last label is
# one of these (any number of labels, so quick104.dev and plex.tv count) it
# is a hostname; dotted settings keys in prose (catalog.search.provider)
# have no such last label and pass. ts.net (Tailscale) is a two-label
# suffix and matched as such.
public_tlds=' com net org io dev app tv cloud xyz me co uk de fr nl se ch ca au nz jp info biz site online tech ai sh gg to ws cc '

# normalize_host strips scheme, userinfo, port, path, regex-escaped dots,
# brackets, and case from a host-shaped token.
normalize_host() {
	local host=$1
	host=${host//\\/}
	host=${host#*://}
	host=${host#//}
	host=${host#*@}
	host=${host#\[}
	host=${host%%\]*}
	host=${host%%/*}
	host=${host%%\?*}
	host=${host%%\#*}
	host=$(printf '%s' "$host" | tr '[:upper:]' '[:lower:]')
	# Drop a :port only when what remains is not itself an IPv6 literal.
	case "$host" in
		*:*:*) ;;
		*) host=${host%%:*} ;;
	esac
	host=${host%.}
	printf '%s' "$host"
}

# reserved_host succeeds for hosts a public fixture may name: RFC 2606 / RFC
# 6761 reserved names, localhost, and the loopback addresses.
reserved_host() {
	local host
	host=$(normalize_host "$1")
	case "$host" in
		127.0.0.1|localhost|::1|*.test|*.example|*.invalid|*.localhost)
			return 0
			;;
	esac
	return 1
}

# public_tld_host succeeds when the token's last label is a public TLD (or
# the token ends in ts.net), which is what makes a bare dotted token a host.
public_tld_host() {
	local host last
	host=$(normalize_host "$1")
	case "$host" in
		*.ts.net) return 0 ;;
	esac
	last=${host##*.}
	case "$public_tlds" in
		*" $last "*) return 0 ;;
	esac
	return 1
}

# check_hosts is an allowlist: every host-shaped token the line pattern finds
# is extracted with the token pattern and must satisfy reserved_host. With
# --tld the token must also have a public TLD before it is judged at all
# (the bare-hostname rule). ERE has no lookaround, so the filtering happens
# here rather than in the regex.
check_hosts() {
	local need_tld=0
	if [[ "$1" == "--tld" ]]; then
		need_tld=1
		shift
	fi
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
			token=$(printf '%s' "$token" | sed -E 's/^[^A-Za-z0-9@/[]//; s/[^]A-Za-z0-9.\\:-]+$//')
			if [[ "$need_tld" -eq 1 ]] && ! public_tld_host "$token"; then
				continue
			fi
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

# The schema's own $schema/$id origins are the only public hosts allowed,
# and only in that file; they are checked by exact value.
check_schema_urls() {
	local file='contracts/api/v2/scenarios/scenario-catalog.schema.json'
	local urls
	if [[ "$cached" -eq 1 ]]; then
		urls=$(git grep --cached -h -o -I -E '[A-Za-z][A-Za-z0-9+.-]*://[^/?#"[:space:]]+' -- "$file" 2>/dev/null) || return 0
	else
		urls=$(git grep -h -o -I -E '[A-Za-z][A-Za-z0-9+.-]*://[^/?#"[:space:]]+' -- "$file" 2>/dev/null) || return 0
	fi
	local url bad=""
	while IFS= read -r url; do
		[[ -z "$url" ]] && continue
		case "$url" in
			https://json-schema.org|https://siloserver.org) ;;
			*) bad+="${file}: ${url}"$'\n' ;;
		esac
	done <<< "$urls"
	if [[ -n "$bad" ]]; then
		printf '%s\n' "local path leak check failed: non-allowlisted URL host in the scenario schema" >&2
		printf '%s\n' "$bad" >&2
		failed=1
	fi
}
check_schema_urls

# Any scheme (http, https, ws, wss, rtsp, ...) followed by an authority, or a
# protocol-relative //authority; the host must be reserved whatever its TLD.
authority='([A-Za-z][A-Za-z0-9+.-]*:)?//[^/?#"[:space:]]+'
check_hosts \
	"non-reserved URL host in a scenario catalog or generator" \
	"$authority" \
	"$authority" \
	"$catalog_files" "$generator_files"

# Mail-style @domain (fixture accounts, invitations); regex-escaped dots
# between labels count as dots.
maildomain='@([A-Za-z0-9-]+[\\]*\.)+[A-Za-z0-9-]+'
check_hosts \
	"non-reserved mail domain in a scenario catalog or generator" \
	"$maildomain" \
	"$maildomain" \
	"$catalog_files" "$generator_files"

# Schemeless dotted token: judged a hostname only when its last label is a
# public TLD (any label count), not preceded by a path/URL/placeholder
# character. Regex-escaped dots are accepted between labels.
fqdn='([A-Za-z0-9-]+[\\]*\.)+[A-Za-z]{2,63}'
check_hosts --tld \
	"non-reserved bare hostname in a scenario catalog or generator" \
	"(^|[^A-Za-z0-9._\$/@:\\\\-])${fqdn}([^A-Za-z0-9._-]|\$)" \
	"(^|[^A-Za-z0-9._\$/@:\\\\-])${fqdn}([^A-Za-z0-9._-]|\$)" \
	"$catalog_files" "$generator_files"

# Non-loopback IPv4 literal (first octet 1-126 or 128-255), anchored on
# non-digit context rather than \b so BSD and GNU regex agree; backslashes
# before a dot allow regex-escaped literals inside "matches" predicates.
check_pattern \
	"non-loopback IPv4 literal in a scenario catalog or generator" \
	'(^|[^0-9.])([1-9]|[1-9][0-9]|1[0-1][0-9]|12[0-689]|1[3-9][0-9]|2[0-4][0-9]|25[0-5])([\\]*\.[0-9]{1,3}){3}([^0-9.]|$)' \
	"$catalog_files" "$generator_files"

# Bracketed IPv6 literal ([2001:db8::1], [fe80::1]:8080); only [::1] is
# loopback and allowed.
ipv6='\[[0-9A-Fa-f:.]*:[0-9A-Fa-f:.]*\]'
check_hosts \
	"non-loopback IPv6 literal in a scenario catalog or generator" \
	"$ipv6" \
	"$ipv6" \
	"$catalog_files" "$generator_files"

check_pattern \
	"credential-looking literal in a scenario catalog or generator" \
	'(sa_[0-9a-f]{64}|eyJ[A-Za-z0-9_-]{20,}\.[A-Za-z0-9_-]{10,}\.[A-Za-z0-9_-]{10,}|AKIA[0-9A-Z]{16}|-----BEGIN [A-Z ]*PRIVATE KEY-----)' \
	"$catalog_files" "$generator_files"

if [[ "$failed" -ne 0 ]]; then
	printf '%s\n' "Remove local machine paths from committed content. Use repository-relative paths instead." >&2
	exit 1
fi
