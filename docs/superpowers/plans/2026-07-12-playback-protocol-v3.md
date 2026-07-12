# Playback Protocol v3 Server Implementation Plan

**Status:** Proposed
**Scope:** `silo-server`, with coordinated contract fixtures in `silo-android`
**Protocol owner:** Silo server
**Client target:** Android Media3-only playback; legacy web and Apple playback remain supported

## 1. Goal

Implement the server half of Silo playback protocol v3 so a client can report
its current device and output capabilities and receive one complete executable
plan for:

- authenticated original HTTP playback;
- progressive or HLS remux;
- video-copy with audio adaptation;
- HLS video transcode;
- subtitle render, conversion, or burn-in; or
- a terminal `adaptation_unavailable` outcome.

The client must never choose FFmpeg recipes. The server must never claim that a
route preserves Dolby Vision, HDR, lossless audio, or subtitle fidelity unless
the source metadata, client/output capability, selected recipe, and installed
tooling support that claim.

The normative behavior remains in the sibling Android repository:

- `silo-android/docs/playback/01-media3-only-player-architecture.md`
- `silo-android/docs/playback/02-migration-compatibility-validation.md`
- `silo-android/shared/src/commonMain/kotlin/org/siloserver/silo/model/playback/PlaybackProtocolV3.kt`

This document maps that contract onto the current server. It does not redefine
the protocol.

## 2. Verified server baseline

The implementation should reuse the current playback stack rather than replace
it:

| Existing behavior | Current owner | v3 implication |
| --- | --- | --- |
| Legacy start, authorization, profile checks, track preferences, session creation | `internal/api/handlers/playback.go` | Preserve the legacy branch byte-for-byte where possible; move shared operations behind helpers. |
| Direct/remux/transcode choice | `internal/playback/resolver.go` | Replace its broad codec/container booleans with a v3 planner for v3 requests only. Do not change legacy decisions during the first rollout. |
| Byte-range original delivery | `internal/playback/directplay.go`, `internal/api/handlers/stream.go` | Reuse as `original_http`. |
| Progressive MP4 remux and audio-to-AAC | `internal/playback/remux.go`, `internal/api/handlers/stream.go` | Reuse as `server_remux_progressive`; make every transformation explicit in the plan. |
| HLS copy/transcode, timelines, restart/reconstruct | `internal/playback/transcode.go`, `internal/playback/transcode_manager.go` | Reuse as `server_remux_hls` or `server_transcode_hls`. |
| Local and remote transcode start | `HandleStartTranscode`, `internal/transcodenode/server.go`, `internal/nodepool` | Extract a server-callable starter so v3 can return the final manifest without a second client recipe request. |
| Playback sessions, policy and limits | `internal/playback/session.go` | Extend session state with v3 attempt/plan identity; keep current admission controls authoritative. |
| Source video/audio/subtitle metadata | `internal/models/media.go`, `internal/scanner/probe.go` | Most required fields already exist, including DV profile, BL compatibility ID, EL presence, color/range, bit depth, layout, and channels. MEL/FEL classification and transformation provenance are not yet represented. |
| Subtitle extract/conversion/burn-in | `internal/playback/subtitles.go`, `subtitle_stream.go`, transcode arguments | Reuse the executors; add an explicit policy decision and artifact contract. |
| Restart-safe signed stream recipes | `internal/playback/recipecard.go`, `internal/streamtoken` | Continue using signed recipe tokens; v3 plans describe the resulting URL and timeline. |

The largest correctness gaps in the current resolver are expected and must not
be carried into v3:

- transcode-disabled incompatibility currently falls back to attempting direct
  playback instead of returning a terminal result;
- codec support is modeled as a string union, without profile, level, bit depth,
  frame rate, range, sink layout, or subtitle-fidelity validation;
- clients currently supply transcode codec, bitrate, segment, and burn-in
  recipe details through `/playback/transcode/start`;
- the start response does not contain an authoritative effective recipe,
  validation claims, transformations, or timeline for every delivery;
- no idempotent replan or attempt-scoped route-event API exists.

## 3. Architecture decision

### 3.1 Additive dispatch on the existing start endpoint

Keep `POST /api/v1/playback/start` for compatibility. Refactor its handler into
a small protocol dispatcher:

1. Decode only an envelope containing `protocol_version`.
2. When it equals `3`, decode strictly as `PlaybackStartRequestV3` and call the
   v3 orchestration service.
3. When it is absent or not `3`, pass the original body to the existing legacy
   decoder and behavior.

Use buffered request bytes or `json.RawMessage`; never decode the body twice
from the socket. Unknown fields remain tolerated for additive compatibility,
but required v3 fields and closed enum values are validated explicitly.

Do not overwrite the existing request/response structs with a union of legacy
and v3 fields. That would make missing nested capabilities silently look like a
valid legacy request and recreate the current false-direct failure mode.

### 3.2 Introduce one v3 orchestration service

Add a service in the `internal/playback` domain that owns:

- input normalization and validation;
- stable track resolution;
- source and capability evaluation;
- ordered candidate construction;
- policy and capacity checks;
- deterministic plan identity and loop prevention;
- transport startup;
- terminal outcome mapping;
- replan idempotency; and
- plan/session state transitions.

HTTP handlers authenticate, authorize, decode, call the service, and encode the
result. They do not contain route-selection rules.

Recommended files:

```text
internal/playback/protocol_v3.go              wire/domain types and enums
internal/playback/plan_v3.go                  candidate planner
internal/playback/capabilities_v3.go          source/capability validators
internal/playback/recipe_v3.go                normalized executable recipes
internal/playback/plan_key_v3.go              canonical IDs and attempt keys
internal/playback/tracks_v3.go                stable track identities/remapping
internal/playback/subtitle_policy_v3.go       subtitle decision policy
internal/playback/transformations_v3.go       installed-tool/recipe registry
internal/playback/protocol_v3_test.go
internal/playback/testdata/protocol_v3/       golden contract/planner fixtures

internal/playback/planstore/store.go           persistence interfaces
internal/playback/planstore/postgres.go        attempts, replans and route events

internal/api/handlers/playback_v3.go           start/replan/event handlers
internal/api/handlers/playback_transport.go    reusable transport starter
internal/api/handlers/playback_v3_test.go
```

If implementation reveals that the wire and domain structs need different
types, keep JSON structs in `handlers` and convert at the boundary. Do not add
JSON tags throughout unrelated session and FFmpeg types merely to save mapping
code.

### 3.3 Reconcile additive client-contract gaps before freezing v3

The current Kotlin v3 types are sufficient to negotiate and execute a plan,
but they do not yet carry every fact the normative direct-play rules require.
The server cannot manufacture those facts. Phase 0 therefore includes a
coordinated additive Android contract change:

| Current gap | Required additive contract |
| --- | --- |
| Video capability is a codec-name list plus a global maximum resolution. | Add per-codec decode entries with supported profiles/levels, bit depths, maximum width/height/frame rate/bitrate, and hardware/software status. |
| Audio passthrough is a codec-name list plus one global maximum channel count. | Add per-codec supported channel counts/layouts; retain the old fields for tolerant readers. |
| A plan does not expose requested versus effective media file when alternate-version selection occurs. | Add requested/effective media file IDs and a normalized source descriptor to the plan. |
| Subtitle decision does not expose the effective fidelity policy. | Add `subtitle_fidelity_policy` beside mode/artifact. |
| Route events cannot explicitly report replan identity, local PCM recovery, retry outcome, decoder timing, or requested/effective quality. | Add optional structured fields or a versioned, bounded diagnostic schema; the server enriches delivery/recipe/source facts from stored plan state rather than trusting duplicates from the client. |
| Android's profile-quality canonicalizer passes unknown stored labels through and does not produce `original`. | Canonicalize the closed v3 values and aliases client-side; keep server normalization tolerant for rolling upgrades. |

Gate the detailed behavior with additive client feature tokens such as
`detailed_decode_capabilities`, `layout_aware_passthrough`, and
`playback_route_diagnostics`. A v3 client missing them remains parseable, but
unknown profile/layout facts cannot satisfy a strict direct claim. The planner
must choose a conservative adapted route or terminal result.

Freeze the exact JSON field names only after Go/Kotlin golden fixtures exist.
Do not implement a server-only guess and call the contract complete.

The response engine mapping is fixed:

| Delivery | `engine` |
| --- | --- |
| `original_http` | `media3_direct` |
| `server_remux_progressive` | `media3_progressive_remux` |
| `server_remux_hls` | `media3_hls` |
| `server_transcode_hls` | `media3_hls` |

Android currently accepts header refresh modes `none` and `session`; do not
emit `refresh_endpoint` until the client implements it.

Never emit legacy/client-owned runtime values in a v3 plan:
`mpv_direct`, `client_local_loopback`, `external_player`, or
`client_local_normalization`. They remain decode-only compatibility values on
Android.

### 3.4 Keep v3 dark until the complete route is executable

Add a dynamic server setting `playback.protocol_v3_enabled`, default `false`.
Read it through `PlaybackSettingsReader.Get` on each capability/start request,
as the existing `allow_4k_transcode` path does. Do not load it only through
`internal/config/db_loader.go`; that would turn rollback into a restart-required
change.
Also add `GET /api/v1/playback/capability` because `/api/v1` features use
capability detection rather than server-version sniffing.

When a v3 start arrives while disabled, return a successful negotiation
response with no allocated session and without `playback_plan_v3` in
`server_features`. This intentionally triggers Android's
`server_upgrade_required` path without leaking a legacy session.

The capability response should contain:

```json
{
  "enabled": false,
  "protocol_versions": [3],
  "features": [],
  "deliveries": [],
  "transformations": [],
  "reason": "disabled"
}
```

Only advertise `playback_plan_v3` after every enabled delivery and
transformation passes Phase 0 fixtures on the deployed build.

## 4. HTTP contract and ownership

### 4.1 Endpoints

| Endpoint | Success | Responsibility |
| --- | --- | --- |
| `GET /api/v1/playback/capability` | `200` | Feature/protocol and installed transformation discovery. |
| `POST /api/v1/playback/start` | `201` | Authorize source, create a session, select/start a route, return playable or terminal v3 response. |
| `POST /api/v1/playback/{session_id}/replan` | `200` | Idempotently replace the active plan at the supplied media position. |
| `POST /api/v1/playback/route-events` | `202` | Validate and enqueue attempt-scoped diagnostics. |

All four routes use normal account authentication. Start, replan, and route
events require profile auth. `X-Profile-Id` remains authoritative; a body
`profile_id` must match it. Replan additionally verifies session ownership and
that `playback_attempt_id` belongs to the session.

Playable and `adaptation_unavailable` are protocol outcomes, not HTTP errors.
Use HTTP errors only for transport/API failures such as malformed input,
unauthorized access, missing files, or an idempotency conflict.

### 4.2 Strict request validation

Reject before allocating a session when any of these fail:

- protocol is not exactly `3`;
- `playback_attempt_id` is absent or not a bounded UUID/ULID-like identifier;
- file/profile authorization fails;
- track ID and fallback index disagree;
- output-route generation is negative or disagrees with the nested output
  context;
- codec/container/feature lists exceed bounded counts or string lengths;
- bandwidth, bitrate, resolution, attempt count, or position is outside sane
  bounds; or
- a replan's failed plan is not the session's active plan.

Normalize codecs, containers, layouts, and dynamic-range labels once at the
boundary. Accept bounded known quality aliases such as `4k`; fold an unknown
rolling-upgrade quality value to `auto` with a decision warning instead of
hard-failing playback. Preserve the original values only in bounded
diagnostics.

### 4.3 Complete response invariant

A `playable` response is valid only after its transport can be fetched or has
successfully entered its startup state. It includes:

- protocol and feature negotiation;
- stable `session_id` and deterministic `plan_id`;
- delivery and Media3 engine;
- final URL, stream protocol, container/MIME, scoped headers, and supported
  header-refresh mode;
- exact player/source timeline mapping and seek window;
- selected stable audio/subtitle identities;
- effective codec, range, dimensions, frame rate, bitrate, channels and layout;
- route validation claims;
- subtitle mode/artifact;
- named transformations and degradation warnings; and
- a stable decision reason code.

Never return a placeholder transcode URL and expect v3 Android to call the
legacy transcode-start endpoint.

## 5. Domain invariants

### 5.1 Stable tracks

Use the current Android-compatible identity initially:

```text
file:{media_file_id}:audio:{ffmpeg_audio_ordinal}
file:{media_file_id}:subtitle:{combined_subtitle_ordinal}
```

Freeze the combined subtitle ordering used by `buildSubtitleURLs`:

1. external subtitles in stored order, starting at zero;
2. embedded subtitles at `len(external) + embedded_ordinal`; skipped legacy
   bitmap entries retain their ordinal hole; and
3. downloaded subtitles in repository `created_at` order at
   `len(external) + len(all_embedded) + downloaded_ordinal`.

The identity is paired with the effective media file ID. Centralize generation,
parsing, URL mapping, and alternate-version remapping in `tracks_v3.go`, and
freeze the exact inventory in Phase 0 fixtures. A plan that switches file
version must return the effective file's identity after matching the requested
track by signature. Do not echo a requested-file track ID with an
effective-file index.

Longer term, scanner-persisted immutable stream IDs can replace ordinal IDs in a
future protocol version; do not change v3 identity semantics after release.

### 5.2 Deterministic plan IDs and loop prevention

The Android attempt key currently includes `plan_id`. Therefore a random plan
ID would let the same failed recipe reappear under a new key and defeat loop
prevention.

Derive `plan_id` deterministically within a playback attempt from:

- `playback_attempt_id`;
- effective file ID;
- delivery and stream protocol/container;
- normalized video/audio recipe;
- effective track IDs;
- subtitle mode/artifact type;
- sorted transformation names; and
- policy/recipe version.

Do not include expiring URLs, JWTs, timestamps, node hostnames, or the output
route generation in `plan_id`. The same effective recipe has the same plan ID;
an output-route change remains distinguishable because Android includes
`output_route_generation` in its attempt key.

Implement the client's FNV-1a 64-bit `v3:<16-lowercase-hex>` attempt-key
algorithm in Go. The UTF-8 canonical string is exactly:

```text
plan_id
|KOTLIN_DELIVERY_ENUM_NAME
|KOTLIN_STREAM_PROTOCOL_ENUM_NAME
|lowercase_container_or_empty
|lowercase_video_codec_or_empty
|lowercase_audio_codec_or_empty
|(width_or_0)x(height_or_0)
|bitrate_kbps_or_0
|lowercase_dynamic_range_or_empty
|KOTLIN_SUBTITLE_MODE_ENUM_NAME
|comma_joined_transformation_names_sorted_by_name
|output_route_generation
|comma_joined_local_mutations_sorted_lexically
```

The newlines above are explanatory only; the hashed value is one string joined
by literal `|` characters. Enum components are Kotlin constant names such as
`ORIGINAL_HTTP`, `HTTP_PROGRESSIVE`, `HLS`, and `BURN_IN`, not lowercase wire
tokens. Hash with offset basis `0xcbf29ce484222325` and prime
`0x100000001b3`, then zero-pad to 16 lowercase hex digits.

Before committing a candidate, compute its base key for the request's output
route and reject it when present in `attempted_plan_keys`. Canonical fixtures
must be generated by the checked-in Kotlin implementation and consumed as
opaque expected values by Go tests; a Go-generated fixture would make the
parity test circular. Include transformation-order, empty/default-field,
output-route, and local PCM-mutation cases.

Server-side ordered candidate IDs are not security tokens.

### 5.3 Session and replacement semantics

Treat replan as one replacement transaction, serialized with the per-session
lifecycle lock:

1. validate policy, source, candidate and tooling without touching the active
   route;
2. reserve the active session's existing stream/transcode capacity slot so the
   replacement is neither double-counted nor denied by its own predecessor;
3. start the successor in a plan-scoped staging directory/transport generation,
   or under an explicitly replacement-linked new session ID;
4. wait until the successor URL is fetchable or its startup state is validated;
5. atomically commit effective file, tracks, recipe, plan ID, timeline and
   signed route;
6. close and reap the predecessor; and
7. release or transfer the capacity reservation exactly once.

If any pre-commit step fails, leave the old route and session usable and return
a typed terminal/retryable result. Never run two FFmpeg writers against the
same output directory. Progressive request-scoped transports may drain until
the old client request disconnects; they do not require a destructive
close-first swap.

Prefer retaining the same logical playback session so progress, scrobble and
admin history remain continuous. If the transport design requires a new public
session ID, return it explicitly and keep the old ID alive until the successor
is ready. Android will stop the old ID afterward and ignore stop failures. Keep
the legacy stop endpoint's behavior unchanged; internal v3 replacement and
cleanup paths must treat an already-stopped session as success even though the
current `SessionManager.StopSession` returns `ErrSessionNotFound`.

### 5.4 Replan idempotency

Create a timestamped Goose migration, generated with:

```text
make migrate-create NAME=add_playback_protocol_v3
```

Add short-lived persistence for:

- playback attempt/session/current-plan state;
- `(session_id, replan_request_id)` request digest, state, lease and serialized
  response; and
- route events.

Current native restart reconstruction is carried by signed stream tokens, not
a durable plan database. This plan store is the first persisted v3 control-
plane state; it complements rather than replaces recipe tokens and therefore
needs explicit expiry/cleanup and token-to-plan reconciliation tests.

For a duplicate replan ID:

- same digest and completed state returns the cached response;
- same digest and active lease waits on/single-flights the owner, then returns
  the response;
- different digest returns `409 idempotency_key_reused`; and
- an expired lease may be reclaimed after reconciling the session's current
  recipe/plan state.

Store only the minimum response needed for replay and expire it with the
playback attempt. Never put source paths or authorization headers in route
events. Signed URLs in cached responses inherit the attempt TTL and are removed
by cleanup.

## 6. Planner rules

### 6.1 Candidate order

Build candidates in least-destructive order, but validate every candidate
independently:

1. `original_http`;
2. `server_remux_progressive` when the container alone is incompatible and
   progressive seek semantics are acceptable;
3. `server_remux_hls` when HLS packaging is required;
4. `server_remux_hls` with copied video plus audio adaptation, with the audio
   change named in the effective recipe, transformations, claims and warnings;
5. `server_transcode_hls`; and
6. terminal `adaptation_unavailable`.

Remux is not mandatory before transcode. Skip directly to the first valid
candidate. A selected subtitle may independently add an artifact or force
burn-in.

### 6.2 Direct eligibility

Original delivery requires all of:

- validated source file and byte-range path;
- client container extractor support;
- selected video codec/profile/level/bit depth/resolution/frame rate/bitrate;
- compatible source dynamic range and current display output;
- selected audio local decode or exact sink passthrough by codec and channel
  layout;
- renderable selected subtitle at the required fidelity, or an independent
  converted artifact; and
- no requested quality reduction.

Missing metadata is `unknown`, not compatible. A probe repair may run before
planning, but the planner must terminal or adapt when required fields remain
unknown.

### 6.3 Quality policy

Move target selection out of clients. Add a pure `QualityPolicy` that maps:

- `original` to no user-requested reduction, while still permitting mandatory
  compatibility adaptation;
- a fixed rung to a maximum height with no upscaling; and
- `auto` to the lowest of device maximum, bandwidth estimate with safety
  margin, user cap, metered policy, and administrator constraints.

Start with one server-owned ladder. Reconcile it with the existing web constants
in `web/src/player/hooks/useTranscodeQuality.ts`; do not maintain two unrelated
bitrate tables. The v3 result returns the actual height/bitrate and decision
reason. Multi-variant ABR is explicitly outside this work.

### 6.4 Audio

Evaluate the selected track, not `MediaFile.CodecAudio` alone:

- exact sink passthrough codec plus channel count/layout permits audio copy and
  a `passthrough=true` claim;
- client decode support permits copy with `passthrough=false`;
- otherwise use video-copy/audio adaptation when permitted;
- otherwise terminal with `transcoding_disabled`,
  `audio_conversion_unsupported`, or policy/capacity reason.

The effective recipe must report the actual output channels/layout. Do not call
AAC stereo output Atmos-preserving. DTS core extraction, E-AC-3 conversion, or
other lossy/core-only changes are named transformations with warnings.

### 6.5 HDR and Dolby Vision

Use `VideoTrack` fields already populated by the scanner: DV profile, BL
compatibility ID, EL presence, range type, color metadata, bit depth, profile,
level, and dimensions.

Add an explicit enhancement-layer classification:

```text
none | mel | fel | unknown
```

An analyzer interface may use an installed `dovi_tool`/libdovi implementation;
without it, Profile 7 with an EL remains `unknown`. Unknown must never be
reported as MEL, FEL, or validated native DV.

Represent transformations in a registry with:

- stable name and recipe version;
- required binary/filter/hardware capability;
- accepted source metadata;
- promised output range;
- argument builder;
- validation fixture; and
- failure-to-terminal mapping.

Initial entries should cover only paths proven by fixtures, for example:

- `dv_metadata_strip_to_hdr10` using a compatible HDR10 base layer;
- `dv_p7_to_p8_1` only when the selected toolchain and fixture prove the output;
- `hdr_to_sdr_tonemap` only when integrated and remote transcode paths both
  implement the same declared result.

FFmpeg's `dovi_rpu` bitstream filter can strip DV metadata without decoding,
and `dovi_split` can separate Profile 7 layers
([FFmpeg bitstream-filter documentation](https://ffmpeg.org/ffmpeg-bitstream-filters.html#dovi_005frpu)).
[`dovi_tool`](https://github.com/quietvoid/dovi_tool) can inspect RPU metadata
and convert compatible streams. Probe these capabilities at startup and expose
only registered, available transformations. Do not silently run the current
Profile 7 strip path and still claim Dolby Vision preservation.

The current progressive remux path is not v3-safe unchanged: when `dovi_rpu`
is present it strips Profile 7 metadata automatically, while the no-filter path
leaves the copied bitstream without that named adaptation. Refactor remux input
to require an explicit transformation choice. A Profile 7 progressive remux is
eligible only when it either preserves a fixture-validated native P7 stream or
runs the registered `dv_metadata_strip_to_hdr10` transformation against a
validated HDR10-compatible base layer. When the required filter/tool is
missing, disqualify that remux candidate rather than emitting an ambiguous
stream.

### 6.6 Subtitles

Resolve exactly one mode:

- `off`: no selected track;
- `render`: return the source artifact when Media3 can render it with required
  fidelity;
- `convert`: return a v3 artifact URL, MIME, format and timing origin;
- `burn_in`: force video encoding and declare any HDR/range degradation; or
- terminal when the selected subtitle cannot meet policy.

Map `preserve` to `require_authored_fidelity` and `compatible` to
`allow_simplified_rendering`, subject to administrator policy. ASS/SSA styling,
font attachments, PGS, VobSub and DVB must each have fixtures; do not infer all
bitmap paths from one codec test.

Converted artifacts need session ownership, signed or authenticated fetches,
bounded cache lifetime, and timing relative to the returned stream origin.

## 7. Transport orchestration refactor

Extract the body of `HandleStartTranscode` into a server-callable operation that
accepts a validated normalized recipe and returns:

- final manifest URL;
- effective local/remote node and hardware encoder;
- effective file ID;
- final codecs/resolution/bitrate/channels/range;
- player/source timeline mapping;
- seek-window semantics; and
- typed terminal/startup failure.

Both the legacy endpoint and v3 service call this operation. The legacy handler
continues accepting its existing body and maps it to the normalized recipe;
there is no `/api/v1` breaking change.

Jellyfin compatibility is behaviorally out of scope for protocol v3. Its
handlers currently start local/remote transcodes through parallel paths and a
shared `TranscodeManager`, not through `HandleStartTranscode`. Preserve those
wire and lifecycle paths during this work and keep their regression tests
green. The extracted lower-level starter may be adopted by jellycompat only in
a separately reviewed convergence change; do not half-migrate one of its local
or remote branches.

Apply the same extraction to progressive remux startup where necessary. The v3
planner chooses a recipe; the transport starter is not allowed to silently
change it. If safety logic must change copy to encode—for example seeked HEVC or
subtitle burn-in—the starter returns the effective normalized recipe so the
plan, claims, warnings, recipe card, and FFmpeg process remain identical.

## 8. Route events and observability

Add `playback_route_events` with bounded columns for:

- playback/session/plan/plan-attempt identities;
- event and failure classification;
- fallback reason;
- output-route generation;
- sanitized diagnostics JSON;
- user/profile/client metadata derived from auth and headers; and
- received timestamp.

Accept only a closed event set initially:

```text
plan_selected
plan_invalidated
plan_failed
first_frame
terminal
stopped
```

Allowlist diagnostic keys and cap key count, key/value length, body size, and
events per user/attempt. Strip URLs, headers, tokens, file paths, free-form
stack traces, and unknown keys. Enqueue/batch writes so telemetry never blocks
playback planning.

Add exact release queries for:

- starts by delivery, recipe, dynamic range and client model;
- first-frame rate and latency;
- replan/failure rate by classification;
- terminal reasons;
- PCM retry and passthrough outcomes;
- repeated-key/loop prevention; and
- DV/HDR degradation transformations.

Operational logs include IDs and reason codes but no stream tokens.

## 9. Implementation sequence

Each phase should be a separate reviewable PR and link the playback capability
epic/sub-issue.

### Phase 0 — Freeze the contract

- Add Go structs/enums mirroring the Kotlin v3 wire model.
- Split only the start-envelope dispatch: protocol `3` returns the disabled
  no-session negotiation response, while absent/non-3 requests replay the
  buffered body through the unchanged legacy decoder.
- Resolve the additive capability, effective-file, subtitle-policy and route-
  diagnostic gaps from section 3.3 in a coordinated Android change.
- Add request/response JSON golden fixtures and canonical attempt-key fixtures.
- Add strict validation tests, legacy-envelope tests, and unknown-field tests.
- Add `GET /playback/capability` and the disabled negotiation response.
- Keep `playback.protocol_v3_enabled=false`.

**Exit:** Go and Android decode the same fixtures and produce identical attempt
keys; Android populates the detailed capability fields; legacy start tests are
unchanged; disabled v3 starts allocate no legacy playback session.

### Phase 1 — Source facts and transformation registry

- Complete normalized source descriptors from `MediaFile` tracks.
- Add MEL/FEL/unknown metadata and conservative probe behavior.
- Add installed FFmpeg/dovi tool capability probes.
- Define transformation registry and terminal reasons.
- Add scanner/model migration or JSONB backfill handling as required.

**Exit:** every source fact used by the planner is present or explicitly
unknown; no unavailable transformation is advertised.

### Phase 2 — Pure planner

- Implement capability normalization, stable tracks, quality policy, direct
  eligibility, candidate ordering, recipe claims, deterministic plan IDs and
  attempt-key rejection.
- Cover direct, progressive remux, HLS copy, audio adaptation, full transcode,
  subtitles and terminal outcomes with table-driven tests.
- Do not start sessions or FFmpeg from planner tests.

**Exit:** the full Phase 0 decision matrix passes as a pure deterministic
function.

### Phase 3 — Start orchestration

- Wire the Phase 0 dispatcher's protocol-3 branch to the completed v3
  orchestration service; keep the legacy branch unchanged.
- Extract reusable transcode/remux transport starters.
- Start sessions and final transports from the v3 service.
- Return complete plans with final URLs and timelines.
- Persist attempt/current-plan state.

**Exit:** every playable fixture fetches and prepares its returned URL; a
terminal fixture allocates no leaked session/transcode.

### Phase 4 — Replan and idempotency

- Add route, handler, persistent idempotency records and per-session
  serialization.
- Replan track/quality/output changes and classified runtime failures.
- Reuse session IDs when safe; close superseded transports exactly once.
- Reject attempted recipes and stale failed plan IDs.
- Add restart, duplicate request, concurrent request and crash-window tests.

**Exit:** repeated identical replan requests return the same result without a
second FFmpeg process; a different payload under the same ID conflicts.

### Phase 5 — Route events and rollout controls

- Add sanitized asynchronous route-event ingestion and retention cleanup.
- Add release queries/dashboards and structured plan logs.
- Add shadow planning for legacy starts on validation servers: compute and log
  v3 choice without advertising or executing it.
- Compare v3 shadow decisions with current production routes.

**Exit:** telemetry is queryable, bounded, privacy-reviewed and cannot delay a
start/replan response.

### Phase 6 — Hardware and multi-repository validation

- Run original/remux/transcode fixtures locally and through proxy/transcode
  nodes.
- Verify restart reconstruction, session limits, transcode-disabled policy,
  alternate versions and output-route changes.
- Run Android Shield tests first on SDR/1080p, then the named 4K DV/AVR chain.
- Record plan, route events, decoder, TV range indicator and AVR format for the
  same attempt.
- Publish minimum server revision and enable v3 on validation deployments.

**Exit:** all Phase 0 fixtures pass on the deployed revision and Android no
longer reports `server_upgrade_required`.

## 10. Test matrix

At minimum, include:

### Contract and compatibility

- absent/2/3/unknown protocol versions;
- disabled and enabled feature negotiation;
- v3 nested capabilities are consumed, not silently dropped;
- legacy start response and HTTP status remain unchanged;
- old web/Apple transcode-start requests still work;
- malformed plan/terminal responses cannot be emitted.

### Direct and adaptation

- H.264/AAC MP4 direct;
- HEVC MKV original when the exact Media3 envelope supports it;
- container-only progressive and HLS remux;
- unsupported TrueHD/DTS layout with video copy and audio conversion;
- resolution/user-cap quality transcode;
- transcode disabled, user denied, capacity unavailable, missing tool and
  policy denied terminal results;
- alternate lower-resolution file with stable track remapping.

### HDR/DV

- HDR10, HDR10+, HLG and SDR direct gates;
- DV profiles 5, 7 MEL, 7 FEL, 7 unknown and relevant profile 8 variants;
- BL compatibility ID and range mismatch;
- native DV, validated HDR10 base-layer fallback and SDR tone map;
- missing `dovi_rpu`/`dovi_split`/dovi-tool behavior;
- no transformation claims when output was not validated.

### Audio and subtitles

- local decode versus passthrough by exact channels/layout;
- E-AC-3 JOC, TrueHD, DTS-HD/core and PCM fallback replan;
- text sidecar/embedded, ASS with/without authored fidelity, PGS, VobSub, DVB,
  conversion and burn-in;
- artifact timing after seeked remux/transcode;
- burn-in forces encode and reports HDR degradation.

### Recovery and load

- transport/decoder failures, track/quality/output invalidation;
- deterministic same-recipe loop rejection;
- idempotent duplicate and concurrent replans;
- API and transcode-node restart between plan and first manifest;
- session expiry, explicit stop and terminal cleanup;
- no double capacity accounting or orphan FFmpeg process;
- route-event flood/body/diagnostic limits.

## 11. Verification commands

Commands assume the repository root is the cwd.

```text
GOWORK=off go test ./internal/playback ./internal/api/handlers ./internal/transcodenode ./internal/nodepool
GOWORK=off go test -race ./internal/playback ./internal/api/handlers
make verify-local-paths
make lint
make build
```

Add a focused integration target that starts PostgreSQL/Redis, creates a media
fixture, calls v3 start/replan, fetches the returned stream or manifest, submits
route events, and verifies cleanup. Contract fixtures must also run in Android
CI against its Kotlin serializers and attempt-key function. Android tests also
prove detailed codec/layout capability emission and a local PCM-recovery route
event.

## 12. Rollout and rollback

1. Merge schema/types with v3 disabled.
2. Deploy shadow planner and compare decisions without changing playback.
3. Enable v3 only on a validation server with the Android test build.
4. Complete SDR, HDR, DV, passthrough, subtitle, restart and node fixtures.
5. Publish the minimum server revision.
6. Enable v3 for production servers after the named soak and telemetry gates.

Rollback is the dynamic `playback.protocol_v3_enabled` setting. Disabling it
stops advertising the feature and returns the no-session negotiation response
to new v3 starts. Existing v3 sessions remain playable until stop/expiry; do
not terminate active streams during rollback. Legacy web and Apple endpoints
remain available throughout.

## 13. Definition of done

- The three Android canonical endpoints and playback capability endpoint are
  implemented with profile/session ownership checks.
- Every playable response is complete, final and fetchable without client-owned
  recipe decisions.
- Every enabled delivery/transformation has a passing integration fixture.
- Direct decisions validate the selected track and full source/output facts.
- Android supplies the detailed per-codec and per-layout capability facts those
  direct decisions consume; missing facts fail conservatively.
- Terminal reasons replace unsafe direct fallbacks.
- Replans are idempotent, loop-safe, restart-aware and capacity-safe.
- Route telemetry is bounded, sanitized and queryable.
- Existing legacy server, web, Apple, Jellyfin compatibility and audiobook
  playback tests remain green.
- Android passes direct/remux/transcode/replan tests against the deployed server
  revision, followed by the documented 4K Dolby Vision/AVR validation.
