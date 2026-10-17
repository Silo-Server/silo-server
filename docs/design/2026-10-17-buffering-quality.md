# Lowering quality when playback keeps buffering

Status: Proposed
Date: 2026-10-17
Repos affected: `silo-server` (contract, planner, store, web), `silo-android`, `silo-apple`

## Summary

When playback keeps rebuffering, every Silo client should notice, lower the
quality, and remember the result for that device and network. Today only the
web player does this.

This design defines:

1. One stall detector with the web player's thresholds, and one state machine
   for what happens next. Every client implements both identically: web,
   Android (phone, tablet, TV) and Apple (iPhone, iPad, tvOS, macOS).
2. Automatic lowering on Auto, on by default from the first release, with a
   **Restore quality** undo. A new setting switches it to asking first or turns
   it off. A quality the viewer picked is never lowered without asking.
3. A server-side learned limit. When Auto is lowered because of buffering, the
   server keeps that limit for the device on that network for 7 days and plans
   Auto under it. Viewers can see and reset it in Settings.
4. The bandwidth probe on Android and Apple, with the server's existing 80%
   margin, and probe results reported to the server. Viewers see their
   connection speed in Settings, and admins see it on the device page.

## Decisions

Settled on 2026-10-17:

| Question | Decision |
| --- | --- |
| Lower automatically, or ask? | Lower automatically on Auto, from the first release. Viewers who find it unreliable can switch the setting to **Ask first** or **Never**, reset a learned limit, or cap quality with **Maximum bitrate**. |
| How long a learned limit lasts | Longer than the web's 30 minutes: 7 days from the last observation (proposed below). |
| Margin on a probe estimate | Keep the server's 80%. |
| Report probe results to the server? | Yes. Admins can see per-device connection speeds. |

## Motivation

On 2026-10-17 an onn 4K Pro (Android TV) buffered all the way through
*A Minecraft Movie* on Auto. The file was a 63 Mbps (average) 4K Dolby Vision
remux. The link delivered 40–80 Mbps with 50 ms of round-trip time, and the
player's 128 MiB buffer held only about 17 seconds at that bitrate. What happened:

- Android sends no `bandwidth_estimate_kbps` (silo-android#613), so Auto sent the
  source.
- The one failure replan returned a new plan at the same bitrate.
- Later 10–20 second stalls triggered nothing, because Android only detects a
  start that stalls for 20 seconds.
- The item has a 31.6 Mbps 1080p version that would have played.

The surfaces have drifted:

| | Web | Apple | Android phone | Android TV |
| --- | --- | --- | --- | --- |
| Probes before playback | Yes | No | No | No |
| Sends an estimate | Yes | No | No | No |
| Detects repeated rebuffering | Yes | No | No (a 20 s start only) | No (a 20 s start only) |
| Lowers quality mid-title | Automatically on Auto; offers it in Watch Together | No | No | No |
| Remembers a slow link | 30 min, in the browser | No | No | No |
| Notice with an action | Yes | No | No | No |
| Speed shown in Settings | No | No | No | No |
| Rebuffer telemetry | No | No | Local log only | Local log only |

Two server gaps make a client-side fix incomplete:

- `auto` with a low estimate can return the source unchanged. When the only
  reason to transcode is a bandwidth reduction and the transcode cannot run (an
  HDR source without a tone-map policy, or no transcode route), the planner
  delivers the source with `quality_reduction_unavailable`
  (`internal/playback/plan_v3.go`). It does not try a lower-bitrate version of
  the same item, as the menu's 4K fallback does.
- Nothing records rebuffering, so nobody can tell how often it happens or
  whether a fix worked.

## Trust model

Two signals feed Auto, and they get different weight:

- **A probe estimate is a hint.** It is used only when playback starts, and
  only to rule out a source the link clearly cannot carry: the server's existing
  rule, source bitrate above 80% of the estimate. It never raises quality in the
  middle of a title.
- **Repeated rebuffering is the evidence.** It catches whatever the probe
  missed: a probe that overstated the link, scenes that peak well above the
  file's average bitrate, and congestion that starts mid-film.

Clients detect stalls, because only they can see them. The server decides
quality, because it owns the ladder and the margin. Clients report what they
measured and where it came from, and never do Auto's arithmetic.

## Terms

- **Plan**: the playback plan the player is playing. A replan that returns a
  different plan starts a new one. An in-place update, such as a subtitle that
  became ready, does not.
- **Delivered bitrate**: `effective_recipe.bitrate_kbps`, else
  `source.bitrate_kbps`.
- **Stall**: the player has run out of data while it should be playing. These
  never count as stalls:
  - buffering caused by a seek, a pause, the end of the media, or a source
    reload;
  - buffering while media requests are failing, or while the device is offline.
    That is an outage, and the reconnect path owns it.
  - stall time inside the startup grace: the first `STARTUP_GRACE_MS = 10000` of
    a plan, counted from its first frame, or from its first wait if no frame has
    appeared yet. A stall that begins in the grace counts from the end of the
    grace.
- **Counted stall**: a stall that lasted at least `STALL_MIN_MS = 1000` when
  playback resumed.
- **Trigger**: either of these, using the web constants:
  - `STALLS_BEFORE_TRIGGER = 2` counted stalls that ended within
    `STALL_WINDOW_MS = 120000` of each other;
  - one stall reaching `LONG_STALL_MS = 6000`, while it is still in progress or
    within the window after it ends.
- **Lower rung**: the highest `available_qualities` entry whose bitrate is below
  the bitrate of the active rung. When the active quality is Auto, the
  comparison is against the delivered bitrate. This is the web's
  `lowerQualityOption`.
- **Step-down estimate**: `floor(0.7 × delivered bitrate)`. It counts only when
  it is at least 100 kbps. Auto plans for 80% of an estimate, so the next stream
  targets about 56% of the one that stalled.
- **Answered**: the viewer has decided about buffering for this title at this
  quality, by **Keep quality** or **Restore quality**. An answered title is not
  lowered or prompted again until the viewer changes quality.

## Behaviour

The controller runs while a video plan plays. Inside a Watch Party room it is
off, and the room's own lower-quality offer applies.

Its mode comes from `playback.buffering_quality_mode`:

| Mode | On Auto | On a quality the viewer picked |
| --- | --- | --- |
| `always` (the default) | Lower at once, then offer an undo | Ask |
| `ask` | Ask | Ask |
| `never` | Detect and report stalls only | Detect and report stalls only |

A quality the viewer picked always gets a prompt first, because lowering it
silently would override an explicit choice.

Trigger evaluation follows the media event clock:

- Evaluate when a stall ends (`playing`) and when an in-progress stall reaches
  the long-stall threshold. Starting a wait does not evaluate old stall history.
- Once a long-stall timer has fired, keep watching the stall. Re-evaluate when a
  time-based suppression expires if a stall is still in progress; expiry while
  playing does not itself trigger an offer.
- A suppressed trigger (answered, cooldown, rearm, or re-offer) leaves stall
  history intact. A prompt timeout or rearm clears history, with startup grace
  already over, and times any in-progress stall again from that instant.
- Back on an undo pill only hides it. It does not answer the title.
- The no-lower-rung notice is once per title. A lowering whose adopted plan is
  no lower shows the failure notice on each such adoption, then starts its
  cooldown.

The controller acts only when the trigger fires and the title is not answered
at this quality. If there is no lower rung, the client instead shows the
display-only notice **"Can't lower quality for this video"**, once per title.

Both pills use `PROMPT_SECONDS = 15`, measured in wall-clock time.

### Lower automatically

| Event | Outcome |
| --- | --- |
| Trigger fires on Auto | Send a `quality_change` replan with `quality_preference: "auto"`, `bandwidth_estimate_kbps` set to the step-down estimate, and `bandwidth_estimate_source: "observed_rebuffer"`. Nothing shows yet. |
| The adopted plan is lower | Show the undo pill: the caption **"Quality lowered to stop buffering"** over the action **"Restore quality"**, with its timer running. Focus-driven platforms move focus to **Restore quality**, unless the viewer is mid-scrub or has a menu open (the intro-skip rule). |
| **Restore quality** | Send a `quality_change` replan with `quality_preference: "auto"`, no estimate, and `learned_limit: "clear"`. The title is answered at the restored quality. |
| The undo timer runs out, or Back | The pill hides, and the lower plan keeps playing. Back is consumed, so a second Back behaves normally. |
| The lower plan keeps buffering | It is a new plan, with its own grace. Its trigger lowers again from its own delivered bitrate. |

### Ask first

The prompt has a title, **"Playback keeps buffering"**, a primary action,
**"Lower quality"**, and a secondary action, **"Keep quality"**.

| Event | Outcome |
| --- | --- |
| Trigger fires | Show the prompt with its timer running. Focus-driven platforms move focus to **Lower quality**, under the same rule as the undo pill. |
| **Lower quality** on Auto | Send the replan from the first row of *Lower automatically*. The prompt hides. |
| **Lower quality** on a quality the viewer picked | Send a `quality_change` replan with the lower rung's `label`. This applies to this playback only: the saved preference does not change, and no limit is recorded. |
| **Keep quality**, or Back | The prompt hides, and the title is answered at this quality. Back is consumed. |
| The timer runs out | The prompt hides. The title is not answered: stall history clears, and a new trigger can offer again, but no sooner than `REOFFER_MIN_MS = 120000` after the last offer. |
| The plan adopted after **Lower quality** is lower | Show the display-only notice **"Quality lowered to stop buffering"**. |

### In every mode

| Event | Outcome |
| --- | --- |
| Pause while a pill shows | The timer freezes, and resumes on play. |
| The viewer changes quality, or a replan the controller did not send is adopted | Any pill hides and stall history clears. A quality change also clears the answer. |
| The plan adopted after a lowering is no lower, or carries `quality_reduction_unavailable` | Show **"Can't lower quality for this video"**. Leave this plan alone for `NO_LOWER_COOLDOWN_MS = 300000`. |
| The replan fails, or its plan is never adopted | Keep playing the current plan. Watch it again after `REARM_AFTER_UNADOPTED_MS = 60000`. |

Presentation rules:

- At most one actionable pill shows at a time. If the intro-skip or Next Up pill
  is showing, a buffering pill waits until that pill hides, then shows if the
  plan has not changed. An automatic lowering does not wait; only its undo pill
  does.
- Select and Back are handled at the player root, as for intro skip.
- The pills use the platform's notice position. A caption and its action, or
  two actions, are separate elements, never one phrase.

### The setting

`playback.buffering_quality_mode` joins the settings manifest in phase 1:

- Label: "Lower quality when buffering". Description: "What Silo does on Auto
  when playback keeps buffering. Silo always asks before lowering a quality you
  picked."
- Values: `never` ("Never"), `ask` ("Ask first"), `always` ("Automatically").
- Scopes: `profile` and `profile_device`, the same as `playback.intro_skip_mode`.
- Default: `always`.

The existing **Maximum bitrate** setting (`playback.max_bitrate_kbps`) remains
the way to cap Auto by hand. It reaches the server as `bandwidth_cap_kbps`, and
a learned limit never raises it.

## Learned limit (server)

A limit is recorded when a start or replan carries
`bandwidth_estimate_source: "observed_rebuffer"`.

- **Key**: user, device (`X-Silo-Device-Id`), and connection. The server derives
  the connection from what it already resolves for `streamlocation`:
  - `local` for a private or loopback client address;
  - `provider:<name>` for a network-access provider path;
  - otherwise `remote:` plus a keyed hash of the public address (the IPv4
    address, or the IPv6 /64).

  Clients need no network identity, so no Wi-Fi name and no location
  permission. A request without a device ID records and applies no limit.
- **Value**: the estimate from that request. A later lowering on the same key
  replaces it, and it is lower by construction, because Auto was already
  planning under the old limit.
- **Lifetime**: `LEARNED_LIMIT_TTL = 7 days` from the most recent observation. A
  changed home IP address simply starts a new key. That fails toward the source,
  and a new lowering follows.
- **Use**: Auto on a matching key plans with `min(request estimate, limit)`, or
  with the limit alone when the request has no estimate. Fixed rungs ignore it,
  as they ignore estimates today. The admin bitrate cap and
  `bandwidth_cap_kbps` still apply on top.
- **Request control**: start and replan gain an optional `learned_limit` field.
  Absent, the limit applies. `ignore` plans without it and leaves it stored;
  clients in `never` mode send this on every request, so a limit learned by
  another profile on the same device does not reach them. `clear` deletes the
  limit for this key, then plans without it; **Restore quality** sends this.
- **Visibility**: a plan the limit lowered carries
  `playback_plan.bandwidth_limit = {kbps, source: "observed_rebuffer", expires_at}`.
  The quality menu labels Auto as **"Auto · limited after buffering"**. The
  limit also appears in Settings, with a reset action (see below).
- **Cleared by**: Settings → Reset, **Restore quality**, the admin, or expiry.

The web's 30-minute browser-local observed limit is retired once the web sends
`observed_rebuffer`, so all clients share one store.

### Auto under a budget must go lower or say so

When an `observed_rebuffer` estimate or a learned limit puts the source over
budget, Auto tries these in order:

1. a lower-bitrate version of the same edition that fits and plays without
   video encoding, using the media-version fallback order the menu already uses;
2. a transcode that fits;
3. the current route with `quality_reduction_unavailable`. This is the existing
   warning, and it now means "nothing lower exists", which the client reports.

A version comes first because it is an original encode and costs the server
nothing. A limit that only returned the source again would make every later
title on that connection stall and lower again.

Under this rule, the incident replans at a 44 Mbps estimate, a budget of about
35 Mbps, and plays the item's 31.6 Mbps 1080p version.

## Probe on Android and Apple

These clients adopt the web policy as it stands:

- **Probe**: 40 MB, timed over a 2-second window from the first byte.
- **When**: in the background a few seconds after sign-in or a return to the
  foreground. Not while media plays or starts, and not on a metered or cellular
  link.
- **Cache**: 30 minutes. A network change discards the cached result.
- **Sending**: the result goes out as `bandwidth_estimate_kbps` with
  `bandwidth_estimate_source: "probe"`.

Android removes its "send no estimate" rule, along with the
`UNSENT_NETWORK_EVIDENCE` exemption in `PlaybackProtocolV3ConformanceTest`.
Media3's bandwidth meter is still never sent.

The probe has to measure the path the media takes. Today it measures the path
to the API server, while media served from a remote node can take another
route. The server should serve the probe wherever it serves this client's
media, and the capability response should name that URL. On a single-host
server, it stays the API host.

## Reporting and display

### Probe results

`POST /api/v2/playback/bandwidth-probe/results` takes
`{kbps, bytes, duration_ms, transport}`, where `transport` is `wifi`,
`ethernet`, `cellular` or `other`, and returns 204. The server keeps the latest
result per learned-limit key.

### `GET /api/v2/playback/connection`

Returns the state for the calling device on the connection it is using now.
Phase 1 returns `connection` and `learned_limit`; phase 2 adds `probe` and
`auto_budget_kbps`:

```json
{
  "connection": "remote",
  "probe": { "kbps": 48000, "measured_at": "2026-10-17T19:02:11Z", "transport": "wifi" },
  "learned_limit": { "kbps": 44100, "observed_at": "2026-10-17T16:21:40Z", "expires_at": "2026-10-24T16:21:40Z" },
  "auto_budget_kbps": 35280
}
```

`auto_budget_kbps` is computed by the server: 80% of the lower of the probe
result and the learned limit, then capped by the admin bitrate cap. Clients
display it and never compute it. `DELETE /api/v2/playback/connection/learned-limit`
resets the limit.

### Settings

Phase 1 adds to every client's Playback settings:

- **Lower quality when buffering**: Never, Ask first or Automatically.
- When a limit exists on this connection: "Limited to 44 Mbps after buffering
  on Oct 17", with a **Reset** action.

Phase 2 adds a Connection group:

- **Connection speed**: "48 Mbps · measured 3 minutes ago on Wi-Fi", with a
  **Test speed** action. The test runs the probe in the foreground, reports the
  result, and refreshes the row. A `429` shows "Try again in N seconds".
- "Auto plays videos up to 35 Mbps without converting them," from
  `auto_budget_kbps`.
- On cellular, the row says that speed tests are skipped on cellular, and the
  cellular quality settings apply.

### Admin

`GET /api/v2/admin/devices/{user_id}/{device_id}` gains `connections`: for each
key, the connection label, the latest probe, the learned limit, and the latest
plan summary. The web admin device page shows them, with **Clear limit**.

Rows are deleted 30 days after their last update and when the device is
deleted. Only the hash of a public address is stored. Admins can already see
each account's client IPs under account activity, so this adds connection
speed, not location.

## Telemetry

The server first advertises the client feature `playback_plan_summary_v1`. Once
it does, clients send a new route event, `plan_summary`, when a plan stops being
the playing plan, whether it was replaced, stopped or failed.

The event adds these keys to the diagnostics allowlist:

| Key | Value |
| --- | --- |
| `played_ms` | Milliseconds the plan was playing |
| `stall_count` | Counted stalls |
| `stall_total_ms` | Total stall time, startup grace excluded |
| `stall_longest_ms` | The longest single stall |
| `delivered_kbps` | The plan's delivered bitrate |
| `buffering_mode` | `never`, `ask` or `always` |
| `buffering_action` | The last action on this plan: `none`, `ignored` (a trigger in `never`), `lowered` (automatically), `restored`, `accepted`, `kept`, `timed_out` or `cant_lower` |
| `bandwidth_estimate_kbps` | Already on the allowlist: the estimate the plan was requested with |

The server exports:

- `silo_playback_stall_ratio`, a histogram of stall time over played time,
  labelled by client family and delivery;
- `silo_playback_buffering_actions_total{mode, action}`;
- the latest summary per learned-limit key, for the admin view.

### Checking the default

`always` ships as the default, so telemetry checks it rather than gating it.
After 28 days, and at least 200 automatic lowerings across at least three client
families, either of these sends the thresholds back for tuning, or the default
back to `ask`:

- More than 15% of automatic lowerings end in `restored`.
- Fewer than 90% of lowered plans play 10 minutes without another trigger.

Changing the default is a manifest change. Profiles that chose a mode keep it.

## Wire contract

These names are fixed, so the server, web, Android and Apple work can proceed
in parallel:

| Item | Contract |
| --- | --- |
| Feature | The server advertises `buffering_quality_v1` in the `/api/v2` capability `features` and native v2 start/replan response `server_features`. A client that sees it adds `buffering_quality_v1` to `client_features` on start and replan, and only then sends the fields below, because the server rejects unknown fields. The server records and applies learned limits only on requests that carry the feature. Against a server without it, clients still run the controller and send the same replans without the new fields; nothing is learned, and the limit row in Settings stays hidden. |
| `bandwidth_estimate_source` | Start and replan, optional: `probe` or `observed_rebuffer`. Valid only alongside `bandwidth_estimate_kbps`; otherwise the request is a `400`. |
| `learned_limit` | Start and replan, optional: `ignore` or `clear`. |
| `playback_plan.bandwidth_limit` | Optional object: `kbps` (integer), `source` (`"observed_rebuffer"`), `expires_at` (RFC 3339). Present only when the learned limit lowered the plan. |
| Connection endpoints | `GET /api/v2/playback/connection`, and `DELETE /api/v2/playback/connection/learned-limit` returning 204. Both exist when the feature is advertised. They use normal authenticated profile-scoped headers and `X-Silo-Device-Id`; no installation ID or body is required. |
| Setting | `playback.buffering_quality_mode`, introduced in the next manifest revision (21 at the time of writing). |
| Telemetry | Feature `playback_plan_summary_v1`, route event `plan_summary`, and the allowlist keys under *Telemetry*. |
| Phase 2 | The server advertises `connection_speed_v1` and adds `bandwidth_probe_url` to the capability response: the probe on the path this client's media takes. With it, clients probe that URL, report to `POST /api/v2/playback/bandwidth-probe/results`, and read `probe` and `auto_budget_kbps` from the connection endpoint. |

## Conformance

The tables above are the test oracle, and
[`schemas/buffering-quality/v1/vectors.json`](schemas/buffering-quality/v1/vectors.json)
encodes them. Each case is a list of timed events on one clock, with the
complete list of outputs it must produce. Events are `plan_adopted` (carrying
the plan's delivered bitrate, quality, menu and warnings), `playing`, `waiting`
(optionally with a `seek`, `pause` or `reload` cause), `pause`, `seek`,
`transport_error_start`/`end`, `replan_failed`, and the pill answers
`select_lower`, `select_keep`, `select_restore` and `back`. Timeouts come from
the clock. Outputs are `replan` with its body, `prompt_shown`, `undo_shown`, and
`notice` with its copy key. Some cases also give the `buffering_action` each
plan's `plan_summary` must report.

Each client vendors the file unchanged and replays it in unit tests against a
fake clock, in the same way that `IntroAutoSkipControllerTest` and
`IntroSkipPromptTests` guard intro skip. A case that seems to contradict this
document is a spec bug: fix both here, not one client's copy.

The cases, in brief. Unless a case says otherwise, the mode is `always`, plan
`p1` is adopted at 0 with its first frame at 0, the quality is Auto, the
delivered bitrate is 63000, and the menu offers rungs from 10000 down. A "long
stall" is a wait from 125000 that reaches 6000 ms.

| Case | Events | Expected |
| --- | --- | --- |
| `long_stall` | waiting 125000 | `replan {operation: quality_change, quality_preference: auto, bandwidth_estimate_kbps: 44100, bandwidth_estimate_source: observed_rebuffer}` at 131000 |
| `two_short_stalls` | waiting 30000, playing 31500, waiting 90000, playing 91200 | the same replan at 91200 |
| `stalls_too_far_apart` | waiting 30000, playing 31500, waiting 160000, playing 161500 | nothing |
| `sub_second_stalls` | three 800 ms stalls within 60 s | nothing |
| `grace` | waiting 3000, playing 8000, waiting 40000, playing 41500 | nothing: the first stall falls inside the grace |
| `seek` | two 3000 ms waits caused by seeks | nothing |
| `slow_start` | no frame, waiting 0 | replan at 16000 |
| `outage` | transport_error_start 29000, waiting 30000, transport_error_end 44000, playing 45000 | nothing |
| `undo` | long stall, then `p2` adopted at 134000 delivering 31600 | replan at 131000, `undo_shown` at 134000 |
| `restore` | `undo`, select_restore at 140000, `p3` adopted, then a long stall on `p3` | `replan {operation: quality_change, quality_preference: auto, learned_limit: clear}` with no estimate, then nothing |
| `lower_again` | `undo`, the undo pill times out, then a long stall on `p2` | a second replan with `bandwidth_estimate_kbps: 22120` |
| `ask_on_auto` | mode `ask`, long stall, select_lower, then a lower plan | `prompt_shown` at 131000, the Auto replan, then `notice lowered` |
| `picked_quality` | quality `1080p-high` (10000), long stall, select_lower | `prompt_shown`, then `replan {operation: quality_change, quality_preference: "1080p-medium"}` with no estimate |
| `never` | mode `never`, long stall | nothing; `p1` reports `ignored` |
| `no_lower_rung` | the menu has only the original; two long stalls | `notice cant_lower` once |
| `keep` | mode `ask`, long stall, select_keep, another long stall | one prompt only |
| `timeout` | mode `ask`, long stall, no answer, then long stalls triggering at 191000 and 261000 | prompts at 131000 and 261000; 191000 is too soon after the first offer |
| `no_lower_result` | long stall, then `p2` adopted at 63000 with `quality_reduction_unavailable`, another long stall | the replan, `notice cant_lower`, then nothing during the cooldown |
| `unadopted_replan` | long stall, replan_failed, a long stall at 150000, another at 200000 | replans at 131000 and 206000 |

Additional conformance cases cover `suppressed_history_retained` (a later
`playing` event can evaluate retained history), `repeated_no_lower_result`
(each unsuccessful adoption shows its notice), `back_hides_undo_without_answering`,
and `cooldown_expires_during_stall` (an active long stall is evaluated at expiry).

## Phases

| Phase | `silo-server` | `silo-android` | `silo-apple` |
| --- | --- | --- | --- |
| 1. Detect and lower | Add `bandwidth_estimate_source` and `learned_limit` to start and replan (optional fields, with schema and fixtures). Add the learned-limit store and its use in Auto, plus `bandwidth_limit` on the plan. Make Auto under a budget go lower or say so. Add `GET /api/v2/playback/connection` (connection and learned limit) and the reset endpoint. Add `playback.buffering_quality_mode` to the manifest with the default `always`, and regenerate bindings. Add `plan_summary` and its allowlist keys. Keep the vectors file in step with any spec change. In web, keep the automatic downshift as `always`, add the undo pill, the prompt for a picked quality, and the `ask` and `never` modes, send the estimate source and `learned_limit`, drop the local observed limit, and add the setting and limit rows. | A `BufferingQualityController` in `shared/` drives phone and TV, following the `IntroAutoSkipController` pattern. One Media3 adapter in `android-shared` feeds it. `PlayerNoticeOverlay` and `TvPlayerNoticeOverlay` gain actions, with `TvIntroAutoSkipBanner`'s focus rules. Revisit the startup detector's same-plan reopen: a slow plan that is still receiving data belongs to this controller. Regenerate `SettingKeys.kt`, and add the setting and limit rows to `PlaybackSettings.kt` and `TvSettingsScreen.kt`. | A shared controller like `IntroSkipPrompt`, fed by `AVPlayer` `timeControlStatus` and wait reasons, `AVPlayerItem` stall notifications, and AetherEngine buffering events. `PlayerNoticeOverlay` gains actions; on tvOS and macOS, focus follows the intro-skip pills. Regenerate `SettingKeys.generated.swift`, and add the setting and limit rows to `PlaybackSettingsView` and `TVPlaybackSettingsView`. |
| 2. Measure and show | Serve the probe on the media path and advertise its URL. Add the probe-results endpoint, `probe` and `auto_budget_kbps` on the connection endpoint, and the device `connections` field. Add the web Connection group and the admin device view. | Probe in `android-shared`, skipped when `PlaybackNetworkMonitor` reports metered. Send the estimate and its source. Add the Connection group. Update `docs/playback/01-media3-only-player-architecture.md` §3.5–3.6. | Probe with `URLSession`, skipped when `NWPathMonitor` reports metered. Add the Connection group. |
| 3. Check the default | Add the dashboards, and run the 28-day check above. | None planned. | None planned. |

Phase 1 alone fixes the incident.

## Open questions

- **Peak bitrate.** Auto compares the budget against the average bitrate,
  because that is all the scanner stores. A remux that averages 63 Mbps can
  peak far higher. Storing the highest 30-second average at scan time, and
  fitting Auto against it, would prevent many of these stalls before they
  happen. The cost to the scan is unmeasured.
- **Self-clearing limits.** Should a plan that delivers above the learned limit
  and plays 20 minutes without a counted stall clear the limit? That plan would
  be a fixed rung the viewer picked. It needs `plan_summary` data first.
- **Viewers who keep lowering a picked quality.** After several accepted prompts
  on a fixed quality, should Settings suggest Auto?
- **Announcing a limit at start.** This design marks a limited Auto only in the
  quality menu and in Settings. A one-time notice at start would be clearer, but
  it adds another pill.
