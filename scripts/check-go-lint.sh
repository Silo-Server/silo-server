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

# R keeps an edited rename in the check.
if [[ "$cached" -eq 1 ]]; then
	mapfile -t go_files < <(git diff --cached --name-only --diff-filter=ACMR -- '*.go')
else
	mapfile -t go_files < <(git diff --name-only --diff-filter=ACMR -- '*.go')
fi

if [[ ${#go_files[@]} -eq 0 ]]; then
	exit 0
fi

failed=0

if [[ "$cached" -eq 1 ]]; then
	# The commit contains the staged blobs, so format-check those rather than
	# the working tree, which may hold unstaged edits.
	unformatted=""
	for file in "${go_files[@]}"; do
		if [[ -n "$(git show ":$file" | gofmt -l 2>&1)" ]]; then
			unformatted+="$file"$'\n'
		fi
	done
else
	unformatted=$(gofmt -l "${go_files[@]}" 2>&1) || true
fi
if [[ -n "$unformatted" ]]; then
	printf '%s\n' "gofmt is required on:" >&2
	printf '%s\n' "$unformatted" >&2
	printf '%s\n' "Fix with: gofmt -w <file>" >&2
	failed=1
fi

if command -v golangci-lint >/dev/null 2>&1; then
	if [[ "$cached" -eq 1 ]] && ! git diff --quiet -- "${go_files[@]}"; then
		# golangci-lint reads the working tree. With unstaged edits to a staged
		# file it would lint something other than the commit, passing a broken
		# commit or blocking a good one, so refuse rather than guess.
		printf '%s\n' "Staged Go files also have unstaged changes; the lint gate cannot check the commit as staged." >&2
		printf '%s\n' "Stage or stash them (git stash --keep-index) and commit again." >&2
		failed=1
	elif ! git rev-parse --verify --quiet "${BASE_REF:-origin/main}" >/dev/null; then
		# lint-changed.sh diffs against this ref; without it the lint output
		# would not name the real cause.
		printf '%s\n' "Cannot lint the commit: base ref '${BASE_REF:-origin/main}' is missing. Run: git fetch origin main (or set BASE_REF)." >&2
		failed=1
	elif ! "$repo_root/scripts/lint-changed.sh"; then
		# lint-changed.sh reports CI's changed-line findings for the touched
		# packages. A type-check failure is a real finding and fails too.
		failed=1
	fi
else
	printf '%s\n' "skipping golangci-lint: not installed. Install with:" >&2
	printf '%s\n' "  go install github.com/golangci/golangci-lint/v2/cmd/golangci-lint@v2.12.2" >&2
fi

if [[ "$failed" -ne 0 ]]; then
	exit 1
fi
