# Client Diagnostics: crash reports and debug log upload

Status: Draft (spec + cross-repo plan)
Date: 2026-07-19
Repos affected: `silo-server`, `silo-android`, `silo-apple`

## Summary

Add an opt-in, self-hosted diagnostics pipeline: Silo clients detect crashes
and abnormal exits, collect structured debug logs (playback, TV focus,
network, lifecycle) plus a hardware snapshot (device model, display/HDMI,
audio path, codec capabilities), and — only with explicit user consent —
upload a compressed diagnostics bundle to the user's own Silo server. Admins
review bundles through new acting-admin API endpoints alongside the existing
server logs tooling.

Everything ships **disabled by default**:

- Server: a `diagnostics.uploads_enabled` server setting gates the whole
  feature (default off), and it cannot be enabled unless the server's private
  object storage is configured and validated.
- Client: verbose "debug logging" is a per-device setting (default off).
  Crash reporting defaults to **Ask** — after an abnormal exit the app shows
  an incident-specific prompt (e.g. *"Silo closed unexpectedly during
  playback. Send a diagnostic report to your server?"*) with
  **Send / Don't send / Always send**. Nothing uploads without one of: an
  explicit prompt confirmation, a prior "Always send" election bound to this
  server and account, or the user explicitly sending a manual capture.

## Motivation

Today we are blind to client-side failures:

- **No crash reporting anywhere.** Neither the Android repo nor the Apple
  repo installs an uncaught-exception handler, uses MetricKit, or bundles a
  crash SDK (verified: no handler/SDK references in either repo; the only
  Firebase dependency is messaging). A user saying "the app crashed" is a
  dead end.
- **Diagnostics don't survive to the report.** Android logs go to logcat (raw
  `android.util.Log` in ~40 files, no buffer or file sink); the Apple
  player's rich `[CMP]`/`[CMP-DIAG]` diagnostics go through `print()`
  (`PlayerLog.swift` / `PlayerCore.swift`) with no app-owned persistent sink.
  By the time a user reports a problem, the evidence is gone.
- **The hardest bugs are device-specific.** Shield HLG quirks, Fire TV
  passthrough suppression, HDMI mode switches, TV focus traps — reproducing
  them requires knowing the exact device, panel, audio chain, and codec path.
  Users can't reliably report any of that.
- **The server already has half the story.** `operational_logs` records
  `playback_session_id` (migration `028_log_partitioning.sql`), the admin
  logs API filters by it, and the web admin has a logs viewer. What's missing
  is the client half, and a way to join the two.

## Goals

- Capture crashes and abnormal exits (JVM/native/ANR on Android; crash and
  hang diagnostics on iOS; abnormal-exit detection on tvOS) with enough
  context to act on: stack or exit trace where the platform provides one,
  recent structured logs, and a device snapshot.
- Capture deep opt-in debug logs for playback, TV focus behavior, networking,
  and navigation/lifecycle — everything needed to troubleshoot user issues.
- Always include a hardware snapshot: device (Shield / Fire TV / onn /
  phone), OS, app build, display and HDMI mode with HDR capabilities, audio
  output path and passthrough capabilities, decoder inventory — with honest
  `unknown` / `not_collected` states where a platform can't provide a field.
- One payload contract across Android and Apple; one server endpoint; one
  admin surface. The contract is a checked-in versioned JSON Schema with
  golden fixtures consumed by all three repos' CI.
- Explicit, server-bound consent for every byte uploaded; feature fully off
  by default on both server and client.
- Bounded cost: size caps, per-account quotas, concurrency limits, and
  server-side retention.

## Non-goals (v1)

- Live/continuous log streaming from clients to the server.
- Screenshots or screen recordings in bundles.
- Native/tvOS crash *stack* capture via an in-process crash reporter
  (PLCrashReporter/KSCrash-style). tvOS v1 detects abnormal exits and
  attaches breadcrumbs, not stacks (see Apple design). Adopting an in-process
  crash reporter is a separate decision with its own signal-safety and review
  implications.
- Symbolication. Bundles store what the OS gives us; a server-side
  symbolication job can come later.
- Native-app admin viewer screens (per current product policy, admin surfaces
  beyond STATS on the clients are a separate product decision; v1 admin
  consumption is via API + web admin).
- macOS. The Apple repo has a macOS target, but v1 scopes to
  `android`, `android-tv`, `ios`, `tvos`; the platform enum reserves `macos`
  without accepting it.
- Third-party telemetry, metrics aggregation, or fleet analytics. This is a
  support/debugging tool, not analytics.

## Why this route

**Upload to the user's own Silo server, not a third-party crash service.**
Crashlytics/Sentry SaaS would send user data to a third party, require vendor
accounts/keys we'd have to ship, and put the reports in front of *us* rather
than the person who can actually act — the self-hosting admin. Requiring
every admin to also run self-hosted Sentry is far too heavy. The Silo server
is the natural, privacy-aligned destination, and it already has auth, admin
tooling, object storage, and retention patterns to build on.

**Bundle-on-event, not streaming.** A bundle captured at the moment of
failure (crash, or a user-triggered capture) contains exactly the context
needed: recent structured logs, the playback stats timeline, and the hardware
snapshot. Continuous streaming would cost bandwidth and privacy while mostly
capturing noise, and would still miss pre-crash buffers.

**Hybrid storage: Postgres row + object-store blob.** Report *metadata* (a
small manifest: device summary, truncated crash summary, type, timestamps)
goes in a queryable table; the payload itself (potentially megabytes of logs
and crash artifacts) goes to the S3-compatible private bucket as a single
archive. Ingesting client log lines into the partitioned `operational_logs`
table was considered and rejected: bundles are consumed as whole documents by
a human, not queried row-wise; client lines are untrusted input with
different retention needs; and multi-MB uploads would bloat a table tuned for
the server's own high-churn logs. The server *does* emit normalized
operational events about diagnostics activity (report accepted / rejected /
downloaded / deleted, with report id, account, sizes, result) so ingest and
admin actions are auditable and correlate with existing logs — raw client
lines never enter `operational_logs`.

**A versioned archive, not a bare gzipped log stream.** Crash evidence is
heterogeneous: JSONL logs, ANR trace text, Android native tombstones
(protobuf on API 31+), MetricKit diagnostic JSON. A tar.gz archive with named
entries carries each artifact losslessly with a declared media type, keeps
large traces out of Postgres, and can evolve by adding entries.

**Build small in-house rather than adopt ACRA/other SDKs.** The surface we
need on Android (one exception handler writing a bounded marker, a ring
buffer, one upload call) is a few hundred lines; ACRA would add a dependency
and its own report format without covering Apple. On Apple, the OS provides
MetricKit natively (iOS; not tvOS — see Apple design).

## Architecture overview

```text
 Android / Apple client                          silo-server
┌────────────────────────────────┐   ┌──────────────────────────────────────┐
│ Safe-logging facade → ring     │   │ GET  /api/v1/diagnostics/status      │
│  └ debug mode: rotating        │   │   └ available|disabled|              │
│    segment files               │   │     storage_unavailable + limits     │
│ Crash/abnormal-exit capture    │   │ POST /api/v1/diagnostics/reports     │
│  ├ UEH marker (Android)        │──▶│   ├ gate + storage check             │
│  ├ ApplicationExitInfo (30+)   │   │   ├ streaming, bounded multipart     │
│  ├ MetricKit (iOS)             │   │   ├ account quotas + concurrency     │
│  └ exit sentinel (tvOS)        │   │   ├ manifest → client_diagnostic_    │
│ Pending reports (server-bound) │   │   │   reports (state machine)        │
│ DeviceSnapshot (probes+new     │   │   └ archive → S3 private bucket      │
│   accessors)                   │   │ Admin (acting admin): list/detail/   │
│ Consent UI (ask/always/never,  │   │   download/delete + audit events     │
│   per server+account)          │   │ Retention + orphan reconciler task   │
└────────────────────────────────┘   └──────────────────────────────────────┘
```

## Consent model

Consent is **scoped, versioned, and destination-bound** — not a global
boolean. Both clients support multiple servers and accounts, so a device-wide
"Always send" would let a bundle full of server A's titles and URLs upload to
server B after a switch. Rules:

- Consent (including "Always send") is **account-scoped**: every consent
  record and every pending report is bound to
  `(server_instance_id, account_user_id, consent_notice_version)`, where
  `server_instance_id` is a stable opaque ID returned by the status
  endpoint. The capturing `profile_id` is recorded on reports for
  *attribution*, not as a consent scope — quotas and elections follow the
  account.
- A pending report is only ever uploaded to the server+account it was
  captured under. Reports are never retargeted. Switching server or account
  neither uploads nor migrates pending reports; signing out of an account or
  removing a server deletes its pending reports.
- "Always send" is an election for one server+account. Bumping
  `consent_notice_version` (when we materially change what bundles contain)
  invalidates prior elections and reverts to Ask.
- Setting crash reports to **Never** deletes pending reports for that
  server+account and stops persistent capture (breadcrumbs/debug files); the
  in-memory ring keeps running (memory-only, never uploaded without a later
  explicit action).
- **Child/managed profiles**: the Diagnostics section is not shown to
  profiles with `is_child`, and every diagnostics action — changing
  settings, electing Always, the crash prompt, and manual capture/send —
  requires a non-child profile. A crash captured while a child profile was
  active is held pending and prompted for the next time a non-child profile
  is active. Reports are attributed to account + capturing profile.
- The crash prompt and the manual flow both offer **View report** — a
  human-readable summary (incident, categories and line counts, device
  identity, destination server) before anything is sent.
- Uploads carry `consent: {mode, notice_version}` in the manifest so the
  server can reject stale-consent uploads outright.

## Bundle format (shared contract)

An upload is `multipart/form-data` with exactly two parts, in order:

1. `manifest` — `application/json`, ≤ 64 KiB. Queryable summary; stored in
   Postgres.
2. `bundle` — `application/gzip`: a tar.gz archive, `manifest.json` first.
   Entry names come from a **fixed allowlist**, and each name implies its
   media type — there is no per-entry type metadata to drift:

```jsonc
manifest.json            (manifest minus the `archive` object; see hashing note)
device.json              (full device snapshot)
logs.jsonl               (structured log lines, newest-last)
crash/summary.json       (normalized crash info incl. provenance)
crash/stack.txt          (JVM stack / ANR trace, when present)
crash/tombstone.pb       (Android native tombstone, opaque bytes, API 31+)
crash/metrickit.json     (raw MetricKit diagnostic JSON, iOS)
breadcrumbs.jsonl        (persisted pre-crash context journal, when present)
```

Hashing note: `archive.sha256` and the size fields in part 1 describe the
finalized `bundle` part bytes as transmitted. Since the archive is hashed
after it is built, the embedded `manifest.json` is the manifest **without**
the `archive` object; the server verifies part 1's `archive` fields against
the received blob. `archive.bytes` is the transmitted (gzip) size;
`archive.uncompressed_bytes` is the total decompressed tar stream size,
including tar headers, the end-of-archive marker, and record padding —
i.e. the byte count between the client's tar writer and gzip writer, and
what `gzip -l` reports. Standard end-of-archive zero padding (up to 64 KiB)
is accepted; any other trailing data is rejected.

The contract lives in this repo as a versioned JSON Schema
(`docs/design/schemas/client-diagnostics/v1/`) with canonical valid and
invalid fixtures; Go, Kotlin, and Swift CI all validate against the same
fixtures. Servers accept all schema versions they support and ignore unknown
additive fields.

### Manifest (part 1)

```json
{
  "schema_version": 1,
  "report": {
    "type": "crash",              // crash | anr | native_crash | hang | abnormal_exit | manual
    "captured_at": "2026-07-19T18:22:31Z",
    "capture_session_id": "run_c7d…",   // UUID per app run, also stamped on log lines
    "app_version": "1.4.2",
    "app_build": "20841",
    "platform": "android-tv",     // android | android-tv | ios | tvos  (macos reserved)
    "os_version": "11 (API 30)",
    "profile_id": "prof_…"        // capturing profile, attribution only
  },
  "destination": { "server_instance_id": "srv_…" },   // consent binding; server rejects mismatches
  "consent": { "mode": "prompt", "notice_version": 1 },  // prompt | always | manual
  "crash": {                      // absent for manual
    "summary": "NullPointerException in PlaybackSessionManager.start",
    "stack_excerpt": "…truncated ≤ 8 KiB; full artifact in archive…",
    "thread": "main",
    "foreground": true,
    "source": "ueh",              // ueh | exit_info | metrickit | exit_sentinel
    "provenance": "pre_failure",  // pre_failure | post_restart | metric_reporting_period
    "occurred_at": "2026-07-19T18:22:31Z"   // best known; MetricKit reports carry period bounds
  },
  "device_summary": { "manufacturer":"NVIDIA", "model":"SHIELD Android TV", "os":"11", "form_factor":"tv" },
  "playback_session_ids": ["ps_9f2…"],
  "log_summary": { "lines": 3841, "bytes_gz": 412034, "dropped_lines": 12, "categories": ["playback","focus","network"], "debug_logging": true },
  "archive": { "entries": ["manifest.json","device.json","logs.jsonl","crash/summary.json","crash/stack.txt"], "bytes": 498112, "uncompressed_bytes": 3110400, "sha256": "…" }
}
```

### Log line schema (`logs.jsonl`)

```json
{"ts":"2026-07-19T18:22:29.412Z","run":"run_c7d…","lvl":"W","cat":"playback","tag":"AudioCapabilityMgr","msg":"passthrough suppressed: HDMI sink lost TrueHD","attrs":{"sink":"HDMI","fmt":"truehd"}}
```

- `lvl`: `V|D|I|W|E`. `cat`: `playback`, `focus`, `network`, `lifecycle`,
  `browse`, `cast`, `download`, `crash`, `other`.
- `run` (capture_session_id) distinguishes lines from prior app runs when
  breadcrumbs or debug files span sessions.
- Playback additionally emits periodic (~5s) player-stats snapshot lines
  (`"cat":"playback","tag":"StatsSnapshot"`) built from the existing stats
  models — a timeline of decoder, resolution, HDR mode, bitrate, dropped
  frames, and audio underruns around the failure.

### Safe logging contract (redaction happens at collection time)

Best-effort scrubbing at package time is not enough: today's client logs
already emit full URLs with query strings, token suffixes, and raw exception
strings (e.g. the Apple `HTTPClient` response logging and player URL prints;
Android exception messages containing backend URLs). Therefore:

- The facade accepts **typed attributes**, not preformatted strings, for
  anything risky: URL values are normalized (userinfo, query, and fragment
  always stripped; path preserved), exceptions pass through a sanitizing
  formatter, and free-text `msg` is authored, not interpolated from network
  data.
- Attribute keys are **normatively registered**: a per-category key registry
  (checked in next to the JSON Schema) declares each key and its value type
  (scalar types plus a few enumerated units). Unregistered keys fail debug
  builds/CI and are dropped in production builds.
- Existing call sites are migrated **curated, not mechanically** — each
  migrated line is reviewed against the contract. Unmigrated `Log.*`/`print`
  call sites simply never enter bundles.
- Defense in depth at bundle time: values matching the account's current
  access/profile tokens are replaced before packaging.
- Golden leak-fixture tests (JWTs, query tokens, signed URLs, cookies, token
  fragments) run in all three repos' CI against the facade and the bundle
  builder.
- The network category logs `method, templated path, status, duration`;
  never headers, bodies, query strings, or full URLs.

### Device snapshot (`device.json`)

Fields are tri-state where a platform can't provide them: a value,
`"unknown"` (probe exists, couldn't determine), or `"not_collected"`
(no probe on this platform). Every snapshot carries `captured_at` and
`provenance` (`pre_failure` for snapshots persisted with the incident,
`post_restart` for snapshots taken at bundle-build time).

```json
{
  "captured_at": "2026-07-19T18:22:31Z",
  "provenance": "pre_failure",
  "identity":  { "manufacturer":"NVIDIA", "model":"SHIELD Android TV", "device":"mdarcy", "form_factor":"tv", "device_id":"…" },
  "display":   { "mode":"3840x2160@59.94", "modes_supported":[…], "hdr_types":["HDR10","HLG","DV"], "wide_gamut":true },
  "audio":     { "outputs":[{"type":"HDMI","encodings":["AC3","EAC3","TRUEHD"],"channels":8}], "passthrough":["AC3","EAC3","TRUEHD"], "suppressions":[…] },
  "video_codecs": [ { "mime":"video/hevc", "hw":true, "profiles":["Main10","DV P8"], "max":"4096x2160@60" } ],
  "network":   { "transport":"ethernet" }
}
```

Android sources: `AndroidDeviceMetadataProvider` (id/model/platform/app
version), `MediaCodecCapabilitiesProbe` (codec + HDR decode caps),
`AudioCapabilityManager` (aggregate passthrough). **New accessors required**
(the probes exist but don't currently export everything): current + supported
`Display.Mode` list (DisplayHdrProbe exposes HDR support only), exact
identity fields (`Build.DEVICE`, form factor), `AudioManager.getDevices()`
output enumeration, and a snapshot/export API on
`PassthroughSuppressionRegistry` (its set is currently private).

Apple sources: `AppleDeviceIdentity.current` (persisted id, device name,
platform) and `ApplePlaybackV3Capabilities.snapshot()` (codec/HDR/device
context). **Honesty constraints:** the current `outputSnapshot()` helper is
private, reports route UIDs/types rather than full output capabilities, and
its passthrough payload is a placeholder — so Apple v1 reports audio as
route metadata with `passthrough: "unknown"` until a real probe exists, and
the private helper is refactored into an internal shared accessor so
diagnostics can reuse it without duplicating route logic. Audio route UIDs
are persistent peripheral identifiers and are one-way hashed before
inclusion.

### Playback session correlation

Clients include recent server playback session IDs so admins can pivot into
the existing server-side logs (`operational_logs.playback_session_id` +
the admin logs filter). Both clients currently retain only the *active*
session ID (Android `PlaybackSessionManager`, Apple `PlaybackSessionBridge`),
so each client adds a small **bounded, persisted recent-session tracker**
(last ~10 session IDs with timestamps) that survives restarts — required for
next-launch crash reports.

## Server design (`silo-server`)

### Feature gate

Borrows the *behavior* of the requests gate (status endpoint + per-call
guard that rereads settings) but stores keys in the general `server_settings`
store — requests uses its own dedicated table, which diagnostics doesn't
need. Key naming follows existing namespaces (`opslog.*`,
`audiobooks.enabled`, `playback.*`):

- `diagnostics.uploads_enabled` (bool, default `false`)
- `diagnostics.max_bundle_bytes` (default 10 MiB compressed)
- `diagnostics.max_uncompressed_bytes` (default 64 MiB)
- `diagnostics.max_reports_per_user_per_day` (default 20)
- `diagnostics.retention_days` (default 30)
- `diagnostics.max_bytes_per_user` (default 200 MiB)

Enabling `diagnostics.uploads_enabled` **requires a storage validation**
(private object store configured and writable); the admin settings API
refuses to enable it otherwise. `S3Private` is a nullable dependency in the
router and is only populated when configured, so the feature must degrade
predictably, not 500.

### Status endpoint

`GET /api/v1/diagnostics/status` — authenticated (`RequireAuth`), **no
profile required**: crashes during login/profile selection must still be
reportable, so capability discovery and upload are account-scoped with
optional profile attribution.

```json
{
  "status": "available",            // available | disabled | storage_unavailable
  "server_instance_id": "srv_…",    // stable; binds consent + pending reports
  "accepted_schema_versions": [1],
  "max_bundle_bytes": 10485760,
  "max_manifest_bytes": 65536,
  "retention_days": 30,
  "consent_notice_version": 1
}
```

Clients cache this alongside existing server-driven config refresh.

### Ingest endpoint

`POST /api/v1/diagnostics/reports` — `RequireAuth`; access-token
`Authorization` header only (no query credentials; `sa_` API keys rejected
for diagnostics). Optional validated profile attribution. Hardened parsing —
the avatar handler is the *precedent* (multipart + `PutObject`), but its
buffer-everything approach (`ParseMultipartForm` + `io.ReadAll`) is not
reused:

1. Guard: `diagnostics.uploads_enabled` + storage present, else stable error
   codes (`disabled`, `storage_unavailable`).
2. `http.MaxBytesReader` with `max_bundle_bytes` + manifest overhead before
   any parsing; request deadline; per-account concurrency cap (1 in-flight)
   and a server-global concurrency ceiling, on top of the existing rate-limit
   middleware.
3. Streaming `multipart.Reader`: exactly the two named parts in order;
   manifest capped at 64 KiB and schema-validated; unknown parts rejected.
4. Quota reservation: in one transaction holding a per-account advisory
   lock (`pg_advisory_xact_lock(user_id)` — serializing concurrent uploads
   from multiple devices on one account), check the per-day count and
   per-user byte quota and insert the report row in state `receiving`
   (server-generated UUID + `short_id`). Failures return `quota_exceeded`
   (429, with `retry_after`) or `too_large` (413) — machine-readable so
   clients back off and keep the report pending locally.
5. Stream the `bundle` part directly to its final key
   (`diagnostics/{user_id}/{report_id}.tar.gz`) while hashing and counting
   bytes; enforce the compressed cap during streaming. Validate the
   gzip/tar structure and per-entry limits (entry count, name allowlist,
   uncompressed total ≤ `max_uncompressed_bytes`, compression ratio bound)
   in the same bounded streaming pass. Reject on violation
   (`invalid_bundle`, `unsupported_schema`). There is no staging/rename
   step — S3-style stores have no cheap rename, and the row's `state` is
   the source of truth: a blob is not a report until its row is `ready`.
6. On success, update the row to `ready` with sizes + sha256. On any
   failure: delete the object, mark the row `failed` (compensation); the
   reconciler (below) cleans up stragglers from process death mid-request.
7. Respond `201 {"report_id":"…","short_id":"SILO-…"}` and emit a normalized
   operational event (accepted/rejected, account, sizes, result).

`short_id` is 12 Crockford-Base32 characters, server-generated with
collision retry, case-insensitively unique, shown to the user for support
conversations, filterable in the admin API — and explicitly not an
authorization secret.

New code: `internal/diagnostics/` (service + store interface), handler in
`internal/api/handlers/diagnostics.go`, wired through `Dependencies` in
`internal/api/router.go`. The store interface is diagnostics-owned (the
avatar handler's is package-private and presign-only) and covers everything
the design uses: a **streaming** put (`io.Reader` — the existing s3client
`PutObject` takes `[]byte`, so this is a small s3client addition),
`GetObject` (streamed admin download fallback), `DeleteObject`,
`ListObjects(prefix)` (reconciler), and `PresignGetURL`.

### Storage

```sql
CREATE TABLE client_diagnostic_reports (
  id            UUID PRIMARY KEY,
  short_id      TEXT NOT NULL,                 -- unique, case-insensitive (expression index)
  user_id       INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  profile_id    TEXT,                          -- capturing profile, nullable
  state         TEXT NOT NULL,                 -- receiving | ready | failed
  captured_at   TIMESTAMPTZ NOT NULL,          -- client claim
  received_at   TIMESTAMPTZ NOT NULL DEFAULT now(),  -- server truth; retention keys off this
  report_type   TEXT NOT NULL,                 -- crash|anr|native_crash|hang|abnormal_exit|manual
  platform      TEXT NOT NULL,
  app_version   TEXT NOT NULL,
  crash_summary TEXT,                          -- truncated ≤ 8 KiB
  manifest      JSONB NOT NULL,                -- part 1 verbatim (≤ 64 KiB by construction)
  playback_session_ids TEXT[] NOT NULL DEFAULT '{}',
  blob_bucket   TEXT,
  blob_key      TEXT,
  blob_bytes    BIGINT,
  uncompressed_bytes BIGINT,
  blob_sha256   TEXT
);
CREATE UNIQUE INDEX ON client_diagnostic_reports (lower(short_id));
CREATE INDEX ON client_diagnostic_reports (user_id, received_at DESC);
CREATE INDEX ON client_diagnostic_reports (received_at);
```

Plain table, not partitioned — volume is orders of magnitude below
`operational_logs`; the byte-heavy payload lives in object storage. Full
stacks/traces live only in the archive; the row holds a truncated summary.

### Admin API

Under the acting-admin subtree (same placement as the `admin_logs.go`
routes):

- `GET /api/v1/admin/diagnostics/reports` — list; filters: user, platform,
  type, date range, `short_id` (exact, case-insensitive); paginated.
- `GET /api/v1/admin/diagnostics/reports/{id}` — row incl. manifest.
- `GET /api/v1/admin/diagnostics/reports/{id}/download` — presigned GET URL
  (primary, matching avatar behavior) with a streamed `GetObject` proxy
  fallback for deployments where presigned URLs aren't reachable — the
  fallback is new behavior, not something the avatar path provides.
- `DELETE /api/v1/admin/diagnostics/reports/{id}` — row + blob.

Admin download and delete emit audit/operational events. Web admin gets a
list/detail/download page next to the logs viewer, with a
playback-session-id link into the existing filtered logs view. Native apps
add no admin screens in v1 (product policy).

### Retention + reconciliation

A taskmanager task `internal/taskmanager/tasks/cleanup_client_diagnostics.go`
mirroring `cleanup_operational_log.go`, registered in `cmd/silo/main.go`
with the other cleanup tasks:

- Delete reports older than `diagnostics.retention_days` (by `received_at`),
  then enforce `diagnostics.max_bytes_per_user` oldest-first. Blob deleted
  first (tolerating already-missing objects), then the row; deletes are
  idempotent.
- Reconcile: remove `receiving`/`failed` rows older than a grace window,
  and (via `ListObjects`) any `diagnostics/*` objects whose row is absent,
  `failed`, or stale-`receiving` — so partial failures and process deaths
  mid-request never leak storage.
- Account deletion cascades rows (FK) ; the reconciler then removes orphaned
  blobs.

## Android design (`silo-android`)

New package
`android-shared/src/androidMain/kotlin/org/siloserver/silo/common/diagnostics/`.

### Logging

- `SiloLog.kt` — safe-logging facade per the contract above:
  `SiloLog.d(cat, tag, msg, attrs)` (+`i/w/e`, sanitized `Throwable`).
  Always forwards to `android.util.Log` (today's behavior unchanged) and
  offers the entry to the ring buffer.
- `LogRing.kt` — always-on in-memory ring: fixed-size array of pre-rendered
  compact entries, **non-blocking** writes (atomic index; a torn final entry
  during crash snapshot is acceptable), ~4000 entries / ~1.5 MB cap, lazy
  attribute formatting off the hot path, dropped-entry counter. Must add no
  measurable contention to playback; a playback benchmark gate accompanies
  the PR that wires player events in.
- `DiagnosticsFileLogger.kt` — only while debug logging is on: a
  single-writer coroutine draining a bounded channel (drop + count under
  backpressure) into **append-only framed segment files** (5 × 2 MB,
  app-private `files/diagnostics/logs/`, `noBackup`), compressed only at
  bundle-build time — a process death never corrupts more than the tail of
  the active segment (unlike a single long-lived gzip stream).
- Playback wiring: each `PlaybackAnalyticsListener` event plus a ~5s periodic
  `PlayerStatsSnapshot` line while debug logging is on (reuses the existing
  reducer). TV focus logging hooks the TV app's focus observers under
  `cat=focus`. Network: a Ktor hook logging `method, templated path, status,
  duration_ms` only.

### Crash and abnormal-exit capture

- `CrashCapture.kt` — installed as early as possible in both `Application`
  classes; idempotent; captures the previous
  `Thread.getDefaultUncaughtExceptionHandler` once. On crash it does the
  minimum on the dying thread: render the stack (bounded), copy what the
  non-blocking ring exposes within a hard time budget, write **one bounded
  marker file** (≤ ~512 KiB) via temp-write + atomic rename, and invoke the
  prior handler in a `finally`. No locks shared with the ring's write path,
  no coroutines, no allocation-heavy JSON building (preformatted line
  buffer). Bundle assembly happens on next launch, never in the handler.
  At most 3 pending reports are kept (oldest dropped) as a crash-loop guard.
- `ExitInfoCollector.kt` — API 30+ enhancement (minSdk is 24; many Fire TV
  devices ship below API 30, where the handler is the only source): reads
  `ActivityManager.getHistoricalProcessExitReasons()` on launch.
  `REASON_CRASH`, `REASON_CRASH_NATIVE`, `REASON_ANR` become pending
  reports. Dedupe uses a persisted bounded **fingerprint set** (process
  name, pid, timestamp, reason, status, trace hash) — not a raw timestamp
  watermark, which loses same-stamp events and breaks on clock changes.
  `getTraceInputStream()` is treated as optional opaque bytes with a bounded
  read: ANR text → `crash/stack.txt`; API 31+ native tombstone protobuf →
  `crash/tombstone.pb` (never decoded into the manifest). UEH-captured
  events are correlated by fingerprint so the same death isn't reported
  twice.
- Next foreground launch, per the consent model: Ask → aggregated,
  incident-specific prompt (one prompt covering N pending reports; default
  focus **Don't send** on TV; "Always send" requires a second confirmation;
  full-screen review flow on TV rather than a small dialog); Always →
  silent upload, at most one bundle per crash fingerprint per day;
  Never → purge. Per-fingerprint prompt suppression with backoff prevents
  crash-loop prompt fatigue. Pending reports expire after 7 days —
  visibly (the Diagnostics screen shows pending reports with expiry), not
  silently.

### Device snapshot

`DeviceSnapshotCollector.kt` assembles `device.json` from the existing
probes plus the new accessors listed in the contract section (display mode
list, `Build.DEVICE`/form factor, `AudioManager.getDevices()` enumeration,
suppression-registry export). A compact snapshot is persisted with each
crash marker (`provenance: pre_failure`) since post-restart audio/display
state can differ from crash-time state.

### Settings + consent UI

- `DiagnosticsSettingsStore.kt` — DataStore. Deliberately **not** the
  per-profile pattern of `AndroidPlayerSettingsStore`: consent and pending
  reports are keyed by server+account (per the consent model), and debug
  logging is per-device. Holds consent records, crash-report mode,
  debug-logging flag, fingerprint set, session tracker, and status cache.
- Phone: "Diagnostics" section in `SettingsScreen.kt`; TV: rail category in
  `TvSettingsScreen.kt`. For non-child profiles the section is **always
  visible** and state-aware rather than hidden when the server gate is off — it distinguishes
  `available` / `disabled by server` / `storage unavailable` / `offline`,
  and always shows local pending reports (count, size, expiry) with
  review/delete controls. Uploads are simply unavailable in non-available
  states; a report captured while the gate was off is not auto-uploaded
  later unless consent for that server+account exists.
- Manual capture is the primary support flow and is stronger than a bare
  "send now": **Start diagnostic capture** (turns on verbose capture,
  shows an indicator) → user reproduces the issue → **Stop & review** →
  summary (categories, time range, sizes, destination) → send. A one-shot
  "Send diagnostics now" remains for quick cases but warns when only the
  in-memory ring is available ("this bundle contains only the last few
  minutes of basic logs").
- After upload, the `short_id` is displayed and kept in a small sent
  history so a user can read it to their admin later.

### Upload

- `shared/commonMain/…/network/api/DiagnosticsApi.kt` — `getStatus()` and
  `uploadReport(manifestJson, bundleBytes)` via Ktor
  `MultiPartFormDataContent` (first multipart use in the client — verified
  none exists — written as a small reusable helper), using the existing
  `safeApiCall`/`ApiResult` pattern, wired through Koin like other `*Api`
  classes.
- `DiagnosticsUploader.kt` — builds the tar.gz (marker + ring/segment files
  overlapping the incident window + breadcrumbs + snapshot), streams it to
  the endpoint, deletes the pending report only on 201, records `short_id`.
  On `quota_exceeded`/`too_large`/`disabled`/`storage_unavailable` it keeps
  the pending report and backs off per `retry_after`; on
  `unsupported_schema` it surfaces "server needs an update" rather than
  silently retrying until expiry.

## Apple design (`silo-apple`)

Mirrors Android with platform-native mechanisms, and with tvOS scoped
honestly — **MetricKit diagnostic payloads (crash/hang) are iOS-only; the
SDK marks them unavailable on tvOS**, and delivery is delayed (typically
daily, while the app runs), not "next launch".

- **Ring + facade**: `DiagLog` implementing the same safe-logging contract.
  The player's existing `cmpLog` helper (`PlayerLog.swift`) is **extended**
  into a tee — it keeps `print()`ing (preserving `devicectl --console`
  capture on tvOS) and also feeds the ring; remaining direct `print()` call
  sites in `Screens/Player/` migrate to it curated, per the redaction
  contract. OSLog-based subsystems are harvested at bundle-build time via
  `OSLogStore(scope: .currentProcessIdentifier)` (deployment targets are
  iOS/tvOS 26, far above the OSLogStore 15+ floor), merged into
  `logs.jsonl` with provenance.
- **Breadcrumb journal**: because MetricKit reports arrive in a *later*
  process, the in-memory ring is gone by delivery time. While crash
  reporting is enabled, a small append-only breadcrumb journal (bounded,
  rotating, crash-safe appends: lifecycle transitions, playback start/stop,
  route changes, screen changes) persists across runs and becomes
  `breadcrumbs.jsonl`, matched to a diagnostic by `capture_session_id` and
  time window. Snapshots taken at bundle-build time are labeled
  `provenance: post_restart`; the design never presents post-restart state
  as crash-time state.
- **iOS crash/hang detection**: an `MXMetricManager` subscriber. Each
  received `MXCrashDiagnostic`/`MXHangDiagnostic` becomes its **own**
  pending report: raw diagnostic JSON preserved as `crash/metrickit.json`,
  deduped by a canonical hash of the diagnostic (not timestamps), carrying
  its own app version and reporting-period bounds. Prompt copy is
  type-specific ("crashed" vs "was not responding") and honest about timing
  ("yesterday during playback"). Delivery is verified on physical devices;
  parsing is tested via injected fixtures.
- **tvOS abnormal-exit detection**: no crash stacks in v1. A dirty-exit
  sentinel (marker written at launch, cleared on clean
  background/termination) plus the breadcrumb journal yields
  `type: abnormal_exit` reports — "Silo did not shut down cleanly last
  time" — with breadcrumbs and device snapshot but no stack. An in-process
  crash reporter for tvOS is explicitly deferred (see Non-goals).
- **Device snapshot**: `AppleDeviceIdentity.current` +
  `ApplePlaybackV3Capabilities.snapshot()` for identity/codec/HDR context;
  audio reported as route metadata with hashed route UIDs and
  `passthrough: "unknown"` until a real output-capability probe exists (the
  current one is a private placeholder).
- **Settings/consent**: Diagnostics section in `SettingsView` (iOS) and
  `TVSettingsView` (tvOS) with the same state-aware presentation, manual
  capture flow, and consent rules.
- **Upload**: a small multipart body writer added to `HTTPClient` (verified:
  JSON-only today, no multipart/uploadTask), same status/error handling as
  Android.
- Housekeeping note: product bundle IDs are already `org.siloserver.silo`;
  only legacy keychain/logging identifiers (`com.continuum.*`) remain, so
  nothing here blocks on rebranding.

## Privacy summary

- **Consent boundaries**: no network transmission of diagnostic data without
  prompt confirmation, a server+account-bound Always election, or an
  explicit manual send. The always-on ring is memory-only; persistent
  capture (segments, breadcrumbs, markers) is app-private, size-capped,
  backup-excluded, and purged by Never/sign-out/server removal.
- **Never collected**: credentials, `Authorization`/profile-token values,
  request bodies, query strings, full URLs (normalized at collection),
  unsanitized exception dumps. Bearer-token matching at bundle time as
  defense in depth. Golden leak fixtures in CI.
- **Deliberately collected** (disclosed in View report): device identity
  incl. device name, media titles appearing in playback logs, templated
  server paths. The recipient is the user's own server admin, who already
  has server-side access to this information; that is the trust model this
  feature is scoped to — and why third-party destinations are out.
- **Persistent identifiers**: the existing device IDs (already sent to the
  server today) plus hashed audio-route UIDs; each is enumerated in the
  schema docs with its purpose and retention.
- **User control**: state-aware Diagnostics screen with pending list,
  review, delete, sent history; admin-side deletion; account deletion
  cascades reports; admin access is acting-admin-only and audited.
- **Bounded retention**: defaults 30 days / 200 MiB per user, admin-tunable,
  enforced server-side.

## Rollout plan (vertical slices, each independently shippable)

1. **Server foundation** — settings + storage-validated gate, status
   endpoint, hardened ingest with state machine, schema + fixtures, admin
   API, retention/reconciler task, operational audit events. Feature stays
   off; nothing user-visible.
2. **Contract fixtures in clients** — JSON Schema + golden fixtures wired
   into Android/Apple CI; safe-logging facades + rings land **dark** (no
   UI, no capture persistence, no prompts).
3. **Android end-to-end slice** — consent model + Diagnostics settings UI +
   crash marker/ExitInfo capture + device snapshot + uploader + manual
   capture flow, with the initial curated logging migration (player tags +
   TV focus + network hook + stats snapshots). First release where a user
   can be asked for, and send, a useful report.
4. **Android coverage expansion** — remaining curated call-site migration,
   breadcrumbs-on-Android if wanted, prompt-fatigue tuning from the field.
5. **Apple slice** — cmpLog tee + OSLogStore harvest + breadcrumbs +
   MetricKit (iOS) + exit sentinel (tvOS) + settings/consent + multipart
   upload.
6. **Web admin viewer + correlation UX** — list/detail/download page,
   playback-session pivot into the logs viewer.
7. **Later / separate decisions** — symbolication job, tvOS in-process
   crash reporter, native-app admin viewer, macOS.

Slices 3 and 5 ship complete consent→capture→upload flows; intermediate
releases never prompt for reports they can't send.

## Open questions

1. Should users be able to delete their **uploaded** reports from the client
   (self-service deletion), or is pending-report control + admin deletion
   enough for v1?
2. tvOS: is abnormal-exit detection (no stacks) acceptable for v1, or does
   the Shield/Fire TV debugging need justify evaluating an in-process crash
   reporter (PLCrashReporter/KSCrash) with its signal-safety and review
   trade-offs?
3. Do we want breadcrumbs on Android in v1 (cheap once the facade exists),
   or is the UEH marker + ExitInfo enough there?
4. Localization of prompt/consent strings at launch vs English-first.
5. Exact quota defaults (20/day, 200 MiB/user, 10 MiB/bundle) — tune before
   enabling anywhere real.
6. Whether the status payload should also fold into an existing server-driven
   config refresh response later (clients poll both today); the dedicated
   endpoint ships first either way.

## Review notes

This spec was adversarially reviewed before the draft PR: two independent
review passes (a fact-check of every codebase claim against the three repos,
and a design attack across consent, crash-capture correctness, ingest
robustness, storage consistency, UX, and phasing). Material corrections
adopted include: tvOS MetricKit unavailability and honest MetricKit timing;
server+account-bound consent; storage-validated gating (private object store
is optional in real deployments); streaming bounded ingest with a report
state machine and orphan reconciliation; the archive container replacing a
bare log gzip; collection-time typed redaction replacing scrub-at-package;
fingerprint-based ExitInfo dedupe; non-blocking ring + bounded crash marker;
recent-session trackers (only the active playback session ID exists today);
and honest Apple snapshot capabilities (passthrough currently unknown).
