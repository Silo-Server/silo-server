#!/usr/bin/env bash
# Fork invariants: fail loudly when an upstream merge (or local edit) breaks a
# deliberate Vio divergence. See docs/architecture/fork-divergence.md.
# Commands assume the repository root is the cwd.
set -uo pipefail

fail=0
pass() { echo "ok   - $1"; }
reject() { echo "FAIL - $1"; fail=1; }

# 1. Web client identifies as Vio Web.
grep -q '"X-Vio-Client": "Vio Web"' web/src/api/v2/request.ts \
  && pass "Vio client headers" \
  || reject "Vio client headers missing from web/src/api/v2/request.ts"
grep -q '"vio web"' internal/apiv2/observe.go \
  && pass "Vio telemetry labels" \
  || reject "Vio telemetry labels missing from internal/apiv2/observe.go"

# 2. No virtual→plugin dispatch outside the plugins package itself (tests
# excluded). Virtual resolution is core-only.
dispatch=$(grep -rn --include='*.go' \
  -e 'ResolveVirtualPlaybackForInstallation' \
  -e 'ListVirtualPlaybackStreamsForInstallation' \
  -e 'RefreshVirtualPlaybackForInstallation' \
  -e 'ResolveVirtualPlaybackDetailedForInstallation' \
  -e 'ConfiguredVirtualVariants' \
  internal/ cmd/ \
  | grep -v '_test.go' \
  | grep -v 'internal/plugins/virtual_playback' \
  | grep -v -E ':[0-9]+:\s*//' \
  || true)
if [ -z "$dispatch" ]; then
  pass "core-only virtual dispatch"
else
  reject "virtual→plugin dispatch callers remain: $dispatch"
fi
if grep -Eq 'PluginHTTPProxy[[:alnum:]_.]*ResolveVirtualMedia' internal/api/router.go internal/api/virtual_media_resolver.go 2>/dev/null; then
  reject "playback still falls back to a plugin virtual resolver"
else
  pass "no plugin virtual resolver fallback"
fi

# 3. Core virtual router installation (0) validates; dormant saves are 422s.
grep -q 'id < 0' internal/apiv2/admin_requests.go \
  && pass "installation 0 validates" \
  || reject "installation 0 rejected in internal/apiv2/admin_requests.go"
# Assert each declaration itself, not any text in the file. A comment carrying
# the expected pattern must not satisfy the check, and the save and options
# bodies must be rejected independently.
grep -E '^[[:space:]]*InstallationID[[:space:]]+ID[[:space:]]+`json:"installation_id"' internal/apiv2/admin_requests.go \
  | grep -Fq 'pattern:"^(0|[1-9][0-9]*)$"' \
  && pass "installation 0 passes the v2 save schema" \
  || reject "installation 0 rejected by the v2 save schema"
grep -E '^[[:space:]]*InstallationID[[:space:]]+\*ID[[:space:]]+`json:"installation_id,omitempty"' internal/apiv2/admin_requests.go \
  | grep -Fq 'pattern:"^(0|[1-9][0-9]*)$"' \
  && pass "installation 0 passes the v2 options schema" \
  || reject "installation 0 rejected by the v2 options schema"
grep -q 'ErrProviderUnavailable' internal/apiv2/admin_collections.go \
  && pass "sync provider-unavailable mapping" \
  || reject "503 mapping missing from internal/apiv2/admin_collections.go"

# 4. Onboarding additions are wired.
grep -q 'RequestsStep' web/src/pages/SetupWizard.tsx \
  && grep -q '"requests"' web/src/pages/setup-wizard/setupStorage.ts \
  && pass "wizard requests step" \
  || reject "wizard requests step unwired"
grep -q 'MetadataProvidersSection' web/src/pages/setup-wizard/steps/LibraryStep.tsx \
  && pass "wizard metadata auto-install" \
  || reject "metadata auto-install missing from LibraryStep"

# 5. Indexer secrets use SecretField with a clear path.
grep -q 'SecretField' web/src/pages/admin-settings/StreamingSettings.tsx \
  && grep -q 'virtual_library.indexer_api_key' web/src/pages/admin-settings/StreamingSettings.tsx \
  && pass "indexer SecretField rows" \
  || reject "indexer SecretField rows missing from StreamingSettings"

# 6. New collections default virtual playback on at the creation API boundary
# (resolveVirtualPlayback: nil means on). The old adminCollectionsShared add
# forms were replaced by upstream's CollectionEditor revamp, which has no
# toggle yet — the server default is what guarantees the behavior.
grep -q 'func resolveVirtualPlayback(v \*bool) bool' internal/api/handlers/library_collections.go \
  && grep -q 'if v == nil {' internal/api/handlers/library_collections.go \
  && pass "virtual_playback creation default" \
  || reject "virtual_playback creation default flipped off"

# 7. Image build pipeline: BuildKit frontend pruning, unshadowed Go module layer caching, Go compiler cache persistence, single-runner manual frontend builds, and pinned base image versions (a floating tag bump silently invalidates the layer cache and the Go build cache).
grep -Eq 'FROM node:22(\.[0-9]+){2}-slim AS node-base' Dockerfile \
  && grep -Eq 'COPY --from=(node-base|jellyfin_web_node) /usr/local/bin/node' Dockerfile \
  && pass "Dockerfile decoupled node-base stage" \
  || reject "Dockerfile missing decoupled node-base stage for BuildKit frontend pruning"

# The build stage must use exactly the toolchain go.mod requires: a floating
# tag (golang:1.27) bumps silently and invalidates the layer and Go build
# caches, and a lagging one cannot build the module at all because the Go
# image, like setup-go, runs with GOTOOLCHAIN=local.
go_directive="$(awk '$1 == "go" { print $2; exit }' go.mod)"
grep -Eq "^FROM golang:${go_directive//./\\.} AS build$" Dockerfile \
  && pass "build stage Go toolchain pinned to the go.mod version" \
  || reject "build stage golang tag is not the patch release go.mod requires (${go_directive:-unreadable go.mod})"
grep -Eq '^FROM debian:trixie-[0-9]{8}-slim$' Dockerfile \
  && pass "runtime stage Debian image pinned" \
  || reject "runtime stage debian tag not pinned to a dated trixie snapshot"

if grep -E -q -- '--mount=type=cache.*target=/go/pkg/mod' Dockerfile; then
  reject "Dockerfile contains --mount=type=cache targeting /go/pkg/mod (shadows Go module layer cache)"
else
  pass "Dockerfile unshadowed Go module cache"
fi

grep -q 'reproducible-containers/buildkit-cache-dance' .github/workflows/docker.yml \
  && grep -q '/root/\.cache/go-build' .github/workflows/docker.yml \
  && pass "docker workflow persists Go compiler cache via buildkit-cache-dance" \
  || reject "docker workflow missing buildkit-cache-dance Go compiler cache persistence"

if awk '/^on:/{flag=1; next} /^[a-z]/{flag=0} flag {print}' .github/workflows/docker.yml | grep -q 'push:'; then
  reject "docker workflow contains stale on: push trigger"
else
  pass "docker workflow push trigger pruned"
fi

grep -q 'frontend-dist-' .github/workflows/docker.yml \
  && grep -Eq 'actions/download-artifact@v[0-9]+' .github/workflows/docker.yml \
  && pass "docker workflow deduplicated manual frontend builds" \
  || reject "docker workflow missing deduplicated manual frontend build artifact handoff"

grep -q 'type=raw,value=dev' .github/workflows/docker.yml \
  && pass "docker workflow publishes :dev for default branch" \
  || reject "docker workflow missing :dev tag for default branch"

grep -q 'ghcr.io/kipsilabs/vio:dev' docker-compose.yml \
  && pass "docker-compose defaults to vio:dev" \
  || reject "docker-compose default image overwritten from vio:dev"

grep -q 'ghcr.io/kipsilabs/vio:dev' .env.example \
  && pass ".env.example defaults to vio:dev" \
  || reject ".env.example default image overwritten from vio:dev"

if grep -q '^\.github/workflows/docker\.yml merge=ours$' .gitattributes 2>/dev/null; then
  pass "docker.yml merge=ours preserved in .gitattributes"
else
  reject "docker.yml merge=ours entry missing from .gitattributes"
fi

# 8. Audio selection: MULTi trackHasLanguage and MatchRank preserved.
if grep -q 'trackHasLanguage' internal/playback/audio_select.go \
  && grep -q 'MatchRank' internal/lang/lang.go; then
  pass "audio selection MULTi trackHasLanguage and MatchRank preserved"
else
  reject "audio selection MULTi trackHasLanguage or MatchRank missing from internal/playback/audio_select.go or internal/lang/lang.go"
fi
# 9. Contribution docs keep the pre-upstream-#2016 evidence policy.
if grep -q '^### Show visible changes' CONTRIBUTING.md; then
  reject "upstream 'Show visible changes' gate reintroduced in CONTRIBUTING.md (see docs/architecture/fork-divergence.md, upstream PR #2016)"
else
  pass "no Show-visible-changes gate in CONTRIBUTING.md"
fi
if grep -q 'attach them only when explicitly requested' CONTRIBUTING.md \
  && grep -q 'Do not capture screenshots or record videos just to prepare a PR' AGENTS.md \
  && ! grep -q '## Evidence' .github/PULL_REQUEST_TEMPLATE.md \
  && ! grep -q 'Changes that alter what users see need' docs/ai-contributions.md \
  && [ ! -e .macroscope/check-run-agents/visible-change-evidence.md ]; then
  pass "pre-#2016 evidence policy (attach on request) intact"
else
  reject "pre-#2016 evidence policy altered (CONTRIBUTING.md / AGENTS.md / .github/PULL_REQUEST_TEMPLATE.md / docs/ai-contributions.md / .macroscope agent)"
fi

# The merge driver itself is per-clone setup (see `make install-hooks`), not
# committed content: fresh CI checkouts never have it. Report, don't fail —
# verification must stay read-only and green on a clean clone.
driver="$(git config merge.ours.driver 2>/dev/null || true)"
if [ "$driver" = "true" ]; then
  pass "git merge.ours.driver configured for .gitattributes"
else
  echo "warn  - git merge.ours.driver not configured (run: make install-hooks)"
fi

if [ "$fail" -ne 0 ]; then
  echo "fork invariants BROKEN — see docs/architecture/fork-divergence.md" >&2
  exit 1
fi

# Working-tree formatting: CI gates gofmt and prettier --check, and the repo
# pre-commit hook does not. Catch it here before pushing, not after.
if [ -n "$(gofmt -l internal cmd 2>/dev/null)" ]; then
  reject "gofmt clean: $(gofmt -l internal cmd 2>/dev/null | tr '\n' ' ')"
else
  pass "gofmt clean"
fi
if [ -d web/node_modules ]; then
  if pnpm --dir web run format:check >/dev/null 2>&1; then
    pass "prettier clean"
  else
    reject "prettier clean (run: pnpm --dir web exec prettier --write <files>)"
  fi
else
  echo "skip  - prettier clean (web/node_modules absent)"
fi

if [ "$fail" -ne 0 ]; then
  echo "fork invariants BROKEN — see docs/architecture/fork-divergence.md" >&2
  exit 1
fi
echo "fork invariants hold"
