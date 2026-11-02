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
scratch=$(mktemp -d "${TMPDIR:-/tmp}/silo-check-go-lint.XXXXXX")
trap 'rm -rf "$scratch"' EXIT

diff_args=(--name-only -z)
if [[ "$cached" -eq 1 ]]; then
	diff_args=(--cached "${diff_args[@]}")
fi

# NUL-delimited, so names git would quote (an accented one, say) come through
# literally. A read loop rather than mapfile, which macOS's Bash 3.2 lacks;
# "${array[@]+...}" below for the same reason, as Bash before 4.4 treats an
# empty array as unset under set -u. R keeps an edited rename in the check,
# and T includes regular files replaced by symlinks (or the reverse).
# Materialize Git output so command failures cannot masquerade as no changes.
git diff "${diff_args[@]}" --diff-filter=ACMRT -- '*.go' >"$scratch/go-files"
go_files=()
while IFS= read -r -d '' file; do
	go_files+=("$file")
done <"$scratch/go-files"

# Deleting Go files can break the code that's left, so deletions alone still
# run the lint gate; there's nothing of theirs to format.
deleted=0
git diff "${diff_args[@]}" --diff-filter=D -- '*.go' >"$scratch/deleted-go"
while IFS= read -r -d '' _; do
	deleted=1
done <"$scratch/deleted-go"

if [[ ${#go_files[@]} -eq 0 && "$deleted" -eq 0 ]]; then
	exit 0
fi

failed=0

if [[ "$cached" -eq 1 ]]; then
	# The commit contains the staged blobs, so format-check those rather than
	# the working tree, which may hold unstaged edits.
	unformatted=""
	# A blob that can't be read fails the check rather than passing as empty
	# input (pipefail carries git show's failure).
	for file in ${go_files[@]+"${go_files[@]}"}; do
		if ! out=$(git show ":$file" | gofmt -l 2>&1) || [[ -n "$out" ]]; then
			unformatted+="$file"$'\n'
		fi
	done
elif [[ ${#go_files[@]} -gt 0 ]]; then
	unformatted=$(gofmt -l "${go_files[@]}" 2>&1) || true
else
	unformatted=""
fi
if [[ -n "$unformatted" ]]; then
	printf '%s\n' "gofmt is required on:" >&2
	printf '%s\n' "$unformatted" >&2
	printf '%s\n' "Fix with: gofmt -w <file>" >&2
	failed=1
fi

if command -v golangci-lint >/dev/null 2>&1; then
	lint_env=()
	# The delegate includes the whole branch and reads dependencies, module
	# files, lint configuration and embedded inputs from the working tree.
	# Staged package directories alone cannot establish that it checks the
	# commit. Require all tracked and nonignored inputs to match the index.
	if [[ "$cached" -eq 1 ]]; then
		git diff --name-only --no-renames -z -- >"$scratch/unstaged"
		git ls-files --others --exclude-standard -z >"$scratch/untracked"
		# Developer-local workspaces are ignored by Git and absent from CI.
		lint_env=(GOWORK=off)
	fi
	if [[ "$cached" -eq 1 ]] && [[ -s "$scratch/unstaged" || -s "$scratch/untracked" ]]; then
		# With the working tree differing from the commit, lint would pass a
		# broken commit or block a good one, so refuse rather than guess.
		printf '%s\n' "The working tree has unstaged or untracked files; the lint gate cannot check the commit as staged." >&2
		printf '%s\n' "Stage them or move them aside (git stash push --keep-index --include-untracked) and commit again." >&2
		failed=1
	elif ! git rev-parse --verify --quiet "${BASE_REF:-origin/main}" >/dev/null; then
		# lint-changed.sh diffs against this ref; without it the lint output
		# would not name the real cause.
		printf '%s\n' "Cannot lint the commit: base ref '${BASE_REF:-origin/main}' is missing. Run: git fetch origin main (or set BASE_REF)." >&2
		failed=1
	elif ! env ${lint_env[@]+"${lint_env[@]}"} "$repo_root/scripts/lint-changed.sh"; then
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
