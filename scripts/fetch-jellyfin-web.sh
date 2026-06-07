#!/usr/bin/env bash

set -euo pipefail

repo_root="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
install_dir="${JELLYFIN_WEB_INSTALL_DIR:-${JELLYFIN_WEB_OUTPUT_DIR:-$repo_root/.local/compat/jellyfin-web}}"
version="${JELLYFIN_WEB_VERSION:-}"
source_url="${JELLYFIN_WEB_REPO:-https://github.com/jellyfin/jellyfin-web.git}"

args=(compat-web install --dir "$install_dir" --source "$source_url")
if [[ -n "$version" ]]; then
  args+=(--version "$version")
fi

cd "$repo_root"
go run ./cmd/silo/ "${args[@]}"
