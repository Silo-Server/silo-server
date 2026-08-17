# Intro Skip Mode: Never / Ask / Always

Status: Proposed (spec + cross-repo plan)
Date: 2026-08-16
Repos affected: `silo-server` (contract, migration, web), `silo-android`, `silo-apple`

## Summary

Replace the boolean `playback.auto_skip_intro` with a three-way
`playback.intro_skip_mode` setting — `never`, `ask`, `always` — and define one
intro-skip prompt state machine that every Silo client implements identically:
web (browser), Android (phone, tablet, TV) and Apple (iPhone, iPad, tvOS, macOS).

The boolean already encodes two of the three modes: `true` is "count down, then
skip" and `false` is "show a Skip Intro button". `never` (no prompt at all) is
the only new behaviour, and it matches what Jellyfin's Intro Skipper offers.
The server change is a contract addition plus a compatibility mirror; the
client change is a rewrite of the prompt behaviour to the table below.

## Motivation

- There is no way to turn the prompt off. A viewer who wants to watch intros
  still gets a button over the picture on every episode.
- The `always` behaviour today is *countdown-then-skip*: the intro plays for
  five seconds while a bar fills. Viewers who chose "always" wanted the intro
  gone; showing it for five seconds first is the wrong default. The right shape
  is *skip immediately, offer an undo*.
- The three clients drifted. Android TV (silo-android#210) got a wall-clock
  fill, focus-on-appear, and D-pad/Back rules that neither web nor Apple has.
  Cross-platform consistency needs a written contract, not three
  re-implementations of a verbal one.

## Setting

### `playback.intro_skip_mode`

| Field | Value |
| --- | --- |
| `value_schema` | `enum` — `never` ("Never"), `ask` ("Ask to skip"), `always` ("Skip automatically") |
| `default_value` | `ask` — identical to today's `auto_skip_intro = false` |
| `persistence` | `remote` |
| `allowed_scopes` | `profile`, `profile_device` (same as the boolean) |
| `resolution_order` | `profile_device`, `profile`, `default` |
| `category` / `label` | `playback` / "Skip intros" |
| `recommended_control` | `select` (the schema has no segmented control; clients that have one should use it) |
| `introduced_in` | 7 |

Contract revision bumps 6 → 7. `playback.auto_skip_intro` stays in the
manifest, marked `deprecated: true`, with `notes` pointing at the new key. It
is not removed in this cut: every shipped client reads it, and the profile DTO
carries it as a `NOT NULL` column.

### Migration

A Goose SQL migration copies every stored `playback.auto_skip_intro` row (all
scopes, all backends the row can live in) to `playback.intro_skip_mode` at the
same identity:

| stored boolean | new enum |
| --- | --- |
| `true` | `"always"` |
| `false` | `"ask"` |

`ON CONFLICT DO NOTHING` so a re-run, or a client that already wrote the enum,
never has its choice overwritten by the mirror. Nobody currently has `never`,
so no data becomes unrepresentable. Down: delete the `intro_skip_mode` rows.

### Compatibility mirror (one release)

While old clients are in the field, the two keys are kept in step at write
time so a preference set on one client shows up correctly on the others:

| Write | Server also writes |
| --- | --- |
| `PUT /settings/values/playback.auto_skip_intro` (any scope) | `intro_skip_mode` at the same identity: `true → always`, `false → ask` |
| `PUT /settings/values/playback.intro_skip_mode` (any scope) | `auto_skip_intro` at the same identity: `always → true`, else `false` |
| `DELETE` of either | delete the other at the same identity |
| Legacy `PUT /profiles/{id}` with `auto_skip_intro` | both keys at `profile` scope (extends `profiles_settings_sync`) |
| Legacy `PUT /settings/{key}` (runtime-key route) with `auto_skip_intro` | both keys, wherever that route lands them |
| Either key written or cleared at `profile` scope | the `user_profiles.auto_skip_intro` column, so `GET /profiles` stays truthful for old clients |

The column is the third copy of one preference, and it tracks every canonical
profile-scope change to either key — not only the enum. A column only the enum
could move would end up contradicting the row the caller stored: an enum write
sets it, and the boolean write or the `DELETE` that follows could not put it
back. A cleared row means "inherit again", so the column falls back to the
contract default with it. Device-scope writes leave it alone; it is
profile-wide, and one television's override is not the household's choice.

The lossy direction is deliberate: an old client sees `never` as `false`
(= ask) and shows the button. That is the least surprising degradation. An old
client that then flips the toggle overwrites `never` — acceptable for the
overlap window, and the reason the mirror is dropped once clients have moved.

Removal plan: once Android, Apple and web all read `intro_skip_mode`, a
follow-up removes the mirror, marks the boolean `deprecated` in the profile DTO
and stops writing it. The `user_profiles.auto_skip_intro` column is retired
with the other legacy profile playback columns, not on its own.

### `playback.auto_skip_credits`

Not changed here. It has the same shape and will want the same treatment
(`credits_skip_mode`); keeping it out keeps this change reviewable, and the
credits prompt has a different interaction (it competes with Next Up).

## Prompt behaviour

Terminology:

- **intro** — a `TimeRange` `[start, end)` from the server's chapter/marker
  data, with a stable per-item key so "this intro" survives seeks.
- **inside** — playback position `p` with `start ≤ p < end`.
- **pill** — the single on-screen prompt. It has a **timer**, always
  wall-clock, always the same length across platforms
  (`INTRO_PROMPT_SECONDS = 5`; contract-visible so a future setting can drive it).
- **resolved** — the viewer has made a decision for this intro in this
  playback session. A resolved intro never shows a pill again, including if
  the viewer scrubs back into it.
- **Select** — click/tap on the pill, or Select/OK on a remote while the pill
  is focused, or Enter/Space on web while focused.
- **Back** — remote Back, keyboard Escape, Android system back.

### `never`

Entering an intro does nothing. No pill, no skip. Chapter markers still render
on the timeline where the client already does so.

### `ask`

| Event | Outcome |
| --- | --- |
| Enter intro (not resolved) | Show pill **"Skip Intro"** with timer running. Pill takes focus on appear on focus-driven platforms (TV, keyboard). |
| Timer runs out | Pill hides. Intro keeps playing. Intro is **not** resolved — scrubbing back into it re-offers. |
| Select | Seek to `end`. Intro resolved. Pill hides. On TV, focus lands on the timeline/transport, never nowhere. |
| Back | Pill hides. Intro keeps playing. Intro resolved. Press is consumed (does not exit playback / close overlay); a second Back behaves normally. |
| D-pad / arrow / pointer move away | Focus moves as normal. **Timer keeps running.** Pill stays until timer ends. |
| Pause | Timer **freezes** at its current value; resumes from there on play. Pill stays visible. |
| Seek out of the intro | Pill hides, timer stops. Not resolved. |
| Seek back into the intro | Same as Enter (unless resolved). Timer restarts from full. |
| Playback stall / rebuffer < 1.5 s | Ignored: timer keeps running (see *Timing*). |
| Controls overlay hides/shows | Pill stays; it may reposition (e.g. drop toward the corner when transport hides). Never hidden by the overlay timeout. |

### `always`

| Event | Outcome |
| --- | --- |
| Enter intro (not resolved) | **Seek to `end` immediately.** Show the undo pill — caption **"Intro skipped"** over the action **"Watch Intro"** — with timer running. Focus as in `ask`. |
| Timer runs out | Pill hides. Playback continues past the intro. Intro resolved. |
| Select | Seek to `start`. Intro resolved (so re-entering the range does not skip again). Pill hides. Focus lands on transport. |
| Back | Pill hides. Playback continues. Intro resolved. Press consumed. |
| Move focus away | Timer keeps running. |
| Pause | Timer freezes; resumes on play. |
| Seek by the viewer into a resolved intro | Nothing. The viewer asked for it. |
| Seek by the viewer into an unresolved intro | Same as Enter — skipped again, undo offered. This only happens on a fresh session or an item with several marked intros. |

The pill in `always` is the *undo* affordance, so unlike `ask` its timeout does
resolve the intro: the viewer was told it was skipped and let it go.

### Timing

- The timer is **wall-clock**. Compose scales `AnimationSpec` by the system
  animator duration scale, `prefers-reduced-motion` can shorten CSS
  transitions, and UIKit honours accessibility motion settings; a countdown to
  an action must ignore all of those or the fill lies about when the action
  fires. Decorative motion (fade in/out, reposition) may still honour them.
- Timer state and its visual (fill / ring / number) derive from the same
  clock. Never run two timers.
- The timer starts when playback is actually running, not when the player is
  still coming up, so the pill and the fill start together.
- A pause is a pause only after `PLAYBACK_PAUSE_GRACE_MS = 1500`; a shorter
  `isPlaying == false` is a rebuffer and does not touch the timer. (This is
  the `settlingFalseEdges` behaviour from silo-android#210; port it, don't
  reinvent it.)

### Input handling

- Select / Back for the pill are handled at the **player root**, not on the
  pill widget, so they work whether or not the pill is in the focus tree. TV
  and keyboard both need this.
- While the pill is focused, Select acts on it with a single press — no
  navigate-then-press.
- The pill must never steal focus during a scrub or from an open menu. If the
  viewer is mid-interaction when the intro starts, the pill appears unfocused
  and the timer runs anyway.
- Pointer platforms (web, phone, tablet): the pill is a normal button; hover
  and tap behave as Select. Tap outside the pill is not Back.

### Presentation

- One pill, lower-right of the video, above the transport cluster while
  controls are visible; drops toward the corner when they hide.
- Fill creeps left → right and reaches full exactly when the timer ends
  (`ask`: pill hides; `always`: pill hides). Same colour language everywhere:
  dimmed when unfocused, lit when focused, solid on press.
- Copy: `ask` is a single action **"Skip Intro"**. `always` is two lines: a small muted caption **"Intro skipped"** (the confirmation) above the action **"Watch Intro"** (the undo). The confirmation and the action are never one phrase — "Intro Skipped · Play Intro" reads as one instruction and was rejected. On very tight layouts the caption may drop and the pill degrades to the action alone. Localised via
  each client's normal string tables; the semantics are fixed.
- Disappears instantly on Select/Back; fades on timeout.

## Client rollout

Each client reads `playback.intro_skip_mode` from the effective settings
endpoint, falling back to `auto_skip_intro` only if the server contract
revision is < 7. Settings UI shows a three-way control (segmented where the platform has one) labelled
Never / Ask to skip / Skip automatically; the old switch goes away.

| Repo | Work |
| --- | --- |
| `silo-server` (this change) | Manifest rev 7, migration, write mirror, bindings regen, three-way web settings controls, and the web player prompt/undo state machine above. Web falls back to the legacy boolean only when the connected server predates revision 7. |
| `silo-android` | `IntroAutoSkipController` (shared KMP, drives phone + TV) gains the three modes and the `Skipped/OfferingUndo` state; `TvIntroAutoSkipBanner` / `IntroAutoSkipBanner` render both copies; settings screens swap the switch for a segmented control; `SettingKeys.kt` regenerated by `make settings-bindings`. |
| `silo-apple` | Same state machine in the shared player; tvOS focus rules; iOS/macOS pointer rules; `SettingKeys.generated.swift` regenerated. |

Conformance: the tables above are the test oracle. Each client's state-machine
tests should assert them case-for-case (silo-android's
`IntroAutoSkipControllerTest` is the template), so a divergence is a failing
test rather than a bug report.

## Open questions

- Should `ask`'s timeout be configurable (a `playback.intro_prompt_seconds`
  key like `next_up_prompt_seconds`)? Not in this cut; the constant is named
  so it can become one.
- Whether the same tri-state should reach `auto_skip_credits` in the same
  release train, or wait for the Next Up interaction to be specified.
