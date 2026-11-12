# Playback planner

How the server chooses a playback route for one file, for people changing
that code. The wire contract is in
[playback-protocol-v3.md](playback-protocol-v3.md).

## Candidates

`PlanPlaybackV3` (`internal/playback/candidate_planner_v3.go`) plans one file
for one request. It never looks at another version of the item; the start and
replan handlers in `internal/api/handlers/playback_v3.go` do that by calling it
again. It works in four steps:

1. **Facts.** Source checks, the server bitrate cap folded into the request,
   and what each delivery class can carry (`videoFacts` in
   `candidate_video_v3.go`).
2. **Candidates.** Every way the file could play, one option per dimension:
   route, video recipe, audio, delivery and HLS subtitle presentation.
   A combination the facts rule out is either not generated or rejected
   when it is checked.
3. **Sort.** `candidatePreferenceV3` orders the list.
4. **Select.** Candidates are checked from the top. The first viable one whose
   attempt key is not in `AttemptedKeys` wins, so a replan after a failure
   takes the next one. Plans are built in `candidate_routes_v3.go`.

## Preference rules, in order

1. The original file, then a copy remux, then an encode.
2. The video recipe: client DV7 to 8.1, client DV7 to HDR10, server DV7 to
   8.1, copy, DV8 base layer, client-managed range, server strip to HDR10,
   HEVC encode, H.264 encode.
3. Audio copied before converted.
4. Progressive before HLS.
5. On-demand subtitle renditions, then the pipeline rendition, then a sidecar.

## Special cases

Each one is commented where it is implemented.

- **Both remuxes lack AAC.** When both remux deliveries must convert the
  audio codec and neither pool offers AAC, planning refuses (retryable) before
  any remux or encode. The missing toolchain is the actionable cause.
- **The HLS remux lacks AAC.** When an HLS copy remux needed AAC that no HLS
  remux executor offers and no other copy route played, planning refuses
  (retryable) instead of encoding.
- **Audio-converting remux under a server cap.** The converted remux may not
  fit the cap, so an encode follows with its own decision reason, and without
  HLS it refuses `client_hls_unsupported`.
- **Failed HEVC encode.** Once the HEVC sidecar plan is attempted, every HEVC
  candidate is dropped and the H.264 encode follows.
- **Audio-only remux.** It has one audio recipe, fixed by the facts, so once it
  is attempted planning ends.
- **Server cap.** When the source must be encoded to fit the cap, only
  encoding candidates exist, and a refusal is renamed
  `bitrate_policy_unavailable`, keeping `retryable`.

When nothing is selected, a video file's refusal is the last route's: the
encode's `adaptation_exhausted`, or `adaptation_unavailable` when no encode was
a candidate. An audio-only file, which has no encode, ends with
`adaptation_exhausted`.

## Lazy lookups

`PlannerInputV3` carries lookups that can reach pooled nodes or run FFmpeg:
the delivery registries, the tone-map capabilities, the Dolby Vision RPU
probe, the subtitle-rendition checks and the cue index. Each runs at most once
per planning run (`memoizeLookupsV3`), and only when a candidate that needs it
is checked. A start that plays the original file asks none of them, with two
exceptions. For a Dolby Vision source, the strip's registries and the RPU probe
are asked before the original file, because the original plan carries the
`dolby_vision_strip_unsupported_by_source` warning. And when an automatic
quality reduction would need an encode, the planner asks whether one can run
(the HLS video registry, and the tone-map capabilities for an HDR source)
before it drops the reduction and plays the original. Each delivery asks its
own executor pool.

## Plan identity

A plan's ID and attempt key hash its delivery, stream, recipe, tracks,
subtitle mode, renditions, transformations and quirks. Finalize the identity
after all of those are set, and again after any later change, as the subtitle
renditions do. The order of appends to `Transformations`, `AppliedQuirks` and
`DegradationWarnings` is part of the output and of the committed fixtures.

## Refusals

Handlers decide what to try after a refusal from `PlannerResultV3` methods
in `internal/playback/plan_refusal_v3.go`, not from reason strings:
`AnotherVersionMayPlay()`, `SubtitleRefusal()`, `ToneMapDependentRefusal()`,
and the `SettingsDependentRefusal` field. A new refusal, or a change to which
reason a cause reports, must keep those answers right.
