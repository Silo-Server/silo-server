# Intro Skip Mode: Never / Ask / Always

Status: Shipped in `silo-server` (2026-08-17, #660). Amended 2026-11-23: the pill timer is 10 seconds, and recap and credits pills follow the `ask` table (see *Recap and credits pills*). Client adoption tracked per repo
Date: 2026-08-16, amended 2026-11-23
Repos affected: `silo-server` (contract, migration, web), `silo-android`, `silo-apple`, `silo-roku`

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

The 2026-11-23 amendment gives recap and credits ranges the same prompt,
lengthens the timer to 10 seconds, and adds Roku.

## Motivation

- There is no way to turn the prompt off. A viewer who wants to watch intros
  still gets a button over the picture on every episode.
- The `always` behaviour before this design was *countdown-then-skip*: the intro plays for
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
| `default_value` | `ask` — identical to the earlier `auto_skip_intro = false` |
| `persistence` | `remote` |
| `allowed_scopes` | `profile`, `profile_device` (same as the boolean) |
| `resolution_order` | `profile_device`, `profile`, `default` |
| `category` / `label` | `playback` / "Skip intros" |
| `recommended_control` | `select` (the schema has no segmented control; clients that have one should use it) |
| `introduced_in` | 7 |

Contract revision bumps 6 → 7. `playback.auto_skip_intro` stays in the
manifest, marked `deprecated: true`, with `notes` pointing at the new key. It
was not removed in #660: every shipped client read it, and the profile DTO
carried it as a `NOT NULL` column.

### Migration

A Goose SQL migration copies every stored `playback.auto_skip_intro` row (all
scopes, all backends the row can live in) to `playback.intro_skip_mode` at the
same identity:

| stored boolean | new enum |
| --- | --- |
| `true` | `"always"` |
| `false` | `"ask"` |

`ON CONFLICT DO NOTHING` so a re-run, or a client that already wrote the enum,
never has its choice overwritten by the mirror. No stored row could hold `never`
before the migration, so no data becomes unrepresentable. Down is deliberately a no-op: it cannot tell
backfilled rows from ones a client wrote later, and an older server ignores the key.

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

The two legacy rows were `/api/v1` routes; they went away when `/api/v1` was retired
(#3200).

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

### `playback.auto_skip_credits` and `playback.auto_skip_recap`

Not changed. Both stay booleans, and an automatic skip shows no undo pill. #660 left the
credits prompt out because it competes with Next Up; the 2026-11-23 amendment specifies it,
with the recap prompt, in *Recap and credits pills*. A three-way `credits_skip_mode` is not
part of that amendment.

## Prompt behaviour

Terminology:

- **intro** — a `TimeRange` `[start, end)` from the server's chapter/marker
  data, with a stable per-item key so "this intro" survives seeks.
- **inside** — playback position `p` with `start ≤ p < end`.
- **pill** — the single on-screen prompt. It has a **timer**, always
  wall-clock, always the same length across platforms
  (`INTRO_PROMPT_SECONDS = 10`; contract-visible so a future setting can drive it).
  The same constant times the `always` undo pill and the recap and credits pills.
  One exception: in `ask`, after the timer has run out, the pill returns
  **without a timer** while the controls overlay shows (see the `ask` table).
- **resolved** — the viewer has made a decision for this intro in this
  playback session. A resolved intro never shows a pill again, including if
  the viewer scrubs back into it.
- **Select** — click/tap on the pill, or Select/OK on a remote while the pill
  is focused, or Enter/Space on web while focused. While the timed pill shows,
  Select at the player root counts too (on web, Enter/Space with nothing
  focused); the untimed pill answers only when activated.
- **Back** — remote Back, the Apple TV remote's Menu button, keyboard Escape, Android
  system back. Touch clients have no Back.

### `never`

Entering an intro does nothing. No pill, no skip. Chapter markers still render
on the timeline where the client already does so.

### `ask`

| Event | Outcome |
| --- | --- |
| Enter intro (not resolved) | Show pill **"Skip Intro"** with timer running. Pill takes focus on appear on focus-driven platforms (TV, keyboard). |
| Timer runs out | Pill hides. Intro keeps playing. Intro is **not** resolved — scrubbing back into it re-offers. |
| Controls overlay shows after the timer ran out, still inside | Pill **"Skip Intro"** returns without a timer and hides again with the controls. It sits above the scrubber. It does not take focus, does not keep focus the timed pill had, and does not claim Select/Back from elsewhere; activating it acts as Select. On TV, Up from the scrubber reaches it. It stays hidden in scrub mode. While it shows it counts as a skip pill for the buffering pills: a buffering pill waits behind it ([buffering quality](2026-10-17-buffering-quality.md), *Presentation rules*). |
| Select | Seek to `end`. Intro resolved. Pill hides. On TV, focus lands on the timeline/transport, never nowhere. |
| Back | Pill hides. Intro keeps playing. Intro resolved. Press is consumed (does not exit playback / close overlay); a second Back behaves normally. |
| D-pad / arrow / pointer move away | Focus moves as normal. **Timer keeps running.** Pill stays until timer ends. |
| Pause | Timer **freezes** at its current value; resumes from there on play. Pill stays visible. |
| Seek out of the intro | Pill hides, timer stops. Not resolved. |
| Seek back into the intro | Same as Enter (unless resolved). Timer restarts from full. |
| Playback stall / rebuffer < 1.5 s | Ignored: timer keeps running (see *Timing*). |
| Controls overlay hides/shows | The timed pill stays; it may reposition (e.g. drop toward the corner when transport hides). It is never hidden by the overlay timeout. The untimed `ask` offer that returns after the timer runs out shows and hides with the controls. |

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

### When the controls are up

The untimed `ask` row applies to the intro pill and to the recap and credits pills.

- "The controls are up" means: tvOS, controls shown and no menu open; iPhone and iPad,
  controls shown; macOS, the player's own controls-visible state; Android, Roku and web,
  the controls overlay shown.
- When the timer ends with the controls already up, the pill stays, loses its timer and
  gives up focus.
- On tvOS, entering scrub mode pins the controls; the untimed pill still stays hidden
  until scrub mode ends.

### Recap and credits pills

A recap or credits range from the same marker data gets one pill, a single action **"Skip
Recap"** or **"Skip Credits"**. It follows the `ask` table whatever `playback.intro_skip_mode`
is set to, and with one press: there is no navigate-then-press step. The range has a stable
per-item key and is **resolved** the way an intro is. *Timing*, *Input handling* and
*Presentation* apply to it unchanged.

| Event | Outcome |
| --- | --- |
| Enter the range (not resolved) | Show the pill with timer running. Focus as the intro pill: it takes focus on appear on focus-driven platforms, never during a scrub or under an open menu. |
| Select on the pill, or Select at the player root while the timed pill shows | The pill's action, in one press: seek to `end`. Range resolved. Pill hides. On TV, focus lands on the timeline/transport. For credits that run to the end of the file, each client keeps what it does at the end of the file today (tvOS opens the Next Up screen over the credits when it has something to offer, and otherwise ends the item). |
| Timer runs out | Pill hides. The range keeps playing and is **not** resolved. Select is the client's normal Select again: tvOS pauses and enters scrub mode; Android TV and Roku show the controls; web Space plays or pauses. |
| Controls overlay shows after the timer ran out, still inside | The pill returns without a timer, under the `ask` table's row and *When the controls are up*. |
| Back while the timed pill shows | Pill hides. The range keeps playing. Range resolved. Press is consumed (does not hide the controls, open a menu or exit playback); the next Back behaves normally. The untimed pill does not claim Back. |
| D-pad / arrow / pointer move away | Focus moves as normal. Timer keeps running. Pill stays until timer ends. |
| Pause | Timer freezes and resumes on play, with the intro pill's pause grace: a stop counts as a pause only after `PLAYBACK_PAUSE_GRACE_MS` (*Timing*). Pill stays visible. |
| Seek out of the range | Pill hides, timer stops. Not resolved. |
| Seek back into the range | Same as Enter (unless resolved). Timer restarts from full. |

Also:

- A range is also resolved when it was skipped automatically, and when Skip Credits opened
  the Next Up screen. After the viewer returns from that screen, no pill comes back for that
  range.
- At most one actionable pill shows, in this order: intro, then recap or credits, then
  buffering. A recap or credits pill that becomes due while the intro pill shows waits, and
  starts its full timer when it first shows.
- A recap or credits pill that has shown is covered while the intro pill shows, as when the
  controls bring back an intro pill that timed out over the same stretch: it is hidden and
  its timer is frozen, and it comes back with the time it had left when the intro pill
  hides. A pill whose timer ran out does not come back timed; it returns without a timer,
  with the controls, only while no intro pill shows.
- While the Next Up screen covers the player the pill is hidden and its timer is frozen.
  That screen still opens `playback.next_up_prompt_seconds` before the end of the file,
  whatever the credits marker says.
- In a Watch Party the pill shows only to members who may seek.
- The web player's Skip Preview pill applies the same table.
- `playback.auto_skip_recap` and `playback.auto_skip_credits` stay booleans, and an
  automatic skip shows no undo pill.

Known cost: while the timer runs, a Select or Space meant to pause skips instead. A
two-press design (the first press focuses, the second skips) was rejected: the first press
still would not pause, and *Input handling* forbids navigate-then-press. Opening the Next Up
screen at the credits marker was rejected too: it fails in a Watch Party, during shuffle play
and between the parts of a multi-part item, with nothing to play next, and with a scene after
the credits.

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

- Select / Back for the timed pill are handled at the **player root**, not on
  the pill widget, so they work whether or not the pill is in the focus tree. TV
  and keyboard both need this. The untimed pill that returns with the controls
  does not claim them (see the `ask` table).
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
| `silo-server` (#660) | Manifest rev 7, migration, write mirror, bindings regen, three-way web settings controls, and the web player prompt/undo state machine above. Web falls back to the legacy boolean only when the connected server predates revision 7. |
| `silo-server` (2026-11-23 amendment) | Web: `INTRO_PROMPT_SECONDS` is 10, and the recap, credits and preview pills move from an untimed button onto the prompt state machine (`useIntroSkipPrompt`). |
| `silo-android` | `IntroAutoSkipController` (shared KMP, drives phone + TV) gains the three modes and the `Skipped/OfferingUndo` state; `TvIntroAutoSkipBanner` / `IntroAutoSkipBanner` render both copies; settings screens swap the switch for a segmented control; `SettingKeys.kt` regenerated by `make settings-bindings`. Amendment: `DEFAULT_COUNTDOWN_SECONDS` is 10, the untimed pill returns with the controls, and `MarkerSkipPill` / `TvMarkerSkipPill` follow the recap and credits table. |
| `silo-apple` | Same state machine in the shared player; tvOS focus rules; iOS/macOS pointer rules; `SettingKeys.generated.swift` regenerated. Amendment: `IntroSkipPrompt.promptSeconds` is 10, the untimed pill returns with the controls (macOS for the intro pill), the recap and credits pills follow their table on iPhone, iPad and tvOS, and Menu dismisses the timed pill on tvOS. |
| `silo-roku` | Amendment: the 10-second timer in `source/playback/skipIntro.bs`, the untimed pill with the controls, and timed recap and credits pills with a timer indicator. |

Conformance: the tables above are the test oracle. Each client's state-machine
tests should assert them case-for-case (silo-android's
`IntroAutoSkipControllerTest` is the template; silo-apple's `IntroSkipPromptTests`
and the web player's `useIntroSkipPrompt` tests cover the same tables), so a
divergence is a failing test rather than a bug report.

## Open questions

- Should the pill timer be configurable (a `playback.intro_prompt_seconds`
  key like `next_up_prompt_seconds`)? Not in #660 or the 2026-11-23 amendment;
  the constant is named so it can become one. It now times the recap and credits
  pills too.
- Whether `auto_skip_recap` and `auto_skip_credits` should become the same tri-state. The
  2026-11-23 amendment specified their pills and left both settings as booleans.
