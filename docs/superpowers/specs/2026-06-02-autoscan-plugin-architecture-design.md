# Autoscan as a Pluggable Scan-Source Category — Design Spec

**Date:** 2026-06-02
**Status:** Approved design, pre-implementation
**Goal:** Turn autoscan from a Requests-coupled, arr-only feature into a standalone **Autoscan** category whose change-detection providers are out-of-process Silo plugins, with Sonarr/Radarr as the first provider.

**Architecture (one sentence):** Silo's host keeps a thin, provider-agnostic engine that resolves changed paths to library folders and enqueues rescans; each *way of noticing change* (arr, later inotify/ceph) is an installable plugin that Silo pulls on a timer through a new additive `scan_source.v1` capability.

**Tech stack:** Go (silo-server host + plugins), `silo-plugin-sdk` (protobuf capability contract, consumed as a tagged module), existing `internal/plugins` + `internal/pluginhost` runtime, existing `scantrigger`/`scanqueue` scan pipeline, React/TS admin UI.

Commands assume the repository root is the cwd.

---

## 1. Background & motivation

The current autoscan implementation (delivered in the now-closed PR #43, still on local `main`) is in-process Go that:

- polls Sonarr/Radarr `/api/v3/history/since` on a host timer,
- rewrites arr-side paths to Silo paths, dedups to parent dirs,
- resolves each to a `MediaFolder` and enqueues a targeted scan,
- is configured against `request_integrations` rows (hard FK, `ON DELETE CASCADE`), living as a tab inside the **Requests** admin page.

Two structural problems:

1. **It is arr-only and not extensible.** Adding inotify, Ceph notifications, or any other change source means more bespoke host code.
2. **It is subordinate to Requests.** A source cannot exist without a Requests integration row; deleting the row destroys the source.

Silo already ships a full out-of-process plugin runtime (`internal/plugins`, `internal/pluginhost`): manifests, an installer, typed capability descriptors (`metadata_provider.v1`, `request_router.v1`, `media_analyzer.v1`, `scheduled_task.v1`, `event_consumer.v1`, …), a host-driven scheduled-task mechanism, and a rate-limited plugin→host event path. This design uses that runtime to make change-detection pluggable.

## 2. Goals / non-goals

**Goals**

- A new **Autoscan** admin category, independent of Requests.
- A new additive `scan_source.v1` plugin capability — **breaks no existing plugin**.
- A **client-pull** contract: Silo asks each source "what changed since `<marker>`?" on a timer.
- Sonarr/Radarr re-implemented as the first scan-source plugin; the closed PR's arr logic relocates into it.
- A provider-agnostic host engine reusable by every future scan source.
- Optional reuse of Requests' arr connection details, *or* standalone connection entry.

**Non-goals (this design)**

- inotify / Ceph / other providers (the contract must *accommodate* them; we do not build them).
- A push (plugin-initiated) delivery mode. Pull only; push sources buffer internally and answer on pull (see §6).
- Refactoring the Requests subsystem itself.

## 3. Architecture overview

```
┌──────────────────────────── Silo (host) ────────────────────────────┐
│  Autoscan category (new; not under Requests)                         │
│    • Sources: which scan_source plugin instance, on/off, interval    │
│    • Connections: reuse a Requests arr server, or enter own          │
│    • Per-source state: opaque marker, last-run status                │
│                              │                                       │
│                              ▼                                       │
│  AutoscanStore — generic engine (provider-agnostic)                  │
│    per tick, per enabled source:                                     │
│      call plugin PollChanges(marker)                                 │
│        → resolve each Silo-native path → MediaFolder                 │
│        → suppress duplicates → enqueue scan (existing scanner)       │
│        → store returned next_marker                                  │
│                              │  scan_source.v1 (pull)                │
└──────────────────────────────┼───────────────────────────────────────┘
                               ▼
              ┌──────── Sonarr/Radarr scan-source plugin ────────┐
              │  (new installable repo, like silo-plugin-tmdb)   │
              │  receives resolved arr address + key             │
              │  polls /history (imports + renames)              │
              │  applies its OWN path rewrites                    │
              │  returns Silo-native paths + next marker          │
              └──────────────────────────────────────────────────┘
```

**Five components**

1. **`scan_source.v1`** (in `silo-plugin-sdk`) — the pull contract. §5.
2. **AutoscanStore** (host) — generic resolve→suppress→enqueue→marker engine. §7.
3. **Connection resolution** (host) — reference-or-inline → concrete credentials. §8.
4. **Autoscan category + data model** (host) — sources, connections, decoupled from Requests. §9.
5. **arr scan-source plugin** (new repo) — first provider. §10.

## 4. Responsibility split (the dividing line)

| Concern | Owner | Rationale |
|---|---|---|
| Talk to arr; poll `/history`; imports + renames; bounded window; first-run | **Plugin** | Provider-specific I/O. |
| **Path rewrites** (config, application, "Sync from arr" suggester) | **Plugin** | Each source mounts content differently; translation is per-source. Plugin returns Silo-native paths. |
| Resolve Silo-native path → `MediaFolder`; dedupe; **enqueue rescan** | **Host** | Touches Silo internals (scan queue, DB, library map) that plugins cannot and must not reach. |
| Opaque per-source marker; poll interval; enabled; last-run status | **Host** | Scheduling and bookkeeping are the engine's job. |
| Connection selection (reuse-from-Requests vs own) and credential resolution | **Host** | Only the host knows about Requests; the plugin receives resolved creds. |

The plugin's responsibility ends at **"here are the changed paths, in Silo's terms."** Everything past that is the host engine, which is provider-agnostic.

## 5. The `scan_source.v1` contract (centerpiece)

A new, additive capability in `silo-plugin-sdk`. Existing plugins never declare it and are unaffected (§11).

### Protobuf (as implemented in silo-plugin-sdk)

```proto
// scan_source.v1 — host pulls changed paths from a provider on a timer.
service ScanSource {
  // Host calls this on each poll tick for a configured scan_source capability.
  rpc PollChanges(PollChangesRequest) returns (PollChangesResponse);
}

message PollChangesRequest {
  string capability_id = 1;          // which configured scan_source instance
  string marker        = 2;          // opaque; empty string on first run
  ResolvedConnection connection = 3; // resolved upstream credentials (see §8)
}

// Concrete upstream credentials the host resolves and hands to the plugin each
// poll, so the plugin stays credential-source-agnostic and stores no secrets.
message ResolvedConnection {
  string base_url = 1;
  string api_key  = 2;
}

message PollChangesResponse {
  // Absolute paths already translated to Silo's filesystem namespace
  // (the plugin has applied its own rewrites). Files or directories.
  repeated string changed_paths = 1;
  // Opaque continuation token. Host stores verbatim and echoes it back
  // on the next PollChanges. The plugin never assumes the host parses it.
  string next_marker = 2;
}
```

### Contract rules

- **Pull only.** The host owns the timer and calls `PollChanges`. The plugin never initiates.
- **Opaque marker.** The host stores and echoes `next_marker` without interpreting it. arr uses an RFC3339 timestamp; a future provider may use a sequence number or event id — no host change required.
- **First run.** `marker == ""` ⇒ the plugin starts from "now" (do not replay full history).
- **Idempotent, at-least-once.** The host may re-issue the same `marker` if it failed to persist `next_marker`; providers must tolerate returning overlapping paths. The host's suppression layer (§7) absorbs duplicates.
- **Error semantics.** A failed `PollChanges` (e.g. arr unreachable) ⇒ the host keeps the *old* marker and retries next tick; nothing is skipped or advanced.
- **Silo-native paths only.** `changed_paths` are already rewritten by the plugin; the host does no path translation.
- **Credentials per call.** The host resolves the source's connection (§8) and passes the concrete `{base_url, api_key}` in `PollChangesRequest.connection` on every poll. The plugin reads them from the request — it stores no credentials and never knows whether they came from its own config or a reused Requests link. Delivered over the local host↔plugin gRPC channel only.

The capability needs **only** `PollChanges` for v1. A `TestConnection`/validate RPC is explicitly deferred (YAGNI; connection validity surfaces as a failed poll).

## 6. Marker & delivery semantics

- One opaque marker stored per source, host-side.
- Pull-only does **not** exclude push-style providers: an inotify/Ceph plugin maintains its own internal buffer of observed changes and flushes it (and advances its marker) on each `PollChanges`. Cost: up to one poll-interval of latency. This keeps a single uniform contract.

## 7. Host: AutoscanStore engine

Provider-agnostic. Per enabled source, per tick:

1. `paths, next := plugin.PollChanges(marker)`; on error, log + keep marker + continue.
2. For each `dir` in `uniqueParentDirs(paths)`:
   - `target := scantrigger.Resolve(dir)`. Unresolvable (outside Silo's media folders) ⇒ quiet skip.
   - Suppression key `fmt.Sprintf("%d|%s", folderID, target.Path)` with debounce TTL; already-claimed ⇒ skip.
   - Accumulate `target`; record claimed key.
3. `scanqueue.EnqueueScans(targets)`. On failure, release claims and **do not** advance the marker.
4. On success, persist `next_marker`.

This is the salvaged core of PR #43 (`internal/autoscan/service.go` resolve/suppress/enqueue, `dedupe.go`, `suppress.go`), generalized so its input is "paths from a provider" rather than "paths from arr." Path-rewrite helpers (`rewrite.go`, `suggest.go`, `history.go`, the arr history client) leave the host and move into the plugin (§10).

## 8. Host: Connections (reuse or own)

A **connection** is "an arr server Silo can reach," created two ways:

- **Own:** name + base URL + API key, entered in the Autoscan category; the key is Fernet-encrypted at rest (same mechanism as existing service credentials — never plaintext in JSONB).
- **Linked (reuse):** a live reference to an existing Requests `request_integrations` arr server. Resolved at use-time, so edits in Requests propagate.

**Resolution:** on each poll the host resolves the chosen connection (own or linked) into concrete `{base_url, api_key}` and passes them in `PollChangesRequest.connection` (§5). The plugin is credential-source-agnostic — it only ever sees resolved values, fresh each poll, so a change to a reused Requests credential propagates immediately with no re-push. This "reuse or own" behaviour is therefore free for every future provider. (Resolving per-call rather than caching in plugin config also means no decrypted secret is ever stored at rest in the plugin.)

**Decoupling from Requests:** the link is **soft and optional** — `SET NULL` / surfaced-as-"needs attention", never cascading. Requests has no knowledge of Autoscan. Autoscan and Requests are peers; Autoscan merely *offers* to borrow a Requests connection.

## 9. Host: Autoscan category & data model

New top-level **Autoscan** admin section (its own nav entry, not a Requests tab). Manages **Connections** (§8) and **Sources**.

A **source** corresponds to one configured `scan_source.v1` capability instance and carries:

- **Plugin-owned config:** path rewrites (+ "Sync from arr") and any provider-specific settings. Configured on the plugin's own settings screen.
- **Host-owned per-source state:** the chosen connection, enabled on/off, poll interval (per-source, not one global timer), the opaque marker, and last-run status/error.

**Source auto-discovery (resolves §14 risk #1).** Source rows are not created by hand. On each poll cycle the host enumerates every installed `scan_source.v1` capability (via the plugin installation store) and *seeds a disabled, connection-less row* per capability (`INSERT … ON CONFLICT (installation_id, capability_id) DO NOTHING`, never disturbing existing config). The operator then binds a connection and enables it. Consequently **`connection_id` is nullable**: a freshly-discovered source has none, and the engine skips any enabled source with no connection (recording a "no connection bound" status). The admin API rejects enabling a source that has no effective connection.

**Per-source interval** is honoured as a floor: the global poll task runs at `default_poll_interval_seconds`, and within a cycle the engine skips a source whose `last_run_at` is newer than its own `poll_interval_seconds` (falling back to the default). A source therefore polls *at most* every N seconds, never more often than the global cadence.

### Schema changes (host)

Rework the PR #43 tables; **no FK into `request_integrations`**:

- `autoscan_connections` — `id`, `name`, `kind` (e.g. `sonarr`/`radarr`), and *either* own credentials (`base_url`, `api_key_ref` encrypted) *or* a nullable soft link `request_integration_id` (`ON DELETE SET NULL`).
- `autoscan_sources` — `id`, `(installation_id, capability_id)` (unique; the discovered identity), **nullable** `connection_id → autoscan_connections` (`ON DELETE RESTRICT`; null until the operator binds one), `enabled`, `poll_interval_seconds` (nullable; null = use default), `marker` (opaque text, nullable), `last_run_at`, `last_error`. Creation-time validity for connections (own creds OR a link) is enforced at the application layer (the DB CHECK would conflict with `ON DELETE SET NULL` on the reuse link).
- `autoscan_settings` (optional) — retain a global enable + default interval; per-source interval overrides it.

A migration supersedes PR #43's `171_autoscan` schema (which is not yet upstream, so this is a forward redefinition, not a production migration of live data on `origin/main`).

## 10. Plugin: Sonarr/Radarr scan-source

A new installable repo structured like `silo-plugin-tmdb` (standalone Go module, `manifest.json` declaring `scan_source.v1`, gRPC `main.go`, depends on the new tagged `silo-plugin-sdk`). Receives resolved arr `{base_url, api_key}` as config. Implements `PollChanges`:

1. Treat `marker` as the "since" timestamp (empty ⇒ now).
2. Call arr `/api/v3/history/since`, capped by a bounded window (24h max-lookback floor + overlap buffer — relocated from PR #43) so a long outage cannot pull an oversized response.
3. Extract paths: **imports** (`downloadFolderImported.importedPath`) and **renames** (`episodeFileRenamed`/`movieFileRenamed`: both new `path` and old `sourcePath`). Deletes ignored (upgrade-deletes are covered by the paired import; standalone deletes carry no path).
4. **Apply this source's path rewrites** → Silo-native paths. Normalize separators (Windows arr → Linux host).
5. Return `{changed_paths, next_marker = newest history timestamp}`.

**Rewrite config + "Sync from arr":** the rewrite list and the suffix-match suggester (which matches arr root folders to Silo media folders) live in the plugin. The suggester needs Silo's media-folder list, obtained via the existing host library-listing service (`internal/pluginhost` library lister) exposed to plugins. The relocated logic is PR #43's `suggest.go`/`suggest_deps.go`/`rewrite.go`/`history.go`.

## 11. Backward compatibility (hard requirement)

Adding `scan_source.v1` **must not break existing plugins.** It doesn't, by construction:

- Capabilities are independent opt-in declarations. Each host subsystem **filters for its own type and ignores the rest** (verified: `metadata_provider.v1`, `scheduled_task.v1`, `event_consumer.v1`, … each use `if type != mine { skip }`). There is **no global allowlist that rejects unknown capability types.**
- **Additive-only rule:** the SDK change *adds* new proto messages/service + a new capability string. It never modifies the shape of an existing capability. (Mutating an existing capability is the only thing that would break plugins; we forbid it.)
- **No forced upgrades:** existing plugin repos stay pinned to their current SDK version and build unchanged. Only a plugin that *wants* to be a scan source adopts the new SDK version.

## 12. Build order & repo decomposition

One shared contract, three repos (+ catalog), in dependency order:

1. **`silo-plugin-sdk`** (long pole): define `scan_source.v1` (proto + generated code + manifest helper + runtime server scaffold), tag a new version (e.g. `v0.5.0`). The tagged release is a hard serialization point — host and plugin cannot build against the contract until it exists.
2. **silo-server (host)** — bump SDK dep; build AutoscanStore (generalized), the `scan_source.v1` caller (timer → `PollChanges`), connections + resolution, the Autoscan category UI, retire the in-process arr autoscan, add the reworked migration. *Parallelizable with (3) once (1) is tagged.*
3. **arr plugin (new repo)** — depends on SDK `v0.5.0`; implement `PollChanges`, rewrites, "Sync from arr", bounded window.
4. **`silo-plugins`** (catalog) — add the entry so the arr plugin is installable.

Each repo gets its **own implementation plan** (separate `writing-plans` pass); this single spec defines the shared contract and per-side responsibilities they all build against.

**Local logistics:** only `silo-server` and `silo-plugin-tmdb` are currently checked out. Implementation will require `silo-plugin-sdk` and a new arr-plugin repo (and later `silo-plugins`) available locally.

## 13. Salvage map (from closed PR #43)

| PR #43 code | Destination |
|---|---|
| arr `/history` client, imports + renames, bounded window (`history.go`) | arr plugin |
| path rewrite apply (`rewrite.go`) + "Sync from arr" suggester (`suggest.go`, `suggest_deps.go`) | arr plugin |
| `last_poll_at` cursor | host, generalized to opaque per-source `marker` |
| resolve + dedupe + suppress + enqueue (`service.go`, `dedupe.go`, `suppress.go`) | host AutoscanStore (generic) |
| `171_autoscan` schema (FK → `request_integrations`) | host, reworked: `autoscan_connections` + `autoscan_sources`, no requests FK |
| Autoscan admin tab under Requests (`AdminRequests.tsx`) | host, as its own Autoscan category |

## 14. Risks & open questions

1. **Plugin multiplicity for multiple arr servers.** Does the runtime support multiple configured instances of one plugin (one per arr server), or is config per-installation only? If the latter, "two Sonarrs + one Radarr" implies either multiple installations or a plugin that manages several connections internally. *Resolve before the host/plugin plans by reading `internal/plugins` installation + capability-instance handling.*
2. **Plugin config UI richness.** The rewrite table + a "Sync from arr" action are richer than typical key-value plugin config. Verify the plugin config-schema/admin-config UI can host a structured list and an action button; if not, design a minimal affordance (e.g. suggester runs on first configure, or a dedicated plugin HTTP route via `http_routes.v1`).
3. **Library-folder list exposure to plugins.** Confirm the `internal/pluginhost` library lister is reachable by a `scan_source` plugin for the suggester. If not, add a host service method.
4. **Marker persistence vs at-least-once.** Ensure marker write happens only after successful enqueue, matching PR #43's "don't advance on failure" guarantee.
5. **Closed PR disposition.** The in-process autoscan on local `main` (34 commits) should be removed/replaced rather than shipped; sequence the host plan to retire it cleanly.

## 15. Testing strategy

- **SDK:** proto compiles; manifest helper round-trips `scan_source.v1`; runtime scaffold serves `PollChanges`.
- **Host engine:** table-driven tests for resolve→suppress→enqueue→marker (port PR #43's `service_test.go`, generalized to a fake `ScanSource` client): dedupe, distinct-paths-same-folder, disabled no-op, poll failure keeps marker, enqueue failure releases claims + keeps marker, opaque-marker pass-through. Run in the libvips-equipped environment (host handler tests link libvips via CGO — see workspace note).
- **Connections:** resolution of own vs linked; linked falls back gracefully when the Requests row is deleted (`SET NULL`).
- **arr plugin:** history parsing (imports + renames, ignores unrelated events), rewrite application, bounded window, first-run "from now"; suffix-match suggester cases (unique/ambiguous/covered/no-op/normalization) ported from PR #43.
- **Compat:** an existing-style plugin manifest with no `scan_source.v1` is unaffected by host scan-source dispatch.

## 16. Out of scope / future

- inotify and Ceph-notify scan-source plugins (the contract accommodates them via the buffer-and-flush pattern of §6).
- A push/event delivery mode, if pull latency ever proves insufficient.
- A `TestConnection` RPC on the capability.
- Migrating Requests onto a shared connection abstraction (today's reuse is a one-directional soft link only).
