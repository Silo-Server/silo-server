#!/usr/bin/env bash
# Analyze the Go packages a branch touched and report findings on changed lines.
# Local runs include committed, staged, unstaged and untracked edits and enable
# gocritic. CI sets LINT_CHANGED_CI=1 because it runs gocritic full-tree separately.
#
# Usage: scripts/lint-changed.sh [golangci-lint flags...]
# BASE_REF defaults to origin/main. LINT_CHANGED_BASE_MODE defaults to merge-base
# for PR/local comparisons; revision compares exactly BASE_REF (a push's before
# SHA). An unavailable comparison runs unfiltered lint over the whole tree.
set -euo pipefail

base_mode=${LINT_CHANGED_BASE_MODE:-merge-base}
ci_mode=${LINT_CHANGED_CI:-0}
case "$base_mode" in
	merge-base | revision) ;;
	*) echo "lint-changed: invalid LINT_CHANGED_BASE_MODE: $base_mode" >&2; exit 2 ;;
esac
case "$ci_mode" in
	0 | 1) ;;
	*) echo "lint-changed: LINT_CHANGED_CI must be 0 or 1" >&2; exit 2 ;;
esac
if [[ "$base_mode" == revision ]]; then
	base_ref=${BASE_REF-}
else
	base_ref=${BASE_REF:-origin/main}
fi

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"
scratch=$(mktemp -d "${TMPDIR:-/tmp}/silo-lint-changed.XXXXXX")
trap 'rm -rf "$scratch"' EXIT

run_lint() {
	if [[ "$ci_mode" == 0 ]]; then
		golangci-lint run --enable gocritic "$@"
	else
		golangci-lint run "$@"
	fi
}

comparison=
if [[ "$base_mode" == merge-base ]]; then
	if ! comparison=$(git merge-base "$base_ref" HEAD); then
		echo "lint-changed: cannot find merge base for $base_ref; analyzing all packages" >&2
	fi
else
	# A missing before object (for example after a force push), or an all-zero
	# before SHA on a new branch, cannot provide a safe changed-line filter.
	if ! comparison=$(git rev-parse --verify --end-of-options "${base_ref}^{commit}"); then
		echo "lint-changed: cannot resolve previous revision $base_ref; analyzing all packages" >&2
	fi
fi
if [[ -z "$comparison" ]]; then
	run_lint "$@" ./...
	exit
fi

# Keep command failures observable: process substitutions can silently discard
# a failed git/go command's exit status. NUL records preserve unusual filenames.
git diff --color=never --no-ext-diff --no-textconv --no-renames --name-only -z "$comparison" -- >"$scratch/tracked"
git ls-files --others --exclude-standard -z >"$scratch/untracked"
cat "$scratch/tracked" "$scratch/untracked" >"$scratch/changed"
git diff --color=never --no-ext-diff --no-textconv --no-renames --diff-filter=D --name-only -z "$comparison" -- '*.go' >"$scratch/deleted-go"

full_tree=0
unsafe_paths=0
: >"$scratch/dirs"
while IFS= read -r -d '' file; do
	go_input=1
	case "$file" in
		go.mod | go.sum | go.work | go.work.sum | */go.mod | */go.sum | */go.work | */go.work.sum | \
		.golangci.* | */.golangci.* | Makefile | .go-version | .github/actions/setup-go/* | .github/workflows/* | \
		scripts/lint-changed.sh | internal/routeinventory/lintrules/* | vendor/* | \
		*.c | *.cc | *.cpp | *.cxx | *.h | *.hh | *.hpp | *.s | *.S | *.swig | *.swigcxx)
			full_tree=1 ;;
		*.go)
			dir=${file%/*}
			[[ "$dir" != "$file" ]] || dir=.
			printf '%s\n' "$dir" >>"$scratch/dirs" ;;
		contracts/* | migrations/* | web/dist/* | internal/* | cmd/* | pkg/*)
			# Non-Go inputs in Go source trees can be embedded or influence cgo.
			# Directory embeds include Markdown and other documentation too.
			full_tree=1 ;;
		*) go_input=0 ;;
	esac
	# Only Go inputs participate in package patterns or changed-line reporting.
	# An unusual documentation filename must not enroll unrelated Go packages.
	if [[ "$go_input" == 1 && ! "$file" =~ ^[a-zA-Z0-9_./-]+$ ]]; then
		unsafe_paths=1
	fi
done <"$scratch/changed"

if [[ "$unsafe_paths" == 1 ]]; then
	echo "lint-changed: unusual filenames require unfiltered full-tree analysis" >&2
	run_lint "$@" ./...
	exit
fi

# Deleting or moving a package/declaration can break importers elsewhere. Keep
# the no-renames deletion list for package scope, but retain rename metadata in
# the patch so an unchanged move does not report inherited findings as new.
[[ ! -s "$scratch/deleted-go" ]] || full_tree=1
git diff --color=never --no-ext-diff --no-textconv --find-renames --src-prefix=a/ --dst-prefix=b/ --unified=0 "$comparison" -- '*.go' >"$scratch/changes.patch"
while IFS= read -r -d '' file; do
	[[ "$file" == *.go ]] || continue
	# --no-index returns 1 when it successfully finds a difference; other
	# failures must stop lint instead of dropping the untracked file.
	if git diff --no-index --color=never --no-ext-diff --no-textconv --src-prefix=a/ --dst-prefix=b/ --unified=0 -- /dev/null "$file" >>"$scratch/changes.patch"; then
		:
	else
		status=$?
		[[ "$status" == 1 ]] || exit "$status"
	fi
done <"$scratch/untracked"

pkgs=()
if [[ "$full_tree" == 1 ]]; then
	pkgs=(./...)
else
	sort -u "$scratch/dirs" >"$scratch/changed-dirs"
	if [[ -s "$scratch/changed-dirs" ]]; then
		# -e leaves source/type failures for golangci-lint to report, while the
		# command's own failure still aborts. Dir handles the module root without
		# relying on module-name prefixes. ./... excludes testdata/deleted packages.
		go list -e -f '{{.Dir}}' ./... >"$scratch/packages"
		while IFS= read -r dir; do
			if [[ "$dir" == "$repo_root" ]]; then
				rel=.
			elif [[ "$dir" == "$repo_root/"* ]]; then
				rel=${dir#"$repo_root/"}
			else
				continue
			fi
			if grep -qxF -- "$rel" "$scratch/changed-dirs"; then
				[[ "$rel" != . ]] || rel=
				pkgs+=("./${rel}")
			fi
		done <"$scratch/packages"
	fi
fi

if [[ ${#pkgs[@]} -eq 0 ]]; then
	echo "lint-changed: no changed Go packages since $base_ref"
	exit 0
fi
if [[ "$full_tree" == 1 ]]; then
	echo "lint-changed: shared or deleted Go inputs require full-tree analysis since $base_ref" >&2
else
	echo "lint-changed: ${#pkgs[@]} package(s) changed since $base_ref" >&2
fi
run_lint --new-from-patch="$scratch/changes.patch" "$@" "${pkgs[@]}"
