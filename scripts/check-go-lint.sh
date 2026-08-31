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

if [[ "$cached" -eq 1 ]]; then
	mapfile -t go_files < <(git diff --cached --name-only --diff-filter=ACM -- '*.go')
else
	mapfile -t go_files < <(git diff --name-only --diff-filter=ACM -- '*.go')
fi

if [[ ${#go_files[@]} -eq 0 ]]; then
	exit 0
fi

failed=0

unformatted=$(gofmt -l "${go_files[@]}" 2>&1) || true
if [[ -n "$unformatted" ]]; then
	printf '%s\n' "gofmt is required on:" >&2
	printf '%s\n\n' "$unformatted" >&2
	printf '%s\n' "Fix with: gofmt -w <file>" >&2
	failed=1
fi

if command -v golangci-lint >/dev/null 2>&1; then
	base_ref=$(git merge-base HEAD origin/main 2>/dev/null || git merge-base HEAD main 2>/dev/null || true)
	if [[ -n "$base_ref" ]]; then
		if ! golangci-lint run --new-from-rev="$base_ref" ./...; then
			failed=1
		fi
	else
		printf '%s\n' "skipping golangci-lint: could not resolve merge base with main" >&2
	fi
else
	printf '%s\n' "skipping golangci-lint: not installed. Install with:" >&2
	printf '%s\n' "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2" >&2
fi

if [[ "$failed" -ne 0 ]]; then
	exit 1
fi
