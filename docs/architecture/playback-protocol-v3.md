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
request with a response, not a fire-and-forget event.

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
  "features": ["playback_plan_v3", "neutral_playback_v3_contract_v1", "layout_aware_passthrough", "playback_route_diagnostics",
               "device_quirks_v1", "seek_reanchor_v1", "direct_stream_resume_v1",
               "plan_source_duration_v1"],
  "deliveries": ["original_http", "server_remux_progressive", "server_remux_hls", "server_transcode_hls"],
  "transformations": [{"name": "audio_to_aac", "executor": "server", "recipe_version": "1", "validated_claims": ["audio_decode"]}]
}
```

The eight feature strings above are the full set this server version advertises:

| Feature | What it promises |
| --- | --- |
| `playback_plan_v3` | The three plan endpoints exist and behave as specified here |
| `neutral_playback_v3_contract_v1` | The server mints opaque `plan_attempt_key` values that clients only echo, and exposes `track_change` / `quality_change` as intent replans distinct from failure recovery |
| `layout_aware_passthrough` | Audio passthrough is decided from channel *layouts*, not just channel counts (§3) |
| `playback_route_diagnostics` | `POST /playback/route-events` is accepted |
| `device_quirks_v1` | Plans may carry `applied_quirks` and `runtime_corrections` (§9) |
| `seek_reanchor_v1` | The `seek_reanchor` replan operation is available (§6) |
| `direct_stream_resume_v1` | A direct route may resume mid-file rather than restarting |
| `plan_source_duration_v1` | `source.duration_seconds` is populated when known, so its absence means *unknown* rather than *unsupported* (§5) |

That last one is the reason feature detection is a list and not a version
number: without it, a client cannot tell a server that never sends the runtime
apart from a server that knows this particular file's runtime is genuinely
unknown, and both look like an absent field.

`deliveries` reports the four *server-side* delivery values, not the three
delivery classes a client negotiates in. §4 gives the folding.

`transformations` advertises only what the *installed* FFmpeg was probed for at
startup — a server without a `dovi_rpu` bitstream filter does not list
`server_dv7_to_hdr10`. A client must not assume a transformation exists because
this document names it.

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
| `platform_attested` | Apple (VideoToolbox) | Same walk, but profile and level are **skipped** — the platform attests the codec family rather than enumerating modes. All other bounds still apply. |
| `declared` | Web (`isTypeSupported`) | Flat list match only: `codecs_video` / `codecs_video_hardware` membership. No `video_decode[]` walk. |

Four rules follow from the table and are easy to get wrong:

**A flat claim without backing detail is a refusal, not a pass.** On `exact` and
`platform_attested`, if a codec appears in `codecs_video` but no `video_decode[]`
entry names that codec with `hardware: true`, the source is *not* eligible for a
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
| `profiles` | Case-insensitive string equality against the source profile |
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

**HDR is decided against the output, not the decoder.** `output.hdr_details` (the
display or receiver actually attached) takes precedence over
`client_capabilities.hdr_details` (what the device could do in principle). A
source whose dynamic range is recorded as `hdr_unknown` — legacy rows that only
stored a file-level HDR boolean — is treated as HDR10 when the output supports
HDR10, and the plan carries the `hdr_range_assumed_hdr10` degradation warning.
Refusing to play those outright would be worse than an assumption the client is
told about.

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
| `subtitles` | Six booleans: `embedded_text`, `sidecar_text`, `ass_styling`, `embedded_bitmap`, `sidecar_bitmap`, `font_attachments` |
| `features` | Class-scoped feature strings |
| `auth_header_refresh` | The client can re-fetch stream auth headers without restarting playback |
| `validated_claims` | Claims the client asserts it has verified for this class |
| `transformations` | Client-executed transformations offered for this class (§11) |

Both booleans must be true for the class to be eligible; they are separate
because "the user turned HLS off" and "this device has no HLS player" call for
different degradation warnings and different diagnostics. A class the client
omits entirely is unavailable — the server will not guess.

`stream.header_refresh` tells the client what to do when the stream URL's auth
expires: `none` means the URL is stable for the session, `session` means
re-request headers from `header_refresh_url` rather than restarting playback.

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

`operation` is one of five:

| Operation | Meaning | Requires |
| --- | --- | --- |
| `failure_recovery` | The plan failed on the device | `failure.classification` |
| `seek_failure_recovery` | A seek failed | `failure.classification` |
| `seek_reanchor` | Move a server-anchored timeline to a new position | — |
| `track_change` | The user picked a different audio or subtitle track | — |
| `quality_change` | The user picked a rung from `available_qualities` | non-empty `quality_preference` |

The asymmetry is intentional. A seek reanchor is a timeline operation, not a
failed recipe — a classification is still accepted from older callers but never
selects seek semantics. A track or quality change is not a failure at all, so
demanding a classification would force clients to invent one. But
`quality_change` *must* name the rung it wants: an empty `quality_preference`
normalizes to `auto`, which is a different user intent than the menu selection
the operation models, so the server rejects it rather than silently doing
something else.

**User-intent operations behave differently from failure recovery.**
`track_change` and `quality_change` replace what were separate v2 endpoints (an
audio PATCH and a client-posted transcode start). Nothing failed, so the previous
route stays eligible: neither the attempted-key history nor the failed-plan
exclusion applies. A client may therefore be handed back a plan it has already
tried — that is correct here, and a client must not treat a repeated
`plan_attempt_key` as a loop.

When such an operation actually changes something — the request's tracks or
quality differ from what the session currently has — the server also tries to
return to the *requested* edition rather than staying on whatever alternate
version a previous fallback landed on, since a user switching tracks may well
want the original file back. That is a preference, not a guarantee: if the
requested edition no longer resolves or fails its preflight, the healthy active
alternate is kept. Track identities are remapped only when the edition really
changes, because remapping within one file would degrade an exact selection to a
best-match lookup and could silently move a listener off a commentary track.

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

---

## 7. Registries

### 7.1 Decision reasons

Why the server picked this route. Informational; clients may log or display but
must not branch on an unrecognized value.

`validated_original_playback`, `container_normalization`, `audio_adaptation`,
`hls_audio_adaptation`, `hls_packaging_required`, `subtitle_burn_in_required`,
`client_dv7_to_dv81`, `client_dv7_to_hdr10`, `evidence_insufficient_for_direct`,
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
`subtitle_conversion_unsupported`.

*Subtitle policy:* `subtitle_burn_in_source_unsupported`,
`subtitle_codec_unsupported`, `subtitle_track_invalid`,
`subtitle_track_unavailable`, `subtitle_unavailable_in_version`.

*Transport and session:* `internal_error`, `session_expired`,
`subtitle_artifact_unavailable`, `capacity_unavailable`,
`audio_transcoding_disabled`,
`transcode_start_failed`, `transcode_node_unavailable`,
`transcode_node_capability_unavailable`, `track_unavailable`,
`invalid_seek_position`, `invalid_replan`, `seek_reanchor_route_changed`,
`seek_reanchor_recipe_unavailable`,
`seek_reanchor_intent_mismatch`, `seek_failure_recovery_intent_mismatch`,
`policy_denied`.

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

`subtitle.mode` is `off`, `render` (client draws the sidecar), `convert` (server
transcodes it to a client-renderable format first — always to WebVTT, served as
`text/vtt` at a `.vtt` URL), or `burn_in` (rendered into the video, which forces
a transcode).

The sidecar URL suffix is part of the representation contract, not decoration.
An embedded `hdmv_pgs_subtitle`/PGS sidecar is lossless binary PGS at a `.sup`
URL with `application/octet-stream`; cached full-track responses support `HEAD`
and byte ranges. Text conversion is always WebVTT at `.vtt`, while lossless
ASS/SSA uses `.ass`. A suffix that does not match the selected track or a valid
conversion is rejected with `415` rather than returning bytes of a different
type under the requested extension.

---

## 9. Plan identity

The server mints both identifiers. **Clients treat both as opaque, case-sensitive
tokens and never implement either identity algorithm.** Their wire prefixes and
lengths are validation syntax, not a derivation recipe.

`plan_id` identifies the server's playback decision. It is stable when the same
attempt produces the same source, delivery, recipe, tracks, subtitle mode,
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
`preserves_source: true`. Transcode rungs are added only below the source's own
height, and only when HLS is available to the client, transcoding is enabled,
4K transcoding is permitted for a 4K source, and the source is not HDR. Ladder
bitrates:

| Rung | kbps |
| --- | --- |
| 2160p | 20000 |
| 1080p | 6000 |
| 720p | 2000 |
| 480p | 1500 |

Registry availability is deliberately *not* consulted when building the menu: a
capability check there could trigger lazy node fetches that a source-preserving
start must never pay for. A rung whose toolchain turns out to be missing degrades
to a retryable terminal at replan time instead.

Audio-only sources publish a single `original` rung — quality rungs are a video
concept.

`quality_preference` accepts `auto`, `original` (aliases `source`, `max`), and
`2160p` / `1080p` / `720p` / `480p` with the obvious aliases (`4k`, `uhd`, `fhd`,
`hd`, `sd`). An unrecognized value normalizes to `auto` and the response carries
the `quality_preference_normalized` warning rather than an error.

---

## 11. Transformations

A transformation is a named, versioned media operation with claims attached.

| Name | Executor | Recipe version | Promises | Claims |
| --- | --- | --- | --- | --- |
| `audio_to_aac` | `server` | `1` | — | `audio_decode` |
| `video_to_h264` | `server` | `2` | `sdr` output | `h264_decode` |
| `server_dv7_to_hdr10` | `server` | `1` | `hdr10` output | `dolby_vision_metadata_removed`, `hdr10_base_layer_preserved`, `enhancement_layer_discarded` |

They are advertised only if the installed FFmpeg actually has the required
capability, probed once at startup:

| Transformation | Probe |
| --- | --- |
| `server_dv7_to_hdr10` | `ffmpeg -bsfs` contains `dovi_rpu` |
| `audio_to_aac` | `ffmpeg -encoders` contains an `aac` encoder |
| `video_to_h264` | `ffmpeg -encoders` contains any of `libx264`, `h264_qsv`, `h264_vaapi`, `h264_nvenc`, `h264_videotoolbox` |

`GET /playback/capability` reports the *local* probe only. A deployment with
pooled transcode nodes may still plan an HLS route using a transformation those
nodes advertise but the local FFmpeg lacks, so the capability list is a floor,
not a ceiling — one more reason a client must not precompute routes from it.

An unavailable transformation is not silently skipped at plan time: it produces
its own terminal reason (`dv_conversion_unsupported`,
`audio_conversion_unsupported`, `video_conversion_unsupported`) so the client
learns which conversion was missing rather than seeing a generic refusal.

A client may advertise its *own* transformations in a delivery's
`transformations[]` with `executor: "client"` — Dolby Vision profile 7 → 8.1
conversion, for instance. The server accepts a client executor only when that
delivery is both `enabled` and `supported_on_device` **and** the request's
top-level `client_features` includes `client_video_transformations_v1`.
Duplicate `executor:name:recipe_version` triples are rejected. Client
transformations participate in plan identity exactly like server ones, so a
client that changes its transform version invalidates its prior attempt keys —
which is the intent.

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
