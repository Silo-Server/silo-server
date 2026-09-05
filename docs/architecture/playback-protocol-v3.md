# Playback protocol v3

The wire contract between a Silo client and a Silo server for deciding *how* a
piece of media will play, and for recovering when that decision turns out to be
wrong on the device in front of the user.

> **Status: normative.** This document, the JSON Schemas under
> [`docs/design/schemas/playback-v3/`](../design/schemas/playback-v3/), and the
> golden fixtures under `internal/playback/testdata/protocol_v3/` are the
> contract. Where this document and a client implementation disagree, the client
> is wrong. Where this document and the server implementation disagree, that is a
> bug in one of them — file it.

This is written to be sufficient on its own. A third-party client should be able
to implement playback against a Silo server from this document plus the schemas,
without reading any client repository and without reading the server source.

Paths are repository-relative; assume the repository root is the cwd.

---

## 1. Design invariants

Five properties hold everywhere in the protocol. Most of the surprising details
below follow from one of them.

**The server owns the decision.** A client reports what it can do; the server
decides what will be sent and how. Clients do not pick a bitrate ladder rung, do
not choose a container, do not decide whether to remux, and do not post a
transcode recipe. The `playback_plan` in a response is the whole instruction.

**Plan identity is deterministic.** `plan_id` is a pure function of the request
identity and the plan's own shape (§9). The same decision made twice produces
the same `plan_id`. This is what makes replans idempotent rather than merely
retried.

**Attempt keys are server-owned and opaque.** `plan_attempt_key` identifies "this
plan, on this output, with these local mutations" for loop prevention. It is a
hash the server computes. A client stores it, echoes it back, and never parses,
compares by substring, or recomputes it. Its algorithm and preimage are server
implementation details; §9 specifies only the stability clients may rely on.

**Claims are validated, not assumed.** When the server says a route preserves
Atmos, or that Dolby Vision metadata was removed, that claim was checked against
evidence the client supplied, at the strictness its evidence tier allows (§3).
The server never claims something it did not verify.

**Route events are diagnostics, not control.** A client reports what happened
(`first_frame`, `plan_failed`, `terminal`) so the server can learn; the report
never changes the session. Playback recovery goes through replan (§6), which is a
request with a response, not a fire-and-forget event. The server may *ask* for a
replan — the `plan_invalidated` realtime command, §6.1 — but even then the plan
only changes when the client comes back through the replan endpoint.

Two consequences worth stating early, because they surprise implementers:

- `POST /playback/start` returns **201 for every outcome**, including a terminal
  refusal to play. A terminal is a decision, not a transport failure. 4xx means
  the request was malformed; it never means "this media cannot play."
- `protocol_version: 3` is carried on every body independently, and each is
  validated separately: the start request, the `client_playback_context` nested
  inside it, the replan request, the route event, the decision response, and the
  plan within it. A start request whose envelope says 3 but whose
  `client_playback_context` says otherwise is rejected. There is no negotiation
  and no fallback — a server that does not speak v3 is not a server this
  contract describes.

---

## 2. Endpoints

All paths are relative to `/api/v1`. Every endpoint requires an authenticated
user. The mutation endpoints additionally require a profile, supplied as the
`X-Profile-Id` header.

Every non-2xx response body is the standard error envelope:

```json
{"error": "<machine_code>", "message": "<human sentence>"}
```

The `error` code is the stable part; the `message` is for logs and is not
contract.

### 2.1 `GET /playback/capability`

Feature detection. Auth required; no profile needed.

| Status | Meaning |
| --- | --- |
| `200` | Capability document |
| `401` `unauthorized` | No authenticated user |

v3 is the server's only playback protocol, so `enabled` is constant `true` and
the document is always the full one:

```json
{
  "enabled": true,
  "protocol_versions": [3],
  "features": ["playback_plan_v3", "neutral_playback_v3_contract_v1", "embedded_subtitles_v1", "layout_aware_passthrough", "playback_route_diagnostics",
               "device_quirks_v1", "seek_reanchor_v1", "output_change_v1", "output_display_evidence_v1", "direct_stream_resume_v1",
               "header_authenticated_media_v1", "authorized_media_origins_v1", "software_video_decode_v1",
               "plan_invalidated_v1", "plan_source_duration_v1"],
  "deliveries": ["original_http", "server_remux_progressive", "server_remux_hls", "server_transcode_hls"],
  "transformations": [{"name": "audio_to_aac", "executor": "server", "recipe_version": "2", "validated_claims": ["audio_decode"]}]
}
```

The fifteen feature strings above are the full set this server version advertises:

| Feature | What it promises |
| --- | --- |
| `playback_plan_v3` | The three plan endpoints exist and behave as specified here |
| `neutral_playback_v3_contract_v1` | The server mints opaque `plan_attempt_key` values that clients only echo, and exposes track/quality intent replans distinct from failure recovery |
| `embedded_subtitles_v1` | Exact native embedded subtitle selection on `original_http`, with a sidecar or burn-in fallback after selection failure (§8) |
| `layout_aware_passthrough` | Audio passthrough is decided from channel *layouts*, not just channel counts (§3) |
| `playback_route_diagnostics` | `POST /playback/route-events` is accepted |
| `device_quirks_v1` | Plans may carry `applied_quirks` and `runtime_corrections` (§9) |
| `seek_reanchor_v1` | The `seek_reanchor` replan operation is available (§6) |
| `output_change_v1` | The `output_change` intent replan is available; clients must keep the active route when this feature is absent |
| `output_display_evidence_v1` | The server honors `output.display` and its `hdr_evidence` tier; without it a client must still send `output.hdr_details` so the legacy fallback stays correct |
| `direct_stream_resume_v1` | A direct route may resume mid-file rather than restarting |
| `header_authenticated_media_v1` | An opted-in client receives media URLs without signed credentials in their query or path, and authenticates every media request with its normal Authorization header (§4.1) |
| `authorized_media_origins_v1` | Meaningful only with the token above: the client also honors credential-free absolute media URLs on server-designated proxy origins, which restores distributed egress for a header-authenticated attempt (§4.1) |
| `software_video_decode_v1` | Exact/platform-attested clients may qualify bounded `video_decode[]` entries with `hardware: false` for direct/original delivery; without the opt-in those evidence tiers remain hardware-only (§3) |
| `plan_invalidated_v1` | The client can be told mid-session that the plan it is playing was withdrawn, over the realtime `plan_invalidated` command, and replans off it. A session that did not negotiate it is stopped instead (§6.1) |
| `plan_source_duration_v1` | `source.duration_seconds` is populated when known, so its absence means *unknown* rather than *unsupported* (§5) |

That last one is the reason feature detection is a list and not a version
number: without it, a client cannot tell a server that never sends the runtime
apart from a server that knows this particular file's runtime is genuinely
unknown, and both look like an absent field.

`deliveries` reports the four *server-side* delivery values, not the three
delivery classes a client negotiates in. §4 gives the folding.

`transformations` advertises only what an eligible executor has validated. Most
entries come from the installed FFmpeg probe; pooled transcode nodes contribute
their own advertisements. `hdr_to_sdr_tonemap` additionally requires an
administrator-enabled hardware or software policy and a successful device-level
smoke probe for the applicable PQ, HLG, or SDR fallback-base source kind. A
client must not assume a
transformation exists because this document names it.

`enabled` survives from the rollout period and is now constant `true`; the
negative shape was deliberately removed before v1 lock because v3 is the only
playback protocol. `reason` remains an optional diagnostic for a future
non-rollout condition, but it never changes the meaning of `enabled`.

### 2.2 `POST /playback/start`

Requests a plan. Auth + `X-Profile-Id` required. Request body cap: **256 KiB**.
Body: `StartRequestV3`
([`start-request.schema.json`](../design/schemas/playback-v3/v3/start-request.schema.json)).

| Status | Code | Meaning |
| --- | --- | --- |
| `201` | — | A decision was made. Body is `DecisionResponseV3`: either `outcome: "playable"` with a `playback_plan`, or `outcome: "adaptation_unavailable"` with a `terminal`. |
| `400` | `bad_request` | Malformed JSON, failed validation (the `message` is the validator's own text), missing `X-Profile-Id`, or `profile_id` disagreeing with the header |
| `401` | `unauthorized` | No authenticated user |
| `404` | `not_found` | `file_id` does not exist, is marked missing, or this profile cannot see it |
| `409` | `playback_attempt_reused` | This `playback_attempt_id` was already used for a *different* request |
| `426` | `client_upgrade_required` | The body does not declare the finalized v3 shape: `protocol_version: 3` plus both capability evidence markers |
| `500` | `internal_error` | Session store failure |

A file the profile is not allowed to see is `404`, not `403` — parental and
library restrictions do not confirm that a hidden item exists.

The `426` is what a pre-v3 or draft-v3 client gets. There is no protocol negotiation and no
fallback: the server decodes v3 or it refuses, and the client is expected to
render an "update required" state rather than retry. It is deliberately not a
`400` — the request may be perfectly well-formed for the protocol it was written
against, and the distinction is what lets a client tell "I sent something wrong"
apart from "I am too old to talk to this server".

Note the layering: a request whose *body* is fine but whose *media* cannot be
played is not an HTTP error. It is a `201` with `outcome:
"adaptation_unavailable"` and a terminal reason from §7.3. HTTP statuses on this
endpoint describe the request; the decision lives in the body.

**Idempotency.** The server stores each `playback_attempt_id` alongside a
SHA-256 digest of the exact request body. Replaying a byte-identical body
replays the original response verbatim. Reusing the id with a different body is
`409 playback_attempt_reused` — the id is a claim about *which* playback attempt
this is, so reusing it for different intent is a client bug the server refuses to
paper over. If the attempt is known but its session has since expired, the
response is a `201` terminal with reason `session_expired` rather than a replay
of a plan that no longer exists.

Playable and terminal decisions are both durable attempts. A terminal start has
no `session_id` or plan identity, but its response and ownership are retained
under `playback_attempt_id` for the same TTL. This makes terminal retries obey
the same replay/conflict rules and gives terminal route events an addressable
authorization record.

A client generates a fresh `playback_attempt_id` per user-initiated playback and
reuses it only to retry a request whose response it did not receive.

**Omission is a request, not a default.** Two start fields mean "you decide" when
absent, and the server answers from stored user state rather than from a
constant:

| Field | Omitted | Present |
| --- | --- | --- |
| `start_position` | The profile's saved resume point for this item, or `0` when there is none, it is already complete, or the file is one part of a multipart item (every part shares the item's resume point, so a part-local seek to it would land somewhere arbitrary). It is required when `progress_persistence` is `client` | Exactly that position. `0` means *start over* |
| `audio_track_id` / `audio_track_index` | The profile's preferred audio track, resolved from the series preference, then the profile's audio-language setting, then the library override | Exactly that track |

`progress_persistence` separates the live session clock from durable resume
ownership. Omission (or `server`) means session progress may update the item's
resume/history normally. `client` keeps heartbeats, route diagnostics, and live
session state intact but suppresses those durable writes because the client
persists its own item-global timeline (for example through `/sync/progress`). A
client choosing that mode must send `start_position` explicitly, including
explicit `0`; the server never substitutes saved resume state for it.

Both are settled *before* planning, not after. This is not an implementation
detail a client can ignore: the plan's timeline is cut at the start position
(§5), and the audio track is part of the plan's identity (§9), so a route chosen
for position zero and then seeked is a different route than the one the server
would have chosen for the resume point. A client that resolves resume state
itself and sends the position explicitly gets identical behaviour — that is the
supported way to override the server's policy.

### 2.3 `POST /playback/{session_id}/replan`

Asks for a different plan for an existing session — after a failure, or because
the user changed a track or the quality. Auth + `X-Profile-Id` required. Request
body cap: 256 KiB. Body: `ReplanRequestV3`
([`replan-request.schema.json`](../design/schemas/playback-v3/v3/replan-request.schema.json)).

| Status | Code | Meaning |
| --- | --- | --- |
| `200` | — | `DecisionResponseV3`, plan or terminal |
| `400` | `bad_request` | Malformed or failed validation |
| `401` | `unauthorized` | No authenticated user, or no `X-Profile-Id` |
| `403` | `forbidden` | The session belongs to another user or profile |
| `404` | `playback_session_not_found` | No such session, or its session has ended |
| `409` | `stale_playback_plan` | `failed_plan_id` is not the session's current plan, `playback_attempt_id` is not the session's attempt, or a newer replacement is already active |
| `409` | `idempotency_key_reused` | This `replan_request_id` was used for a different replan |
| `409` | `replan_in_progress` | A replan for this session holds the lease right now |
| `503` | `replan_capacity_exhausted` | Server-wide concurrent replan limit (8) reached; retryable |
| `500` | `internal_error` | Store outage |

Note that a missing profile is `401` here, where start answers `400` — start
validates the body first and reports the header as one more field problem, while
replan treats identity as a precondition. Neither is retryable, so the
difference does not change client behaviour.

Checks run in this order: auth → body decode and validation → concurrency slot →
session lock → attempt lookup → ownership → attempt match → live session → lease.
A client that sees `503` therefore knows nothing was read or written for its
session, and can retry the identical request unchanged. Note that validation
comes before every session lookup: a malformed body against a session that does
not exist answers `400`, not `404`.

**Leases.** A replan takes a 15-second lease on the session. A second request
carrying the *same* `replan_request_id` while the lease is in flight gets `409
replan_in_progress`; once the original completes, the same id replays its
response verbatim. This is what makes a client's retry-on-timeout safe.

Note the deliberate asymmetry with start: a store outage during replan is `500`,
never `404`. Clients tear playback down on session-not-found, so a transient
store failure must read as retryable rather than as the session having vanished.

### 2.4 `POST /playback/route-events`

Reports what happened on the device. Auth + `X-Profile-Id` required. Request body
cap: **32 KiB**. Body: a single `RouteEventV3`, not a batch
([`route-event.schema.json`](../design/schemas/playback-v3/v3/route-event.schema.json)).

| Status | Code | Meaning |
| --- | --- | --- |
| `202` | — | Accepted. **No response body.** |
| `400` | `bad_request` | Malformed or failed validation |
| `401` | `unauthorized` | No authenticated user or no profile |
| `403` | `forbidden` | The session or attempt belongs to another profile, or the referenced session/attempt does not exist |
| `429` | `event_rate_limited` | 120 events/attempt/minute or 600/user/minute exceeded |
| `500` | `internal_error` | Store outage |

The checks run in that order: auth, then body decode and validation, then the
rate limit, and only then the session-ownership lookup. The
limiter sits in front of the ownership lookup deliberately — it exists to bound
store reads as much as writes, so it has to precede the read that would establish
ownership.

Two consequences for clients. A `429` means "drop this event," never "retry it";
the events are diagnostics and losing one costs nothing. And an unknown session
is `403`, not `404` — the handler does not distinguish "not yours" from "not
there." A store outage during that lookup is `500`, so a `403` genuinely means
the event will never be accepted and should be dropped rather than retried.

A terminal decision returned by `POST /playback/start` has a durable attempt but
no playback session or plan. The client reports it with `event: "terminal"`,
the start request's `playback_attempt_id`, and no `session_id`, `plan_id`,
`plan_attempt_id`, or `plan_attempt_key`. The server authorizes the event through
the persisted attempt ownership and returns `202`.

Event names are the eleven in §7.4. `diagnostics` is a string→string map, capped
at 32 entries, and the server keeps only the keys on its allowlist (§7.5),
truncating each value to 256 characters. Unknown keys are dropped silently; a
client sending them is not an error, it just achieves nothing.

---

## 3. Capability evidence tiers

The hardest problem in this protocol is that clients lie — not maliciously, but
because platform APIs vary in how much they actually know. Android can enumerate
`MediaCodecList` and answer "this exact decoder supports H.264 High@4.1 at 8-bit
up to 1920×1080@60". A browser can only answer `isTypeSupported("video/mp4;
codecs=avc1.640028")` → true. Apple can attest that VideoToolbox handles a codec
family but not enumerate levels.

So a client declares *how it knows*, per media type, and the server applies a
different strictness to each tier. `video_evidence` and `audio_evidence` are
required and are one of:

| Tier | Who reports it | What the server does with it |
| --- | --- | --- |
| `exact` | Android (`MediaCodecList`) | Full strict validation. The server walks `video_decode[]` and requires a hardware entry matching codec, profile, level, bit depth, and every `max_*` bound. Only this tier can earn a validated audio **passthrough** claim. |
| `platform_attested` | Apple (platform-backed Aether decoder stack) | Same walk. Profile and level are **skipped for hardware entries** because the platform cannot enumerate them; explicitly opted-in software entries enforce any profiles/levels the pinned stack supplies. All other bounds still apply. |
| `declared` | Web (`isTypeSupported`) | Flat list match only: `codecs_video` / `codecs_video_hardware` membership. No `video_decode[]` walk. |

Four rules follow from the table and are easy to get wrong:

**A flat claim without backing detail is a refusal, not a pass.** On `exact` and
`platform_attested`, if a codec appears in `codecs_video` but no `video_decode[]`
entry names that codec with `hardware: true` (or an explicitly opted-in bounded
software entry), the source is *not* eligible for a
direct route. The plan is downgraded and carries the decision reason
`evidence_insufficient_for_direct` plus the matching degradation warning, which
distinguishes "your evidence didn't support this" from "your device said no." A
client advertising a strict tier must populate `video_decode[]`; the flat lists
alone earn it nothing. On `declared` the flat lists are the whole mechanism, so
that tier never produces this signal.

Note the precise trigger: the signal fires only when *no* entry named the codec.
If an entry matched the codec but the source exceeded one of its bounds — a
4K file against a `max_height: 1080` decoder — that is a real device limit, and
the plan is downgraded with no evidence warning. The two cases mean different
things and a client should not conflate them in its telemetry.

Software decode remains explicit. At `exact` and `platform_attested`, a
`hardware: false` entry participates only when the request advertises
`software_video_decode_v1`; the same codec, bit-depth, dimension, frame-rate,
bitrate, and supplied profile/level bounds are then enforced. Existing clients therefore retain the
historical hardware-only behavior at these tiers.

Download creation reuses this bounded vocabulary additively inside `caps`:
`client_features`, `video_evidence`, and `video_decode` have the same meanings
and limits. Opting in requires a non-empty exact/platform-attested detailed
list; malformed partial opt-ins are rejected rather than falling back to flat
claims. This matters when hardware and software decoders have different
ceilings: the flat `max_resolution` remains a coarse device ceiling, while the
detailed entry decides whether a particular original file is safe. Apple keeps
the legacy coarse ceiling at 1080p so older servers fail safely; a detailed
hardware entry may independently preserve a 4K original on a new server.
Legacy flat-only download clients keep the previous resolver behavior. When a
file's probe metadata is too sparse for the detailed bounds walk to run at
all, the flat claims decide instead — including the coarse `max_resolution`
ceiling — so sparse metadata cannot widen eligibility beyond the flat
contract.

**An omitted bound means "unconstrained", not "unknown".** Within a
`video_decode[]` entry, an empty `profiles`, `levels`, or `bit_depths` list and a
zero `max_width` / `max_height` / `max_frame_rate` / `max_bitrate_kbps` are each
*skipped*, not failed. An entry that names a codec and nothing else therefore
claims that decoder handles every variant of it. That is a strong claim, and on
`exact` it is the client's job not to make it carelessly: the server will honour
it and hand back a direct route. Report what the platform actually enumerated.

The three list bounds are also not matched the same way, which matters when
populating them:

| Field | Match |
| --- | --- |
| `profiles` | Case-insensitive profile identity against the source profile. H.264 ignores presentation separators, and a decoder reporting Baseline also accepts the narrower Constrained Baseline source profile; the reverse is not inferred |
| `levels` | **At-least**: any listed level ≥ the source level passes |
| `bit_depths` | Exact integer equality |

So a decoder that tops out at H.264 level 4.1 may report `[41]` and still
validate a level-3.0 stream, while a decoder that handles 8- and 10-bit must
list both — `[10]` alone rejects an 8-bit source. Levels use the integer form
(4.1 → `41`).

**Every validated video route requires complete routing metadata.** Before any
tier logic runs, the server requires video codec, bit depth, width, height,
frame rate, and bitrate. Profile and level are decoder bounds instead: an
`exact` entry that supplies either bound cannot validate a source whose matching
probe value is absent or unknown, while an omitted bound keeps the explicit
“unconstrained” meaning above. This permits server adaptation of sources such as
VP9 whose probe reports an unknown level without allowing that sentinel to
satisfy a concrete client limit. A source missing the routing fields is
ineligible for any route and this case is *not* reported as
`evidence_insufficient_for_direct` — the client's evidence was never the
problem.

**Passthrough requires `exact` audio evidence.** A validated passthrough claim
(bitstreaming E-AC-3/TrueHD to a receiver) additionally requires the
`layout_aware_passthrough` feature in `client_features`, the codec listed in
`audio_passthrough.passthrough_codecs`, and a matching
`audio_passthrough.entries[]` whose `channel_counts` and `layouts` cover the
source. Only a client that can enumerate real sink layouts — the Android audio
HAL — can supply that. `platform_attested` and `declared` audio evidence still
qualify for ordinary decode/copy routes; they simply cannot earn
`claims.audio.passthrough = true`.

**Native HDR presentation is decided against the output, not the decoder.**
`output.hdr_details` (the display or receiver actually attached) takes
precedence over `client_capabilities.hdr_details` (what the device could do in
principle). A source whose dynamic range is recorded as `hdr_unknown` — legacy
rows that only stored a file-level HDR boolean — is treated as HDR10 when the
output supports HDR10, and the plan carries the `hdr_range_assumed_hdr10`
degradation warning. Refusing to play those outright would be worse than an
assumption the client is told about.

A client may additionally report the raw display probe in `output.display`
with an evidence tier:

```json
"output": {
  "hdr_details": { "hdr10": true, "dolby_vision_profiles": [] },
  "display": { "hdr_evidence": "exact", "hdr_types": { "hdr10": true }, "display_id": "0" }
}
```

`hdr_details` stays the native-output authority (decoder ∩ display) and keeps
its meaning on older servers. `display.hdr_evidence` is `exact` when the
platform answered (an empty `hdr_types` is then a confirmed SDR panel) or
`unknown` when it could not (no display, unsupported API, null capabilities,
probe failure). When `display` is present at all, the server never falls back
from a missing `output.hdr_details` to `client_capabilities.hdr_details`, and
`unknown` disables every native HDR and Dolby Vision output claim, and an exact
record narrows `hdr_details` to the ranges, HDR10 ceilings, and Dolby Vision
levels the panel actually carries (a contradiction is rejected at validation). Clients that have separated decoder
facts from output facts must send `display` so a decoder capability can never
be promoted to a native-output promise. A client that sends `display` must
always send `output.hdr_details` too, because a server without
`output_display_evidence_v1` ignores `display` and would otherwise fall back to
`client_capabilities.hdr_details`; the feature token tells the client whether
the evidence tier is being honored.

There are two delivery-scoped exceptions. An `original_http` capability
carrying the validated claim `client_managed_dynamic_range_v1` asserts that its
executor accepts the declared source range and resolves presentation against
the live output after receiving the original bytes. The planner may therefore
deliver HDR or Dolby Vision through that class even when the active sink does
not natively advertise the source range.

The narrower claim `client_dv8_base_layer_fallback_v1`, also `original_http`
only, asserts that the executor decodes a single-layer Dolby Vision Profile 8
stream through an ordinary HEVC decoder and presents its standards-compatible
base layer when the output lacks native Dolby Vision. The server keeps every
other gate: the source must be Profile 8 with no enhancement layer and a
compatibility id in the standard Profile 8 set (`1` is HDR10, `4` is HLG,
`2` is BT.709 SDR; `0`, `3`, `5`, `6`, and unknown ids fail closed), scan-proven
base-layer metadata (a DV configuration record, an explicit compatibility id,
and a present base layer, matching the tone-map path), the active
output must carry that base range, and the HEVC stream must fit the client's
decode bounds. The plan is then `validated_original_playback` bytes with
`decision_reason: client_dv8_base_layer`, `effective_recipe.dynamic_range` set
to the base range, `claims.video.dolby_vision: false` with
`dolby_vision_reason: base_layer_compatible_hevc`, and the
`dolby_vision_base_layer_only` degradation warning. A native Dolby Vision route
wins over the claim whenever the `original_http` capability can carry it;
when that delivery refuses the native plan (an HDR10-only executor on a
DV-capable output) the base-layer route is used instead. The executor reports
`dv8_base_layer_decoder_unavailable`, `dv8_base_layer_output_mismatch`, or
`dv8_base_layer_metadata_mismatch` as a typed failure when it cannot honor the
promise, and the plan's attempt key (which includes the effective range) keeps
the native and base-layer plans distinct in the replan ladder.

Neither exception applies to `progressive` or `hls`: those server-packaged
streams remain output-gated. The output snapshot is still retained for plan
identity, diagnostics, output-change replans, explicit Dolby Vision
transformation selection, and future server tone-map targeting.

The web client does not promote the generic high-dynamic-range media query to a
format claim, and it does not gate format claims on it either. Decoder capability
and active-output HDR are separate facts — browsers tone-map HDR content onto SDR
outputs, and Safari 26 reports `dynamic-range: standard` even on an XDR display —
so the media query survives only as the best-effort `hdr` output boolean. Format
claims come from exact shape probes matched to the bytes the remux delivers:
HDR10 requires Media Capabilities support for Silo's progressive 2160p HEVC
Main10, Rec. 2020, PQ, SMPTE ST 2086 shape, probed under exactly the `hvc1`
sample entry because the explicit v3 HDR10 strip remux labels its output `hvc1`
(legacy and automatic strip paths retain FFmpeg's default `hev1`). Dolby Vision
requires a definitive media-element answer for exactly `dvh1.05.06` or
`dvh1.08.06`, because the preserve remux tags its output `dvh1`. An answer only
for the other spelling (`hev1`/`dvhe`) is evidence for a file Silo never sends
and earns no claim. When native HLS is available, these media-element `dvh1` and
`hvc1` claims are scoped to `hls`, and `progressive` does not inherit them. When
native HLS is unavailable, they remain scoped to `progressive`; the hls.js MSE
path does not inherit evidence from a different playback engine.
`original_http` never receives normalized-remux evidence. An HDR10 claim can
carry `hdr10_max_width`, `hdr10_max_height`, `hdr10_max_frame_rate`, and
`hdr10_max_bitrate_kbps`; these ceilings keep a successful format probe from
admitting an untested stream class.

---

## 4. Deliveries

A delivery is *how bytes reach the player*. The server works in four values; the
client negotiates in three classes.

| Server `delivery` | Client class | What it is |
| --- | --- | --- |
| `original_http` | `original_http` | The source file, byte-for-byte, over HTTP with range support |
| `server_remux_progressive` | `progressive` | Repackaged into a new container, streamed as one chunked response |
| `server_remux_hls` | `hls` | Repackaged into HLS segments; codecs untouched |
| `server_transcode_hls` | `hls` | Re-encoded and segmented |

Because `original_http` carries the complete source file, a client may put
`client_selected_audio_track_v1` in that delivery's `validated_claims`. The
claim says it maps `selected_tracks.audio.index` onto its probed source
inventory, so selecting a non-default audio track does not by itself require
the server to remux the file. Without the claim, the historical default-track
gate remains. A claiming client that cannot honor the identity reports a typed
playback failure so the bounded replan ladder can choose a packaged route.

`client_playback_context.deliveries` is keyed by **class**, because a client's
answer to "can you play HLS" does not differ between a remux and a transcode —
the same player component handles both. The server folds its four values into
three when checking eligibility, and reports the specific one it chose in the
plan.

Each `deliveries` entry describes one class:

| Field | Meaning |
| --- | --- |
| `enabled` | The client is *willing* to use this class right now (user setting, network policy) |
| `supported_on_device` | The client is *able* to — the platform has a player for it at all |
| `failure_reason` | Optional free text explaining a `false` above; diagnostics only |
| `containers`, `video_codecs`, `audio_decode_codecs` | Flat lowercase name lists |
| `audio_passthrough_codecs` | Bitstream-out candidates; only ever honoured under the `exact` tier (§3) |
| `max_channels` | Optional ceiling applied to audio routing |
| `hdr_details` | Optional per-class HDR support, overriding the device-level value |
| `subtitles` | `sidecar_text`, `ass_styling`, `embedded_bitmap`, `sidecar_bitmap`, `font_attachments`, the legacy `embedded_text` hint, and optional `native_embedded` attestations (§8) |
| `features` | Class-scoped feature strings |
| `auth_header_refresh` | The client can re-fetch stream auth headers without restarting playback |
| `validated_claims` | Claims the client asserts it has verified for this class |
| `transformations` | Client-executed transformations offered for this class (§11) |

Both booleans must be true for the class to be eligible; they are separate
because "the user turned HLS off" and "this device has no HLS player" call for
different degradation warnings and different diagnostics. A class the client
omits entirely is unavailable — the server will not guess.

`client_managed_dynamic_range_v1` and `client_dv8_base_layer_fallback_v1` are
valid only as `validated_claims` entries on `original_http`. Neither is a
selectable transformation: the server supplies the source and the client
executor probes and routes it internally. If that
executor later reports a typed load failure, normal attempted-plan-key
exclusion applies. Until a server tone-map recipe exists, an exhausted HDR
original route terminates honestly rather than pretending an ordinary video
transcode can produce a supported result.

`stream.header_refresh` tells the client what to do when the stream URL's auth
expires: `none` means the URL is stable for the session, `session` means
re-request headers from `header_refresh_url` rather than restarting playback.

### 4.1 Header-authenticated media URLs

`header_authenticated_media_v1` is an engine-neutral client opt-in. A client
uses it only after the server advertises the same token, then includes it in the
top-level `client_features` on start and replan requests. It negotiates *how*
media URLs authenticate; `authorized_media_origins_v1` (below) separately
negotiates *which origins* may serve them.

With `header_authenticated_media_v1` alone, the server returns only relative
URLs on the authenticated API origin:

- direct and progressive remux: `/stream/{session_id}` (an ordinary `seek`
  parameter may still be present);
- remux/transcode HLS: `/playback/transcode/{session_id}/master.m3u8`, with
  relative, credential-free segment URLs in the manifest;
- subtitle artifacts, inventory sidecars and font bundles:
  `/stream/{session_id}/subtitles/...`.

None of those client-visible URLs contains the signed playback token (`st`) or
a token-bearing proxy path, and no proxy or transcode-node origin is returned.
A pooled transcode node may still execute HLS behind the API server; the API
relays its manifest and segments over the same authenticated client route.
Direct-play and progressive-remux proxy routes are bypassed because those
nodes accept a signed URL token rather than the user's API credential. The
shared node-routing resolver then applies the workload's execution and egress
policy. For example, `remux_execution=worker_only` makes an API-executed
progressive remux illegal, so the server plans the same recipe as
`server_remux_hls` when the client supports HLS; a pooled transcode node can
execute that recipe while the API remains the media origin. A progressive-only
client receives a non-retryable `local_transcode_disabled` terminal because
retrying cannot create a legal route.

In routing policy, **worker** means either a proxy node or a transcode node.
Progressive remux has proxy-worker, API, and transcode-node-to-proxy execution
shapes; HLS remux has transcode-worker and API execution shapes. Consequently,
`remux_execution=prefer_transcode` ranks the transcode-node shape first without
changing the delivery selected for the client. A progressive remux can run
FFmpeg on the transcode node and relay its single chunked response through the
selected proxy. The execution choice remains independent of egress—a proxy
origin can serve bytes produced by a transcode node without becoming the
executor itself.

Both halves of the transcode-node-to-proxy transport advertise their contract
through `/hw-capabilities`: transcode nodes publish
`progressive_remux_execution_v1`, and proxies publish
`progressive_remux_relay_v1`. That route requires both markers. A
proxy-executed progressive remux requires only the proxy's execution capability
and remains eligible when relay capability is absent. During a rolling upgrade,
an older node is therefore excluded from only the shapes it cannot serve before
a client receives a URL rather than failing the stream on its first request.
The transcode-executed shape also requires the shared node-recipe store. The API
writes the plan-scoped transport record before publishing the proxy URL; the
transcode node validates it before starting each progressive response, and stop
or force reload deletes it. This durable authority prevents a signed URL whose
token remains valid after a node replacement from resurrecting stopped FFmpeg
work. When the store is unavailable, route selection excludes only this shape.

**Authorized media origins.** A client that also sends
`authorized_media_origins_v1` promises something further: it will fetch media
from absolute URLs the plan returns on origins the server designates, attaching
the same `Authorization` header it sends the API. For such an attempt a plan may
return a proxy origin instead of a relative path:

- direct and progressive remux: `{proxy}/stream/v3/{session_id}` (again with an
  ordinary `seek` parameter when non-zero);
- remux/transcode HLS: `{proxy}/stream/v3/{session_id}/master.m3u8`, whose
  segment URIs stay relative and therefore resolve inside the same family.

Those URLs still carry no credential of any kind — no `st`, no token path
segment, no query parameter. The proxy is told what to serve out of band, and
authenticates the caller itself: it validates the same access token against the
same live login session the API checks, so revoking a session stops proxy
playback immediately, exactly as it stops API playback. A server with no proxy
pool, or one that cannot record the handoff, simply keeps the attempt on the API
origin — the URLs above are an addition a plan may make, never one a client may
assume. The escalation described just above therefore applies only when no proxy
origin is available to serve the remux.

Only the primary audio/video transport moves. Start, replan, route events,
progress and every other control-plane call stay on the API origin, and the
attempt's plan remains the sole authority for which URL to fetch. Subtitle
artifacts and font bundles are authenticated auxiliary resources: their
inventory URLs remain API-relative even when node routing assigns the primary
file, manifest, and segments to a proxy origin. Jellyfin-compatible subtitle
`DeliveryUrl` values follow the same API-origin rule.

The client must attach its current `Authorization: Bearer ...` header to the
manifest/file request and every derived request, including HLS segments,
subtitle artifacts and font bundles. `stream.headers` deliberately does not
echo the bearer token: plans are persisted for idempotent replay, while the
client already owns the current access credential. `header_refresh: none`
means the relative media URL itself is stable; normal API-token refresh remains
out of band, after which a client can retry or reload that URL with the new
header.

The signed playback token is what carries a reconstruction recipe across an
API restart. Opting it out therefore also opts out of transparent session
reconstruction: a missing in-memory session returns the normal expired/missing
response and the client starts a fresh attempt. Once selected, this mode is
sticky for the lifetime of the attempt; a client that can no longer honor it
must stop and start a new attempt rather than downgrade a replan to a
credential-bearing URL.

**Replica affinity.** Because there is no reconstruction recipe, a
header-authenticated attempt's session exists only in the memory of the API
process that started it. A media request routed to any other replica finds no
session and returns the expired/missing response, so a deployment serving
tokenless attempts currently needs either a single API replica or session
affinity on the media routes; legacy token-bearing attempts are unaffected,
since they reconstruct anywhere. Proxy-origin URLs
(`authorized_media_origins_v1`) are also unaffected: the proxy serves from the
shared grant store rather than from an API process's memory. Moving session
state into shared storage is the fix, and until it lands this constraint is
part of the deployment contract.

### 4.2 Media and subtitle URL query parameters

Every URL a plan publishes belongs to one of the route families below, and the
query parameters each family accepts are part of the contract. A client replays the
URL it was handed byte-for-byte; it never composes one, never drops a
parameter, and never carries a parameter across families.

| Route family | Routes | Query parameters |
| --- | --- | --- |
| Media | `/stream/{session_id}`, `/playback/transcode/{session_id}/master.m3u8` and its segments | `seek` only — the progressive-remux start offset in seconds, present only when it is non-zero |
| Media on a designated origin | `{proxy}/stream/v3/{session_id}`, `{proxy}/stream/v3/{session_id}/master.m3u8` and its `segment/{name}` children (§4.1) | `seek` only, with the same meaning; these routes never accept a credential parameter of any kind |
| Subtitle artifact | `/stream/{session_id}/subtitles/{combined_index}{.ext}`, `/stream/{session_id}/subtitles/{combined_index}/fonts` | `file_id`, always; one identity pin: `embedded_stream_index`, `external_subtitle_key`, or `downloaded_subtitle_id` (§8). VTT receivers may explicitly request `timestamp_offset` in seconds |

A media route never carries `file_id` or `downloaded_subtitle_id` — the session
already names the file it plays, and the media timeline is anchored by `seek`
plus the fields in §5. A subtitle route never carries `seek`: a sidecar is
fetched whole with absolute source timestamps and `subtitle.artifact.timing_origin_seconds: 0`. A receiver that cannot map its video clock may request a VTT-only `timestamp_offset` equal to the negative video timeline offset. The server shifts cues after reading the canonical artifact; this does not alter the extraction cache or the ordinary artifact contract. Shifted responses stream complete cues, omit cues that ended before time zero, clip overlapping cues at zero, and do not support byte ranges or conditional caching.

`file_id` is required on a subtitle route because a plan can fall back to an
alternate edition, so the session id alone does not fix which file's ordinal
space `{combined_index}` addresses. `embedded_stream_index` pins the probed FFmpeg stream index; `external_subtitle_key` pins an opaque SHA-256 hash of the sidecar path; `downloaded_subtitle_id` pins the downloaded row. These identities keep already-issued subtitle and font URLs attached to the same track when inventory order changes. Missing or ambiguous pinned tracks return an error rather than falling back to the path ordinal. Legacy unpinned URLs retain ordinal lookup.

An attempt that did not opt into `header_authenticated_media_v1` additionally
carries the signed stream token `st` on its media URLs — never on subtitle or
font-bundle routes. It is an opaque transport credential rather than a playback
parameter, and it is outside the table above.

---

## 5. The timeline model

The single most common client bug in v2 was assuming the player's zero and the
source's zero are the same instant. In v3 they usually are not, and the plan says
so explicitly.

`timeline` carries four numbers that a client must keep distinct:

- `source_start_seconds` — where in the *media* this plan begins.
- `stream_origin_seconds` — the source position that the *transport's* byte-zero
  corresponds to.
- `player_start_seconds` — where the client should seek the player after load.
- `timeline_offset_seconds` — what to add to a player position to get a source
  position.

Three shapes exist:

**Direct and transcoded routes** hand the player a complete timeline.
`stream_origin` and `timeline_offset` are `0`, `player_start` is the requested
position, `can_seek_anywhere` is true when the runtime is known, and
`seek_restoration` is `player_position` — the client seeks locally.

**Copy remux over HLS** is served from FFmpeg's live, still-growing playlist,
which starts at the preceding keyframe selected by FFmpeg's input seek.
For HEVC HDR copy packaging, the frozen plan also controls the sample entry: a
preserved Dolby Vision Profile 5 or 8 plan emits `dvh1` with FFmpeg's unofficial
strictness relaxation so the DOVI configuration record is retained, while a
validated Dolby Vision-to-HDR10 plan emits `hvc1`.
`stream_origin` and `timeline_offset` both equal that resolved source position,
while `player_start` is the requested position minus the resolved origin so the
client advances past copied pre-roll. `seek_window_start_seconds` is the resolved
origin, and **`seek_window_end_seconds` is deliberately absent**. An open end
marks the window as incomplete; combined with `can_seek_anywhere: false` it
routes every seek back through the server as a reanchor (§6), which is correct
because the playlist has no bytes for positions FFmpeg has not reached yet.
`seek_restoration` is `source_position`.

**Progressive remux** is a freshly generated chunked response with no byte-range
support, so it uses the same resolved keyframe origin and player pre-roll offset;
the window is open-ended and seeks go through the server.

### `source.duration_seconds`

The media's full runtime, and nothing else. Specifically:

- It is **not** `total − source_start`. It does not shrink because the plan
  starts mid-file.
- It is **not** adjusted by `timeline_offset_seconds`.
- It is **omitted** rather than sent as `null` when the server does not know it,
  because a client coercing `null` to a numeric default would read it as zero.
- A client **must not** substitute the playback engine's reported duration for
  it. On an HLS copy remux the engine reports the length produced so far, not the
  runtime — using it makes the scrubber grow while the user watches.

---

## 6. Replan

Replan is the only way a plan changes. It covers both "that didn't work" and
"the user asked for something else," and the distinction matters to the server.

`operation` is one of six:

| Operation | Meaning | Requires |
| --- | --- | --- |
| `failure_recovery` | The plan failed on the device | `failure.classification` |
| `seek_failure_recovery` | A seek failed | `failure.classification` |
| `seek_reanchor` | Move a server-anchored timeline to a new position | — |
| `track_change` | The user picked a different audio or subtitle track | — |
| `quality_change` | The user picked a rung from `available_qualities` | non-empty `quality_preference` |
| `output_change` | The active display/output capabilities changed | — |

The asymmetry is intentional. A seek reanchor is a timeline operation, not a
failed recipe — a classification is still accepted from older callers but never
selects seek semantics. A track or quality change is not a failure at all, so
demanding a classification would force clients to invent one. But
`quality_change` *must* name the rung it wants: an empty `quality_preference`
normalizes to `auto`, which is a different user intent than the menu selection
the operation models, so the server rejects it rather than silently doing
something else.

**Intent operations behave differently from failure recovery.**
`track_change`, `quality_change`, and `output_change` keep the previous route
eligible because nothing established that its recipe failed: neither attempted-
key history nor the failed-plan exclusion applies. The first two replace what
were separate v2 endpoints (an audio PATCH and a client-posted transcode start).
A client may therefore be handed back a plan it has already tried — that is
correct here, and a client must not treat a repeated `plan_attempt_key` as a
loop.

When such an operation actually changes something — the request's tracks,
quality, or output capabilities differ from what the session currently has —
the server also tries to return to the *requested* edition rather than staying
on whatever alternate version a previous fallback landed on, since the newly
requested intent may fit the original file again. That is a preference, not a
guarantee: if the requested edition no longer resolves or fails its preflight,
the healthy active alternate is kept. Track identities are remapped only when
the edition really changes, because remapping within one file would degrade an
exact selection to a best-match lookup and could silently move a listener off a
commentary track.

Omitting `quality_preference` on any replan preserves the session's current
preference; sending it replaces that preference. A track change therefore does
not silently reset `original` or a pinned rung, and failure recovery does not
discard the viewer's requested quality unless the client explicitly asks it to.
Clients should still send the current preference when they know it, so their
intent remains explicit in diagnostics.

For failure recovery, `attempted_plan_keys` is the loop guard. The client sends
back every `plan_attempt_key` it has already tried for this attempt (up to 16);
the server will not hand back a plan whose key is in that list. `attempt_count`
(1–8) bounds the whole recovery chain. Together they mean a device that fails
every route reaches a terminal instead of cycling forever.

Failure, seek, and quality replans may omit unchanged track identities. The
server overlays only identities present in those requests and preserves the
durable selected subtitle otherwise. Only `operation: "track_change"` gives an
omitted `selected_tracks.subtitle` the explicit meaning "subtitles off". A
fallback to another media version must remap the selected subtitle; if no
equivalent exists, it returns terminal reason `subtitle_unavailable_in_version`
instead of silently continuing with subtitles off.

`local_mutations` (up to 8 entries, 64 chars each) reports client-side
adjustments — a transport reopen, a PCM decode fallback — that change the
effective route without changing the plan. They feed the attempt key, so a plan
retried after a local mutation is a *different* attempt and is not blocked by the
loop guard.

A seek-scoped recovery refuses to accept new capability or device evidence: a
seek is not an authority boundary for replacing the client's declared abilities
mid-session.

**Attempt-sticky features.** `client_features` is otherwise refreshed by any
replan that sends it, but three entries are fixed by the start negotiation and a
replan can neither add nor drop them:

| Feature | Why it is fixed |
| --- | --- |
| `header_authenticated_media_v1` | It selects the media security contract. A signed URL from an earlier plan stays usable until its recipe expires, so a mid-attempt switch would leave two contracts alive for one session (§4.1) |
| `authorized_media_origins_v1` | It selects which origins may serve the attempt's media. A plan that already handed out a proxy origin outlives the replan that would revoke it, so the client would be left holding a URL it no longer trusts (§4.1) |
| `software_video_decode_v1` | It widens the direct-play evidence tiers. Dropping it converts a direct route into a transcode and persists that downgrade into the durable request |

The server silently restores the negotiated state of each, whatever the replan
sends — including an explicit list that omits one, which is otherwise a valid
way to drop a feature. Seek replans never replace the feature list at all.
Changing any of these modes means stopping and starting a new attempt.

### 6.1 `plan_invalidated_v1` — the server withdraws a plan

Every other control message in this protocol travels client → server. This one
does not: `plan_invalidated` is the only **server-initiated control push**, and
it exists because the server can learn a route is wrong *after* the plan is
already playing.

The concrete case is H.264 stream-copy safety. Some encoders redefine the same
`pic_parameter_set_id` in-band with conflicting content, which cannot be copied
into an avc1/fMP4 segment (§4). Detecting it means reading the opening seconds
of the source, which on remote storage costs seconds — so the server no longer
waits for it. An unresolved verdict plans **optimistically** (a remux is
allowed), the scan runs behind the issued plan, and if it comes back unsafe the
plan that was handed out has to be taken back.

The push is a realtime **command** on the session control socket
(`GET /playback/sessions/{session_id}/control/ws`) — acked and answered like
any other, not a fire-and-forget event:

```json
{
  "type": "command",
  "command_id": "…",
  "session_id": "…",
  "name": "plan_invalidated",
  "reason": "video_copy_unsafe",
  "deadline_ms": 8000,
  "payload": {"reason": "video_copy_unsafe", "plan_id": "<the invalidated plan>"}
}
```

`payload.plan_id` names the plan being withdrawn, which is not necessarily the
one on screen: a client that has already replanned past it has nothing to do and
completes the command as a no-op. That is why the field is required — acting
without checking it would evict a route the server never complained about.

A client that advertises `plan_invalidated_v1` in `client_features` promises to:

1. send `{"type":"ack","status":"accepted"}` immediately,
2. run its ordinary recovery replan — `operation: "failure_recovery"`, with the
   invalidated plan's `plan_attempt_key` in `attempted_plan_keys` so the copy
   route is excluded deterministically (the now-persisted verdict excludes it
   too), and
3. send `{"type":"result","status":"completed"}` when the replan is done.

**Everything else is a session stop.** The server pushes the command only to a
session that negotiated the feature *and* holds a live realtime connection. No
feature, no connection, no `completed` result within `deadline_ms` (an ack
alone does not stop the clock), or a `rejected` result, and the session is
terminated instead. That is deliberate, and it is the whole
backwards-compatibility story: a client shipped before this token sees its
session end, runs the recovery it already has, and its fresh attempt is planned
against the persisted verdict — which lands it on a transcode. No client has to
implement anything to stay correct; the feature only buys a seamless switch
instead of a stopped session.

An inconclusive scan changes nothing: nothing is persisted, no command is
pushed, and live sessions keep the route they were given. Only a positive
"this source cannot be copied" verdict withdraws a plan.

Three scoping rules keep the stop from firing where it cannot help:

- **The verdict is about the effective file.** A session whose *requested*
  edition turned out to be copy-unsafe, but which is streaming a different
  edition after the 4K guard or a version replan, is left alone: the bytes it is
  serving are copy-safe.
- **A session still being established is given time.** A session exists in the
  session manager before its attempt record is written and long before its
  client can open a realtime channel. A verdict landing inside that window would
  see a session it cannot tell and stop one that is mid-start, so a session that
  would otherwise be stopped waits out `CopySafetySessionSettleWindow` and is
  examined once more; by then it is normally reachable and gets the command.
- **Jellyfin-compatibility sessions are exempt.** That surface decides direct
  stream from the device profile and the catalog version, never from the
  copy-safety verdict, so a stopped compat client reconnects onto the identical
  remux. The stop is only correct for clients whose recovery re-decides the
  route, which for a Silo client means planning against the persisted verdict.

That persisted verdict is read from the `media_files` row on every path that
plans a route — start, replan, and the v2 resolver — not only from the probe
ensurer's in-memory stamp. A replan that did not see it would simply walk from
one stream-copy delivery to the other.

Delivery is in-process: the replica that owns the session owns its realtime
connection, so a verdict resolved on one node acts on the sessions that node is
serving.

The row is what covers the gap that leaves. A signed stream URL is a durable
capability the client replays on whichever replica answers next, and a replica
that dies between persisting a verdict and pushing the invalidation takes the
only notifier that knew about it with it. The replacement replica has no live
session, so it rebuilds one from the recipe card — which would replay the exact
remux the verdict condemned. The serve routes therefore re-read the persisted
verdict for a video stream-copy recipe (a progressive remux, or an HLS transport
whose video target is `copy`) and refuse with the ordinary playback-session
not-found when the row says the source is unsafe. The client's existing recovery
mints a fresh attempt, which plans against the same row and lands on a
transcode. Transcode reconstruction is untouched: re-encoding the bitstream is
unaffected by conflicting parameter sets.

On the HLS routes the check runs *before* the session is rebuilt, not before the
transport is. Rebuilding registers the playback session against the user's
stream caps, so a later refusal would leave a session nobody serves holding a
slot the replacement attempt needs; and an HLS recipe pinned to a transcode node
is revived by proxying to that node, a path that never reaches a local transport
rebuild at all. The progressive route decides after the load, because the same
file lookup serves its other preflight checks, and tears the reconstructed
session back down when it refuses.

The row alone is not the whole answer, in two directions.

A verdict can be **known but unwritten**: the scan reached it and the
`media_files` write failed, so it lives only in the memo of the process that
reached it, and the row cannot tell that apart from "never scanned". The revival
gate therefore asks the row first and the local scanner second, and the scanner
retries the failed write — without ffmpeg — while it answers.

A verdict can be **not yet reached at all**, which is the ordinary optimistic
case and is allowed. But the gate runs once, at the revival request, while a
progressive remux is a single response that runs for the length of the title:
nothing later re-examines it, and the replica that is racing for the verdict can
only reach its own sessions. So a revival the verdict does not condemn
*re-engages the race on the reviving replica*, which makes the session it just
built the property of a race running here. That pass costs no ffmpeg when the
answer is already known locally or on the row; it re-runs the notification for
the sessions this replica now holds.

Two smaller rules keep that machinery honest. A race request that arrives while
a scan for the same file is running is folded into one follow-up pass rather
than dropped, because the sessions a pass acts on are the ones that exist when
it runs and a replan can commit a replacement stream-copy mid-scan. And the
verdict write is conditional on the row still holding the size and mtime that
were scanned: a file rewritten in place while the scan read it produces a
verdict about bytes nobody is serving, which is neither persisted nor pushed at
any session.

---

## 7. Registries

### 7.1 Decision reasons

Why the server picked this route. Informational; clients may log or display but
must not branch on an unrecognized value.

`validated_original_playback`, `container_normalization`, `audio_adaptation`,
`hls_audio_adaptation`, `hls_packaging_required`, `subtitle_burn_in_required`,
`client_dv7_to_dv81`, `client_dv7_to_hdr10`, `client_managed_dynamic_range`,
`client_dv8_base_layer`, `evidence_insufficient_for_direct`,
and the quality reasons `quality_original`, `quality_auto_source`,
`quality_fixed_rung`, `quality_device_limit`, `quality_bandwidth_limit`,
`quality_metered_limit`, `quality_bandwidth_cap`.

### 7.2 Degradation warnings

The plan will play, but something the user might notice was given up.

| Code | Meaning |
| --- | --- |
| `hdr_range_assumed_hdr10` | Source range unknown; treated as HDR10 |
| `dolby_vision_removed` | DV metadata stripped |
| `dolby_vision_strip_unsupported_by_source` | DV could not be stripped |
| `dolby_vision_enhancement_layer_discarded` | FEL/MEL dropped, base layer kept |
| `dolby_vision_base_layer_only` | Profile 8 played unchanged through an HEVC decoder as its HDR10/HLG/SDR base layer; DV metadata not presented |
| `hdr_tone_mapped` | HDR video converted to limited-range BT.709 SDR |
| `audio_converted` | Audio re-encoded rather than copied |
| `subtitle_burn_in` | Subtitles rendered into the video |
| `quality_reduction_unavailable` | Requested rung could not be produced |
| `quality_preference_normalized` | Unknown `quality_preference` normalized to `auto` |
| `bandwidth_cap_applied` | `bandwidth_cap_kbps` limited the selection |
| `evidence_insufficient_for_direct` | Evidence tier blocked a direct route |

### 7.3 Terminal reasons

Playback will not proceed. `terminal.retryable` says whether trying again could
help. Delivered inside a `201` (start) or `200` (replan), never a 4xx.

*Planner:* `adaptation_exhausted`, `adaptation_unavailable`,
`client_hls_unsupported`, `conversion_tool_unavailable`,
`hdr_transcode_unsupported`, `no_alternate_version`,
`source_metadata_incomplete`, `source_unavailable`,
`audio_conversion_unsupported`, `video_conversion_unsupported`,
`dv_conversion_unsupported`, `transcoding_disabled`,
`subtitle_conversion_unsupported`. When a video adaptation is forced solely by a
subtitle burn-in requirement and cannot execute, the terminal is
`subtitle_conversion_unsupported` naming the subtitle rather than the underlying
HDR, 4K, or transcode-policy reason — deselecting the subtitle restores playback.

*Subtitle policy:* `subtitle_burn_in_source_unsupported`,
`subtitle_codec_unsupported`, `subtitle_track_invalid`,
`subtitle_track_unavailable`, `subtitle_unavailable_in_version`.

*Transport and session:* `internal_error`, `session_expired`,
`subtitle_artifact_unavailable`, `capacity_unavailable`,
`local_transcode_disabled`,
`audio_transcoding_disabled`,
`transcode_start_failed`, `transcode_node_unavailable`,
`transcode_node_capability_unavailable`, `track_unavailable`,
`invalid_seek_position`, `invalid_replan`, `seek_reanchor_route_changed`,
`seek_reanchor_recipe_unavailable`,
`seek_reanchor_intent_mismatch`, `seek_failure_recovery_intent_mismatch`,
`policy_denied`, `routing_policy_unsatisfied`, `route_capacity_unavailable`.
The last two come from the node-routing resolver:
`routing_policy_unsatisfied` means no route shape is legal under the configured
execution and egress policy and is never retryable, while
`route_capacity_unavailable` means a legal shape exists but no node could serve
it right now and is always retryable.

### 7.4 Route event names

`plan_selected`, `plan_invalidated`, `plan_failed`, `first_frame`, `terminal`,
`stopped`, `runtime_correction_applied`, `runtime_correction_succeeded`,
`runtime_correction_failed`, `seek_reanchor_requested`, `seek_reanchored`.

### 7.5 Diagnostics allowlist

Route-event `diagnostics` keys the server retains. Everything else is dropped;
every value is truncated to 256 characters.

`decoder_name`, `decoder_init_ms`, `first_frame_ms`, `device_model`,
`requested_quality`, `effective_quality`, `pcm_recovery`, `retry_outcome`,
`replan_request_id`, `video_mime`, `video_codecs`, `video_width`, `video_height`,
`color_transfer`, `color_range`, `error_code`, `error_code_name`, `error_cause`,
`transformation_name`, `transformation_version`, `transformation_stage`,
`input_dv_profile`, `output_dv_profile`, `rpu_converted_count`,
`rpu_failed_count`, `el_nal_dropped_count`, `sample_count`,
`transform_buffer_peak_bytes`, `requested_media_file_id`,
`effective_media_file_id`, `audio_output_mode`, `audio_mime`, `audio_channels`,
`audio_decoder_name`, `correction_id`, `correction_stage`, `network_transport`,
`network_metered`, `network_validated`, `bandwidth_estimate_kbps`,
`link_downstream_kbps`, `target_source_position_seconds`, `reason`.

---

## 8. Track identity and the subtitle ordinal space

Every track is addressed as `file:{media_file_id}:{kind}:{ordinal}` — for
example `file:42:audio:0`. When a client sends both an id and an index they must
agree; the server rejects a disagreeing pair rather than picking one.

Subtitles occupy a single **combined ordinal space** spanning three sources, in
three dense consecutive ranges:

1. **External** sidecar files, in catalog order — ordinals `0 … E-1`
2. **Embedded** container streams, in container stream order — `E … E+M-1`
3. **Downloaded** subtitles, in `created_at` order — `E+M … E+M+D-1`

The space is dense and gap-free, and this is load-bearing. A track that has no
sidecar representation the stream handler can serve — a DVD or DVB bitmap stream
— **keeps its ordinal** and is published with `delivery: "burn_in_only"` and no
`url`. Omitting it would leave a hole, and any client deriving the
downloaded-track base by counting published URLs would then undercount and
address the wrong track. (That was a real bug; this rule is the fix.)

`playback_plan.subtitle.inventory` is the authoritative list. A client selects a
track by echoing an entry's `track_id` or `combined_index`. It must never derive
an ordinal by counting tracks, summing array lengths, or taking `max(index)+1`.

Each entry carries `source` (`external` | `embedded` | `downloaded`), `delivery`
(`sidecar` | `burn_in_only`), the `forced` / `default` / `hearing_impaired`
flags, a `url` when deliverable, and a `font_bundle_url` for embedded ASS tracks
with attachments. `default` reflects the source container's own default flag, so
only embedded and external tracks can carry it — a downloaded subtitle is never
`default`. `url` is present only on `sidecar` tracks, and only once a session
exists to scope it to — but it does not depend on the current selection: a start
or replan that resolves to `subtitle.mode: "off"` still publishes every sidecar
entry with its fetchable `url`, so a client can build its full subtitle menu
without first asking for a plan it does not want.

`subtitle.mode` is `off`, `render` (client renders the selected embedded stream or sidecar), `convert` (server
transcodes it to a client-renderable format first — always to WebVTT, served as
`text/vtt` at a `.vtt` URL), or `burn_in` (rendered into the video, which forces
a transcode).

`subtitle.artifact` describes the selected sidecar. A `render` decision carries either `artifact` or `embedded`, never both; `convert` carries an artifact. Under `off` and `burn_in` both are absent, and every plan states this afresh: an artifact is never carried over
from an earlier plan of the same session, so a client must take the current
plan's `subtitle` block literally rather than remembering the previous one.
`off` also carries no `subtitle.track_id`. The inventory `url`s are unaffected
— they describe what is fetchable, not what is selected, and stay published in
every mode.

Native embedded selection requires `embedded_subtitles_v1` in `client_features` and an exact capability in `client_playback_context.deliveries.original_http.subtitles.native_embedded`:

```json
{
  "container": "mp4",
  "codecs": ["mov_text"],
  "track_identity": "container_track_id",
  "ass_styling": false,
  "font_attachments": false
}
```

`track_identity` is either `ffmpeg_stream_index` (the absolute probed AVStream index) or `container_track_id` (the canonical positive decimal container track ID, when available from probing). Neither is a combined subtitle ordinal. Missing or ambiguous identity metadata disqualifies the native route. Container and codec support must match; authored ASS preservation additionally requires styling and font support. The old `embedded_text` flag alone never authorizes native selection. Text sidecar rendering depends on `sidecar_text`, regardless of the source being embedded or external.

The native decision is `subtitle: {"mode":"render", "track_id":"file:42:subtitle:0", "embedded":{"stream_index":3,"container_track_id":"4"}, "inventory":[...]}`. The client selects that exact stream from the original media and does not mount the inventory's fallback URL. Native selection applies only to `original_http`; remux and transcode plans use sidecars or burn-in. Inventory `delivery` continues to describe the available server representation, so even a `burn_in_only` entry can be selected natively when the client attests the exact bitmap codec.

A confirmed native selection failure triggers `failure_recovery` with `failure.classification: "subtitle_embedded_failed"`. The server disables native selection for the rest of that playback attempt, including later capability refreshes, and replans with an executable fallback. The native identity participates in both plan identifiers, so the same video route with extracted subtitles is a distinct attempt. Seek reanchors preserve the native identity and reject source drift.

Subtitle artifact, inventory and font-bundle URLs are session-scoped and carry
their own query parameters; see §4.2 for the per-route-family contract.

The sidecar URL suffix is part of the representation contract, not decoration.
The artifact `format` and `mime_type` describe served bytes, independently of the source codec. SRT, SubRip, and mov_text sources served as VTT therefore report `format: "vtt"` and `mime_type: "text/vtt"`. Artifact timestamps are absolute original-media time, with `timing_origin_seconds: 0` even when the video transport resumes from a nonzero source position.

An embedded `hdmv_pgs_subtitle`/PGS sidecar is lossless binary PGS at a `.sup`
URL with `application/octet-stream`; cached full-track responses support `HEAD`
and byte ranges. Text conversion is always WebVTT at `.vtt`, while lossless
ASS/SSA uses `.ass`. A suffix that does not match the selected track or a valid
conversion is rejected with `415` rather than returning bytes of a different
type under the requested extension.

Embedded text URLs return the complete track from source time zero by default,
including when playback starts at a resume position. Consumers that maintain a
sliding window may explicitly supply `position` (nonnegative source seconds)
and `duration` (positive seconds, at most 3600). They must request subsequent
windows themselves; HTTP EOF ends only the requested window. ASS remains a
complete script. PGS windows require `windowed=1` in addition to the window
parameters. External and downloaded sidecars are always returned whole.

Complete embedded text and PGS extracts are cached by source file identity,
modification time, size, subtitle ordinal, and output format. Partial or failed
extracts are never published. Text cache misses stream while extracting; repeated
complete text requests reuse the finished artifact. A failed extraction returns
an error response before output begins, or aborts an already-started stream so
clients can distinguish failure from a complete track and retry.

---

## 9. Plan identity

The server mints both identifiers. **Clients treat both as opaque, case-sensitive
tokens and never implement either identity algorithm.** Their wire prefixes and
lengths are validation syntax, not a derivation recipe.

`plan_id` identifies the server's playback decision. It is stable when the same
attempt produces the same source, delivery, recipe, tracks, subtitle mode and native identity,
transformations, applied quirks, and recipe revision. A change to any of those
inputs produces a different identity.

`plan_attempt_key` is the replan loop guard for a plan as attempted on one output
route with a set of client-reported local mutations. The server canonicalizes
order-insensitive inputs internally. The client:

1. stores the exact key from `playback_plan.plan_attempt_key`;
2. echoes it unchanged as `plan_attempt_key` when reporting that plan;
3. adds the unchanged token to `attempted_plan_keys` after the plan fails; and
4. never case-folds, parses, truncates, hashes, or synthesizes a replacement.

`internal/playback/testdata/protocol_v3/attempt_keys.json` contains opaque
cross-message vectors: a server-emitted token, the exact replan echo, and the
loop-rejection result. The generator computes the server token internally but
does not publish its preimage.

---

## 10. Quality

`playback_plan.available_qualities` is the menu. The client renders it and, on
selection, sends a `quality_change` replan with the entry's `label`. It does not
compute rungs.

The source rung is always present, labelled `original`, with
`preserves_source: true`. Transcode rungs are added below the source resolution
class, plus at the same class when they reduce bitrate, and only when HLS is
available to the client, transcoding is enabled, and 4K transcoding is permitted
for a 4K-or-higher source. A source falls under that policy when its catalog
resolution label reads `2160p`, `4k`, `uhd`, `4320p`, or `8k` (case- and
whitespace-insensitive), its probed width is at least 3840, or its probed height
is at least 2160. HDR plans additionally require
at least one enabled tone-map policy. A source-preserving HDR plan advertises
those lower rungs without probing an executor; selecting one performs the lazy
capability validation during the quality-change replan. The published ladder
uses compound labels so each menu selection pins both a resolution class and a
bitrate:

When a planner terminal permits media-version fallback, start and replan order
same-edition candidates with non-4K versions first, then continue through the
remaining candidates until one produces a plan. A refused lower-resolution
candidate therefore does not hide a later 4K version that can direct-play or
remux without video encoding.

| label | display_name | height | kbps |
| --- | --- | --- | --- |
| `2160p-high` | 4K High | 2160 | 40000 |
| `2160p-medium` | 4K Medium | 2160 | 20000 |
| `2160p-low` | 4K Low | 2160 | 10000 |
| `1080p-high` | 1080p High | 1080 | 10000 |
| `1080p-medium` | 1080p Medium | 1080 | 6000 |
| `1080p-low` | 1080p Low | 1080 | 3000 |
| `720p-high` | 720p High | 720 | 4000 |
| `720p-medium` | 720p Medium | 720 | 2000 |
| `720p-low` | 720p Low | 720 | 1500 |
| `480p` | 480p | 480 | 1500 |

A rung below the source resolution class is always useful. At the source's own
class, a rung is published only when it undercuts the source bitrate; a 25.2
Mbps 4K file therefore offers 4K Medium and 4K Low but not a pointless 40 Mbps
4K High encode. Resolution classification also considers width, so cinema-crop
UHD sources such as 3840x1540 retain their native dimensions on a 4K bitrate
step instead of being upscaled to 2160 lines.

Compound rungs are strict resolution/bitrate selections. A bandwidth cap can
clamp their bitrate but does not silently demote their resolution. Plain labels
remain accepted for stored/default preferences and retain their existing
height-only behavior.

Registry availability is deliberately *not* consulted when building the menu: a
capability check there could trigger lazy node fetches that a source-preserving
start must never pay for. A rung whose toolchain turns out to be missing degrades
to a retryable terminal at replan time instead.

Audio-only sources publish a single `original` rung — quality rungs are a video
concept.

`quality_preference` accepts `auto`, `original` (aliases `source`, `max`), the
plain `2160p` / `1080p` / `720p` / `480p` values with their obvious aliases
(`4k`, `uhd`, `fhd`, `hd`, `sd`), and the compound labels in the table above.
An unrecognized value normalizes to `auto` and the response carries the
`quality_preference_normalized` warning rather than an error.

---

## 11. Transformations

A transformation is a named, versioned media operation with claims attached.

| Name | Executor | Recipe version | Promises | Claims |
| --- | --- | --- | --- | --- |
| `audio_to_aac` | `server` | `2` | — | `audio_decode` |
| `video_to_h264` | `server` | `2` | `sdr` output | `h264_decode` |
| `hdr_to_sdr_tonemap` | `server` | `1` | limited-range BT.709 `sdr` output with HDR metadata removed | `hdr_metadata_removed`, `sdr_bt709_output` |
| `server_dv7_to_hdr10` | `server` | `1` | `hdr10` output | `dolby_vision_metadata_removed`, `hdr10_base_layer_preserved`, `enhancement_layer_discarded` |

`audio_to_aac` recipe version 2 treats the selected source channel count as a
byte-affecting input. When a source with more than two channels is encoded to
stereo, FFmpeg first rematrixes it to stereo, then applies up to 6 dB of input
gain through a limiter with a -2 dBFS sample ceiling. Mono output, surround
output, stereo sources, and copied audio do not use the boost.

Recipe 2 is fenced at every byte-producing boundary during a rolling upgrade.
Token-based proxy remuxes use `/stream/remux/audio-v2/{token}`; Jellyfin-compatible
progressive and HLS media use the corresponding literal `audio-v2` path segment.
Legacy handlers reject a recipe-2 session, current handlers reject an ordinary
session on the versioned path, and old routers do not match the extra segment.
Jellyfin HLS keeps the versioned URL whenever any selectable audio track is
surround, because an in-player track switch does not request a new playlist URL.
Direct-file and subtitle routes are unchanged because they never execute this
audio recipe.

Jellyfin HLS remuxes that copy both video and audio use the literal `remux-v1`
path segment instead (`/Videos/{id}/remux-v1/...`). The segment freezes copy
semantics for the life of the negotiation: old routers do not match it, current
handlers reject a copy session on an unversioned or `audio-v2` path and any
other session on the `remux-v1` path, and an audio switch to a track outside
the negotiated copy allowlist (same codec, same channel layout, accepted by the
device profile for fMP4) is rejected before any local, remote, or durable state
changes — the playlist and `EXT-X-MAP` URLs never change, so a switch that
alters the copied bytes' shape would require a new PlaybackInfo negotiation.
Because compat sessions are shared as one durable JSON document that every
mutation rewrites whole, the compat session structs preserve JSON fields they
do not declare; a binary that predates that envelope can still erase
newer-generation fields (such as the remux flags) during the single rolling
deploy that introduces them.

A remote start carrying source-channel facts is valid only for the exact AAC
stereo shape and must echo recipe version 2 after FFmpeg reaches readiness. The
caller stops a job that omits or contradicts that receipt. Shared reconstruction
cards use a versioned audio envelope that older readers reject, while live
session updates preserve codec, source/target channels, bitrate, and the
transcode decision as one recipe. A failed Jellyfin audio switch restores the
prior durable selection and executor facts so the same client report can retry.
Prepared downloads persist the audio recipe version and use `audio_v2_*` queue
states that pre-v2 API workers cannot claim or publish as ready.

They are advertised only if an eligible executor actually has the required
capability. The ordinary FFmpeg feature probe is cached; the more expensive
tone-map smoke probe is lazy and cached by binary, backend, and device:

| Transformation | Probe |
| --- | --- |
| `server_dv7_to_hdr10` | `ffmpeg -bsfs` contains `dovi_rpu` |
| `audio_to_aac` | `ffmpeg -encoders` contains an `aac` encoder and a bounded silent-frame smoke test executes the exact stereo-downmix limiter graph |
| `video_to_h264` | `ffmpeg -encoders` contains any of `libx264`, `h264_qsv`, `h264_vaapi`, `h264_nvenc`, `h264_videotoolbox` |
| `hdr_to_sdr_tonemap` | A bounded decode → BT.709 H.264 encode succeeds for the advertised PQ, BT.2100 HLG, legacy HLG, BT.709 SDR-base, and/or BT.2020 SDR-base source kinds on the real software, VAAPI/QSV, or NVENC executor |

`GET /playback/capability` reports the union of currently eligible local and
pooled executors. The generic tone-map transformation does not reveal hardware
selection policy; the server freezes a validated executor into each accepted
recipe. Heterogeneous pools are filtered again at transport startup, and a stale
or older node advertisement is rejected instead of silently changing modes.
Dolby Vision compatibility IDs `1` through `6` resolve to their declared
standards-compatible base layer: `1` and `6` are PQ, `2` is BT.709 SDR, `3` is
legacy BT.709-gamut HLG, `4` is BT.2100 HLG, and `5` is BT.2020 SDR. ID `0`,
Profile 5, and a declared absent base layer remain unsupported. Missing,
reserved, legacy, or contradictory signaling
is only a candidate classification: the selected executor must successfully
decode and normalize samples near the beginning, midpoint, and end before the
first manifest is published. Positive and negative verdicts are cached against
the source revision, FFmpeg build, recipe, backend/device, and driver, so any
relevant change forces validation again.

An unavailable transformation is not silently skipped at plan time: it produces
its own terminal reason (`dv_conversion_unsupported`,
`audio_conversion_unsupported`, `video_conversion_unsupported`,
`hdr_transcode_unsupported`) so the client learns which conversion was missing
rather than seeing a generic refusal.

A client may advertise its *own* transformations in a delivery's
`transformations[]` with `executor: "client"` — Dolby Vision profile 7 → 8.1
conversion, for instance. The server accepts a client executor only when that
delivery is both `enabled` and `supported_on_device` **and** the request's
top-level `client_features` includes `client_video_transformations_v1`.
Duplicate `executor:name:recipe_version` triples are rejected. Client
transformations participate in plan identity exactly like server ones, so a
client that changes its transform version invalidates its prior attempt keys —
which is the intent.

Automatic work wholly owned by an original-file executor is not enumerated as
a transformation merely because it can include demuxing, local repackaging,
audio bridging, or display adaptation. Those operations do not give the server
a distinct selectable output recipe. Use a delivery claim for an executor
property; reserve transformations for named outcomes the server deliberately
selects and can describe in the plan.

### 11.1 Tone-map execution integrity

`hdr_to_sdr_tonemap` is the only transformation whose output is not derivable
from the plan alone: the same recipe run against a different executor, or
against bytes the catalog no longer describes, produces silently wrong pixels
rather than an error. These rules exist to make that impossible, and they bind
every executor — local FFmpeg, pooled transcode node, and prepared-download
worker alike.

**A frozen recipe carries every source fact its FFmpeg graph depends on.**
Source video profile and bit depth ride the frozen recipe alongside codec,
decode mode, duration, tone-map source kind, source revision, and Dolby Vision
provenance. A seek, restart, or reconstruct rebuilds from the frozen snapshot,
never from a later catalog row — a 10-bit hardware SDR-base recipe that dropped
its profile would degrade to an 8-bit assumption on reconstruct and change the
output. For the same reason a sidecar-only replan may reuse existing
audio/video bytes only when profile and bit depth are equal too; otherwise it
gets a new transport rather than old bytes under a new recipe.

**Every tone-map execution re-verifies the source immediately before it runs.**
Size, mtime, and content hashes are change signals, not proof that the executor
about to run sees the metadata the plan froze. So each attempt runs a bounded
FFprobe on the executor, normalized through the same `mediaprobe` path the
scanner persists with, and requires an exact match against the frozen
signature. A mismatch is permanent (the recipe is stale); an unavailable,
malformed, cancelled, or timed-out probe is transient and retryable. The two
must stay distinguishable across the local, jellycompat, and transcode-node
boundaries, and a frozen tone-map recipe is never replaced by a fresh plan
merely because its reconstruction failed. Verification runs for starts,
restarts, reconstructs, and prepared downloads — never for direct play, direct
stream, or an ordinary non-tone-mapped transcode.

**A tone-mapped reconstruction token is rejected by readers that predate it.**
Stream tokens for a tone-map transcode carry the `transcode_tonemap_v1`
play-method discriminator instead of `transcode`. Current readers map it back
to an ordinary transcode; an older binary in a rolling deployment rejects it
rather than reconstructing the HDR recipe without its tone-map stage.

**A remote prepared artifact is accepted only against its attestation.** A node
that advertised tone-map support can be replaced by an older binary before its
queued job runs, and an older decoder ignores the frozen recipe fields — so
artifact ID and size cannot prove the HDR source was tone-mapped. After a
successful encode the node publishes a receipt beside the artifact recording
the confirmed mode, output size, and a canonical fingerprint over every
transported byte-affecting field (the artifact ID is excluded: it is the
idempotency handle, not an encoding input). Publication is crash-ordered — the
output and its directory are synced before the receipt — so the receipt is the
commit record for bytes already on disk. Central accepts an artifact only when
every attested value matches its frozen request, and the expected size and
fingerprint travel through direct file targets and signed proxy claims too, so
a missing or mismatched receipt fails closed at delivery instead of serving
unverified bytes.

**Ambiguous Dolby Vision provenance fails closed.** Resolving a DV source for
tone mapping requires authoritative evidence that the configuration record was
present, that the base-layer compatibility ID was present and nonzero, and that
a base layer exists. Profile 5 and incomplete provenance are refused rather than
guessed at. Because a row written before the provenance columns existed decodes
to explicit `false` — indistinguishable from a source that genuinely has none —
the scanner records whether those keys were literally present, and a row that
predates them is reprobed once rather than trusted. A failed repair probe
preserves the previous technical metadata and leaves the probe timestamp empty
for a later bounded retry, so a legacy writer in a rolling upgrade can never
make an incomplete row look current.

---

## 12. Conformance

Three artifacts, in decreasing order of authority:

1. **`internal/playback/testdata/protocol_v3/`** — golden fixtures generated by
   `cmd/playbackfixtures` from the live server types. `make playback-fixtures`
   regenerates them; `make verify-playback-fixtures` fails CI if they are stale.
   Android and Apple CI vendor these and compare against them as **opaque
   expected output**. The direction of authority is inverted from where this
   protocol started: the server defines the contract and clients prove
   conformance, not the reverse. `conformance_matrix.json` covers the release
   train's evidence tiers, delivery fallback chain, replan operations and
   idempotency, quality ladder, audio-only route, HDR/Dolby Vision decisions,
   audio adaptation and exact-layout passthrough, text/bitmap subtitle policy,
   failure recovery, restart replay, capacity cleanup, output change, route
   event limits, opaque loop guard, and legacy-upgrade response in one
   generated cross-client corpus.
2. **`docs/design/schemas/playback-v3/`** — JSON Schemas for every wire body,
   with valid and invalid fixtures. Every bound mirrors a server validator, so a
   body these schemas reject is a body the server rejects.
   `internal/playback/contract` enforces that the schemas, the Go types, and the
   golden fixtures agree.
3. **This document** — the reasoning behind the above, and the normative source
   for anything the schemas cannot express (idempotency semantics, the timeline
   model, evidence-tier strictness, ordinal density).

A client that decodes the golden fixtures, echoes attempt keys byte-for-byte,
and round-trips a replan without computing an identity is conforming.
