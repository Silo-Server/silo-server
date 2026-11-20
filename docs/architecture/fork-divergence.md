# Fork divergence (Vio)

This repo tracks upstream `Silo-Server/silo-server` with deliberate Vio
divergences. Read this before merging upstream — an upstream change that looks
innocent can silently undo one of these. The merge procedure below plus
`scripts/check-fork-invariants.sh` exist so both humans and AI agents catch
that before it ships.

## Owned divergences

| Area | Divergence | Key files | Pinned by |
| --- | --- | --- | --- |
| Client identity | Web client identifies as `Vio Web` (dual `X-Vio-*` / `X-Silo-*` headers); telemetry labels `vio *` alongside `silo *` | `web/src/api/v2/request.ts`, `web/src/api/client.ts`, `web/src/lib/plexAuth.ts`, `internal/apiv2/observe.go` | `request.test.ts`, `observe_test.go`, invariant script |
| Virtual routing | All virtual-library traffic resolves through core (`internal/virtuallibrary`); no virtual→plugin dispatch anywhere | `internal/api/router.go`, `cmd/silo/main.go`, `internal/catalog/item_repo.go`, `internal/catalog/library_collection_repo.go` | catalog integration tests, `router_virtual_library_core_test.go`, invariant script |
| Virtual network permissions | `virtual_library.allow_insecure_http` covers HTTP manifests on private/local hosts only; `virtual_library.allow_private_streams` separately gates private stream destinations (SSRF bypass on resolved URLs, relay transport, redirects, probes). Both default false; enabling the former does not imply the latter | `internal/virtuallibrary/service.go`, `internal/virtuallibrary/resolve.go`, `internal/api/router.go`, `internal/plugins/virtual_provenance.go`, `cmd/silo/main.go`, `web/src/pages/admin-settings/StreamingSettings.tsx` | `resolve_test.go` boundary cases, `split_allow_private_streams_migration_test.go` |
| Request integrations | `installation_id` 0 (core virtual router) validates; virtual-dormant saves answer 422, not 500 | `internal/apiv2/admin_requests.go`, `internal/requests/service.go` | `service_test.go` |
| Collection sync | Missing virtual provider answers 503, not 500 | `internal/apiv2/admin_collections.go` | `admin_collections_test.go` |
| Onboarding | Requests step, TMDB/TVDB auto-install section, format scoring card | `web/src/pages/setup-wizard/` | step tests |
| Collections | `virtual_playback` defaults on when omitted at the collection-creation API boundary (`resolveVirtualPlayback`, nil means on); add and template forms default on | `internal/api/handlers/library_collections.go`, `internal/api/handlers/admin_collections_service.go`, `internal/adminjob/template_bundle_apply.go`, `web/src/pages/AdminRequests.tsx` | `internal/api/handlers/library_collections_test.go`, `internal/adminjob/template_bundle_apply_test.go`, `scripts/check-fork-invariants.sh`, step tests |
| Settings secrets | Indexer keys use `SecretField` (configured indicator + clear) | `web/src/pages/admin-settings/StreamingSettings.tsx` | `StreamingSettings.test.tsx` |
| Settings checks | `virtual_library` check kind; safe messages | `internal/api/handlers/admin_settings_checks*.go` | `admin_settings_check_service_test.go` |
| Monitor registrar | Library-scoped registration, episode URIs, plugin-path fallbacks | `internal/virtuallibrary/registrar.go`, `monitor/monitor.go` | virtuallibrary suites |
| Plugin SDK | Upstream SDK v0.21.0, no replacement; fork virtual plugin RPCs retired; virtual registration/resolution remain core-owned (`internal/virtuallibrary`, `internal/catalog/virtual_media.go`); network-access provider support follows upstream. | `go.mod`, `go.sum` | requests suites |
| V3 transcode planner | Vio's ladder/HEVC/output-check planner (`targetVideoSelectionV3`, `CappedRungHeightV3` shared with the virtual picker, QSV `-1` width, 2160p min-clamp, 420p/328p rungs) instead of upstream's shared-ladder rework (#1659: `bitrateLadder`, decoder-bounded H264, generalized `scaleTargetHeight` switches). Portable #1659 pieces taken: `scaleTargetHeight`/`vaapiNV12Filter` helpers, `softwareEncode` rename, server-cap audio reserve, `anchorRef` menu wiring, exact ladder-fit heights (`800p` bubbles, `heightLabel`) in the quality resolver. The hardware scalers (`vaapiScaleFilter`, `qsvScaleFilterWithMapMode`, `qsvVPPInputScaleFilter`) scale exact heights in their defaults — a plan/filer mismatch here encodes at source size while the plan advertises the fit, so any future filter must parse `scaleTargetHeight` too. Removed #1659 planner-behavior tests are replaced by Vio equivalents: `TestHardwareScaleFiltersShareExactLadderHeights`, `TestScopeFilmAutoPlanScalesOnHardwareFilters` plus the retained HEVC fallback/decoder tests in `hevc_transcode_v3_test.go`. | `internal/playback/plan_v3.go`, `internal/playback/transcode.go`, `internal/api/handlers/playback_virtual.go` | playback suites (Vio ladder/HEVC/output-check tests) |
| Audio selection | MULTi track awareness (`track.Languages`, `trackHasLanguage`, `trackLanguageRank`), cross-version language preservation, and fine-grained regional/script discrimination | `internal/playback/audio_select.go`, `internal/lang/lang.go` | `internal/playback/audio_select_test.go`, `internal/api/handlers/playback_virtual_languages_test.go`, `internal/jellycompat/jelly_gap_test.go` |
| Image builds | Decoupled `node-base` stage for BuildKit frontend pruning, unshadowed Go module layer caching, persisted Go compiler cache (`buildkit-cache-dance`), deduplicated `workflow_dispatch` frontend builds, base images pinned to patch/dated tags, skipped image attestations, and publishing default-branch images as `:dev` with `:latest` reserved for stable releases or manual promotion | `Dockerfile`, `.github/workflows/docker.yml`, `docker-compose.yml`, `.env.example` | invariant script |
| Image tags | The fork publishes the BASE image (no bundled PostgreSQL/Redis/Meilisearch) under the plain `:sha`, `:dev`, `:latest`, `:build-N`, and `:apiv2` tags, because the omnibus jobs that own those tags upstream are gated on `Silo-Server/silo-server` and never run here. `vars.ENABLE_OMNIBUS` (unset = off) keeps the plain tags on the base `merge` job; setting it to `true` disables them there and admits the omnibus pipeline, so the two never race for the bare SHA. `:sha-base` is always the base image and feeds the omnibus build | `.github/workflows/docker.yml` | invariant script (`:dev` tag), merge-procedure review |
| Subtitle fonts | Fork-only font extraction in `subtitle_fonts.go`: payload-scaled attachment dump budgets (`subtitleFontDumpBudget`), ASS-referenced-family subsetting when a pack exceeds the bundle ceiling (`assReferencedFontFamilies`, `dumpFontAttachmentsSubset`), and the `ffprobeBinary` fallback when no ffmpeg path is configured. The upstream `videoStream` parameter (first video stream, `-1` when absent) is threaded through `probeFontAttachmentStreams`, `dumpFontAttachments`, `dumpFontAttachmentsSubset`, and `attachmentDumpOutputArgs`; an upstream sync touching this file must keep both | `internal/playback/subtitle_fonts.go` | `internal/playback/subtitle_fonts_test.go` (`TestExtractAttachedSubtitleFontsSupportsPacksAboveOldCap`, `TestExtractAttachedSubtitleFontsSelectsReferencedSubset`, `TestExtractAttachedSubtitleFontsSubsetRespectsCeiling`) |
| Upstream non-goals | Not adopted: the fork may explore remote/debrid-backed library integrations (Live TV/IPTV/DVR, `.strm`-style remote-URL playback) that upstream permanently rejects. Upstream non-goals changes must not silently reimpose scope here; the Apple/Google store risk stands and any shipped integration owns it | `AGENTS.md` (Non-goals section) | merge-procedure review |
| Contribution docs | Pre-upstream-#2016 evidence policy kept: screenshots/recordings only when explicitly requested; upstream's `Show visible changes` gate, the PR-template Evidence section, and the visible-change check agent are not carried; `docs/ai-contributions.md` keeps the same carving. Upstream CONTRIBUTING/AGENTS/template merges that reintroduce them must be re-reverted | `CONTRIBUTING.md`, `AGENTS.md`, `.github/PULL_REQUEST_TEMPLATE.md`, `docs/ai-contributions.md`, `.macroscope/check-run-agents/visible-change-evidence.md` | `scripts/check-fork-invariants.sh` |
| V1 retirement | `/api/v1` is retired with upstream (#3200): the main API listener answers `410 client_upgrade_required` on every v1 route except the health/ready probes and three Vio-only operations that still have no v2 operation — deleting a catalog item (`DELETE /api/v1/admin/items/{id}`), purging a collection's zero-storage items (`POST /api/v1/admin/collections/purge-virtual`), and a plugin's dynamic config options (`POST /api/v1/admin/plugins/installations/{id}/config-options`). Those three are registered ahead of the tombstone and must move to `/api/v2` before the carve-out is removed. `contracts/api/v2/migration-inventory.json` freezes the pre-retirement inventory so the migration ledger and scenario-catalog coverage gates keep validating the historical alpha surface; `contracts/api/v2/offline-routes.txt` is regenerated against the tombstones. | `internal/api/router.go`, `internal/api/v1_tombstone_test.go`, `contracts/api/v2/migration-inventory.json` | `verify-migration-ledger`, `verify-scenario-catalogs`, `verify-offline-routes`, `internal/api/v1_tombstone_test.go` |
| Virtual transcode worker dispatch | Virtual library sources (`virtual://`) resolve to pre-resolved stream relay URLs (`/source/<token>/...`) and dispatch to pooled remote transcode worker nodes for video transcoding, while direct play and progressive remux remain restricted to the integrated API path. The transcode node path authorizer permits approved relay stream URLs | `internal/api/handlers/playback_v3.go`, `internal/api/handlers/playback_transport.go`, `internal/jellycompat/virtual_playback.go`, `internal/jellycompat/streams.go`, `internal/transcodenode/path_authorizer.go` | transcodenode, jellycompat, and api handlers playback suites |

### Upstream work deliberately not adopted

- The candidate-list V3 planner rework (#3946/#3948) is not carried: the fork keeps
  `internal/playback/plan_v3.go`. Files it introduced (`candidate_planner_v3.go`,
  `candidate_routes_v3.go`, `candidate_video_v3.go`, `plan_refusal_v3.go`) are not
  in the tree. Portable pieces that were kept: the remux range-refusal helper and
  transport feature, the HLS copied-AAC (`aac_adtstoasc`) path, and the detached
  terminal-scrobble helpers.
- The Jellyfin compat admin-end hook (#3969) is deferred: stopping a native
  session does not yet end the Jellyfin play attached to it.
- Upstream-added tests whose subject the fork does not implement were dropped;
  tests for surfaces upstream retired with `/api/v1` were removed with them.
- Signed node source grants and the core loopback relay (#4337/#4338) are not
  adopted. The fork keeps `CatalogPathAuthorizer`
  (`internal/transcodenode/path_authorizer.go`) and its direct `virtual://`
  dispatch: a transcode node approves its input against the catalog, and
  virtual sources arrive as pre-resolved relay URLs. Upstream's
  `internal/nodesource` grant minting, grant-open endpoint, and loopback relay
  are not wired into the fork's node runtime. Adopting them (per-node keys,
  thin nodes, core-authoritative source access) is a separate project, not a
  merge-time change.
- The copy-group rework (#1770/#4400/#4434/#2515) is deferred. The fork keeps
  its copy planner — `copySegmentPlan.seekTarget`/`segmentSeekPosition`,
  `StartSegmentNumber`, `StreamOriginSeconds`, and `CopySeekAnchorResolved` —
  and the files that only make sense with upstream's `copyGroupAssembler`
  (`copy_fmp4.go`, `copy_group_assembler.go`, `copy_keyframe_resume_test.go`)
  are not carried. The deferral preserves the fork's segment numbering and
  seek-anchor behavior; porting the assembler is a separate project.
- The per-node worker key handshake is not adopted either; the fork keeps the
  shared `auth.jwt_secret` for worker calls. `nodeauth.Sign` and `SignWith`
  therefore send an unsigned request when no key is configured instead of
  refusing it, and `nodepool.TranscodePool.HasKey` treats every node as
  signable while no key check is wired. Do not reintroduce a hard failure
  there: it would leave every transcode node filtered out of route planning on
  a deployment without node keys. Node result headers keep the fork's
  `X-Vio-*` names (see `internal/downloadprepare`).
- The fork's start-ready pipeline (`transcode_startup.go`, `prepare_file.go`,
  `prepare_progress.go`) and its tests are the ones to keep on a merge:
  upstream's rework there changes when a transcode is declared ready and how a
  hardware start falls back, and the fork's tests pin the older contract.

## Merge procedure

1. `git fetch upstream` and review every commit: subjects plus `git show
   --stat` for each. Pay special attention to dropped or renamed operations,
   parameters, settings keys, and anything touching the files above.
2. Red flags that need a decision, not a blind resolve:
   - A removed API operation: grep `web/src` for its operation ID — if the
     fork's UI calls it, keep calling code working (adapt or vendor a
     replacement) before merging.
   - New hunks in `internal/api/router.go` or `cmd/silo/main.go` near virtual
     dispatch: upstream may reintroduce plugin fallbacks. The invariant script
     catches callers; keep routing core-only.
   - `internal/config/admin_settings.go` changes: check for key renames or
     removed fork-used keys.
   - `internal/playback/audio_select.go` changes: upstream lacks MULTi track
     awareness (`track.Languages`, `trackHasLanguage`) and fine-grained
     regional/script discrimination. Do not adopt upstream `audio_select.go`
     wholesale.
   - `contracts/api/v2/*` and `web/src/api/v2/{operations,schema}.ts` churn:
     expected when upstream changes the contract. These are generated —
     resolve by regenerating (`make apiv2-*`), never by hand-editing.
   - `Dockerfile`, `docker-compose.yml`, `.env.example`, and `.github/workflows/docker.yml` hunks: upstream lacks the
     decoupled `node-base` stage (which enables BuildKit frontend pruning when
     prebuilt assets are injected), shadows Go module layers with cache mounts,
     lacks `buildkit-cache-dance` to persist Go compiler caches, duplicates
     manual frontend builds across runners, and continuously overwrites the `:latest`
     tag on every main branch commit. Do not let upstream merges
     overwrite Vio's optimized build pipeline or revert `:dev` defaults to `:latest`. Note that `.github/workflows/docker.yml`
     is preserved automatically via `.gitattributes` (`merge=ours`, configured by
     `make install-hooks`), while `Dockerfile`, `docker-compose.yml`, and `.env.example` hunks must be reviewed to keep
     the decoupled `node-base` stage, unshadowed module layer caching, the
     pinned base image versions, and the `:dev` image tags. Floating base image tags (`node:22-slim`,
     `golang:1.27`, `debian:trixie-slim`) bump silently and invalidate the
     layer cache and the Go build cache, so the fork pins them to patch/dated
     tags and bumps them as a deliberate change. The build stage's `golang`
     tag must equal the `go` directive in `go.mod`: the Go images and
     `setup-go` both run with `GOTOOLCHAIN=local`, so a lagging tag cannot
     build the module at all. CI and `make verify-fork-invariants` enforce
     both.
3. Merge with a merge commit so fork history stays readable. Never rebase
   pushed history.
4. Verify before pushing: `scripts/check-fork-invariants.sh` (includes gofmt
   and prettier gates — the repo pre-commit hook does not check formatting),
   `go build ./...`, the focused suites for touched areas (`internal/apiv2`
   including the fixtures/document tests that pin the contract,
   `internal/catalog` virtual suites against a disposable DB, `internal/api`
   router tests, web typecheck plus touched step tests).
5. Push. The image pipeline builds from the merge.
