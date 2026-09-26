#!/usr/bin/env bash
# Lint only the Go packages this branch touched, reporting findings on the
# lines it changed. CI's "Lint changed lines" step runs over ./... but can
# only report findings on changed lines, and every changed line lives in one
# of these packages, so the result matches CI while analyzing a fraction of
# the tree. Committed, staged, unstaged and untracked changes all count.
#
# gocritic (the route inventory's ruleguard check) is off in .golangci.yml
# because CI runs it full-tree through make lint-router-recovery; enabling it
# here covers the same rule on the changed packages for a couple of seconds
# each, so a local run needs no full-tree pass.
#
# Usage: scripts/lint-changed.sh [golangci-lint flags...]
# BASE_REF names the branch to compare against (default origin/main).
set -euo pipefail

base_ref=${BASE_REF:-origin/main}

repo_root=$(git rev-parse --show-toplevel)
cd "$repo_root"

merge_base=$(git merge-base "$base_ref" HEAD)

# Directories holding a changed .go file, relative to the repository root.
changed_dirs=$(
	{
		git diff --name-only "$merge_base" -- '*.go'
		git ls-files --others --exclude-standard -- '*.go'
	} | while IFS= read -r file; do dirname "$file"; done | sort -u
)

# Keep the directories go list reports as packages of this module, which
# drops deleted packages, testdata and anything else the go tool ignores.
module=$(go list -m)
pkgs=()
while IFS= read -r pkg; do
	rel=${pkg#"$module"}
	rel=${rel#/}
	[[ -n "$rel" ]] || rel=.
	if grep -qxF -- "$rel" <<<"$changed_dirs"; then
		pkgs+=("./$rel")
	fi
done < <(go list -e -f '{{.ImportPath}}' ./...)

if [[ ${#pkgs[@]} -eq 0 ]]; then
	echo "lint-changed: no changed Go packages since $base_ref"
	exit 0
fi

echo "lint-changed: ${#pkgs[@]} package(s) changed since $base_ref" >&2
exec golangci-lint run --new-from-merge-base="$base_ref" --enable gocritic "$@" "${pkgs[@]}"
