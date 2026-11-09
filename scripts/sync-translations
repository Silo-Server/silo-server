#!/bin/sh
# sync-translations: installs silo-translations output into a client repository.
# Vendored by each client as scripts/sync-translations; keep copies identical to
# Silo-Server/silo-translations tools/sync-translations.sh.
#
# The client keeps .silo-translations at its root:
#   platform=web|apple|android|roku
#   version=<release version, or local-<commit> for a local build>
#   sha256=<SHA-256 of the release tarball, or - for a local build>
#
# Usage, from anywhere inside the client repository:
#   scripts/sync-translations                         reinstall the pinned release
#   scripts/sync-translations --version <v>           move to release <v> (pins its checksum)
#   scripts/sync-translations --local <checkout>      generate from a silo-translations checkout
#   scripts/sync-translations --platform <p> ...      first run, before .silo-translations exists
#
# Downloads use `gh release download` (the repository is private; needs gh auth with read
# access). Set SILO_TRANSLATIONS_PUBLIC=1 to use plain curl once the repository is public.
# Nothing in the client changes until the download is verified and unpacked; the
# .silo-translations file is written last.
set -eu

REPO=Silo-Server/silo-translations
BASE_URL=${SILO_TRANSLATIONS_BASE_URL:-https://github.com/$REPO/releases/download}

die() { echo "sync-translations: $*" >&2; exit 1; }

sha256() {
  if command -v sha256sum >/dev/null 2>&1; then sha256sum "$1" | cut -d' ' -f1
  else shasum -a 256 "$1" | cut -d' ' -f1; fi
}

root=${SILO_TRANSLATIONS_ROOT:-$(git rev-parse --show-toplevel)}
config=$root/.silo-translations
platform= version= sha= local=

if [ -f "$config" ]; then
  platform=$(sed -n 's/^platform=//p' "$config")
  version=$(sed -n 's/^version=//p' "$config")
  sha=$(sed -n 's/^sha256=//p' "$config")
fi

while [ $# -gt 0 ]; do
  case $1 in
    --platform) platform=${2:?}; shift 2 ;;
    --version) version=${2:?}; sha=; shift 2 ;;
    --local) local=${2:?}; shift 2 ;;
    *) die "unknown argument $1" ;;
  esac
done

case $platform in web|apple|android|roku) ;; *) die "set --platform to web, apple, android or roku" ;; esac

tmp=$(mktemp -d)
trap 'rm -rf "$tmp"' EXIT INT TERM
mkdir "$tmp/tree"

if [ -n "$local" ]; then
  [ -f "$local/locales.json" ] || die "$local is not a silo-translations checkout"
  make -s -C "$local" generate OUT="$tmp/gen" >&2
  version=$(cat "$tmp/gen/.silo-translations-out")
  sha=-
  cp -R "$tmp/gen/$platform/." "$tmp/tree/"
else
  [ -n "$version" ] || die "no version pinned; pass --version <v>"
  case $version in local-*) die "pinned to a local build ($version); rerun with --local <checkout> or --version <release>" ;; esac
  asset=silo-translations-$platform-$version.tar.gz
  if [ "${SILO_TRANSLATIONS_PUBLIC:-}" = 1 ]; then
    curl -fsSL -o "$tmp/$asset" "$BASE_URL/v$version/$asset"
    [ -n "$sha" ] || curl -fsSL -o "$tmp/SHA256SUMS" "$BASE_URL/v$version/SHA256SUMS"
  else
    gh release download "v$version" -R "$REPO" -D "$tmp" -p "$asset"
    [ -n "$sha" ] || gh release download "v$version" -R "$REPO" -D "$tmp" -p SHA256SUMS
  fi
  if [ -z "$sha" ]; then
    sha=$(awk -v f="$asset" '$2 == f || $2 == "*" f { print $1 }' "$tmp/SHA256SUMS")
    [ -n "$sha" ] || die "SHA256SUMS of v$version has no entry for $asset"
  fi
  got=$(sha256 "$tmp/$asset")
  [ "$got" = "$sha" ] || die "checksum mismatch for $asset: expected $sha, got $got"
  # Only relative regular files and directories, nothing that climbs out of the tree.
  tar -tvzf "$tmp/$asset" | awk '{ t = substr($1, 1, 1); if (t != "-" && t != "d") { print; bad = 1 } } END { exit bad }' \
    || die "$asset contains links or special files"
  tar -tzf "$tmp/$asset" | grep -E '^/|(^|/)\.\.(/|$)' && die "$asset contains unsafe paths"
  tar -xzf "$tmp/$asset" -C "$tmp/tree"
fi

cp -R "$tmp/tree/." "$root/"
printf 'platform=%s\nversion=%s\nsha256=%s\n' "$platform" "$version" "$sha" > "$config.tmp"
mv "$config.tmp" "$config"
echo "sync-translations: $platform $version installed" >&2
