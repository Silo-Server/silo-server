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
4. One connection speed test on every client, with the server's existing 80%
   margin and results reported to the server. A quick automatic test stops once
   it proves the fastest rate the server would send, and runs only when the
   server holds no current reading for this device and network. Viewers see
   their connection speed in Settings and can run an accurate test there;
   admins see it on the device page.

## Decisions

Settled on 2026-10-17:

| Question | Decision |
| --- | --- |
| Lower automatically, or ask? | Lower automatically on Auto, from the first release. Viewers who find it unreliable can switch the setting to **Ask first** or **Never**, reset a learned limit, or cap quality with **Maximum bitrate**. |
| How long a learned limit lasts | Longer than the web's 30 minutes: 7 days from the last observation (proposed below). |
| Margin on a probe estimate | Keep the server's 80%. |
| Report probe results to the server? | Yes. Admins can see per-device connection speeds. |
| What do later replans send after a lowering? | The lowering's step-down estimate, without a source, until **Restore quality**, a quality change or a new start (see *Later replans after a lowering*). |

Settled on 2026-10-18, for the connection speed test:

| Question | Decision |
| --- | --- |
| How many connections? | One, as playback uses on every platform. |
| How far does the automatic test go? | To a target the server publishes: the highest bitrate it would plan for this viewer, plus 25%. It stops as soon as a window after ramp-up reaches the target. |
| Where does an accurate reading come from? | **Test speed** only: up to 10 seconds, the rate held over the final 3. |
| How often do clients test? | Only when the server holds no current reading for this device and network, or the viewer taps **Test speed**. A reading lasts 7 days by default. No periodic test. |
| How many tests at once? | 4 per server, refused beyond that with `429` and `Retry-After`. |
| Can an admin turn automatic tests off? | Yes, with a server setting that also sets the reading lifetime. |
| Does playback count as a measurement? | Yes: the throughput a plan saw is a lower bound the server keeps like a test result. |

## Motivation

On 2026-10-17 an onn 4K Pro (Android TV) buffered all the way through a
feature film on Auto. The file was a 63 Mbps (average) 4K Dolby Vision
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

- **Plan**: one adoption of a playback plan. Every adopted replan starts a new
  plan, even when the server returns the same `plan_id`: that ID is derived
  from the attempt, files, delivery and recipe, so it can repeat. Clients key
  controller state and request correlation on a local adoption identity, and
  send the server `plan_id` in `plan_summary` route events. In the vectors,
  `plan_id` labels are adoption identities. An in-place update, such as a
  subtitle that became ready, is not a new adoption.
- **Delivered bitrate**: `effective_recipe.bitrate_kbps`, else
  `source.bitrate_kbps`.
- **Stall**: the player has run out of data while it should be playing. These
  never count as stalls:
  - buffering caused by a seek, a pause, the end of the media, or a source
    reload. Buffering after a seek belongs to the seek until playback next
    runs, including a seek made while paused. Until a plan first runs, a seek,
    including the player applying the start position, does not end the
    start's wait: buffering after it still belongs to the start. A wait after
    resuming from a pause is a stall unless a seek or reload happened since
    playback last ran. A source reload begins when the client replaces or reopens the playing
    source, not when it sends the replan that leads to one; until then the
    current plan is still playing.
  - playback the platform stops when the app leaves the foreground. If the
    platform stops or tears down playback then, a stall in progress ends
    uncounted, as for a pause. If playback continues in the background, its
    stalls count as usual.
  - buffering while media requests are failing, or while the device is offline.
    That is an outage, and the reconnect path owns it. Media requests are the
    plan's playlist, segment and range requests; a failed API request is not a
    media failure. Subtitle requests are not media requests: a subtitle
    failure doesn't start an outage, and a subtitle success doesn't end one. A
    media request is failing when it errors, cannot connect, or receives no
    data for the client's idle or first-byte timeout. A request that is still
    receiving data is not failing, even when it runs past a whole-request time
    limit: a slow link is what the controller exists to catch. An outage
    ends when the plan's media data arrives again (a segment or range request
    receives data, or the buffered position advances) or when playback runs.
    A playlist, subtitle or API request that succeeds does not end it on its
    own. No time limit ends an outage. While an outage lasts the controller
    does not evaluate; the next `playing` after it does.
  - a renderer wait: one that begins with more than
    `RENDERER_WAIT_BUFFER_MS = 1000` of media buffered past the playhead. The
    decoder or renderer is behind, as when a weak device drops frames to the
    next keyframe, and the network is not. If the media buffered past the
    playhead falls to `RENDERER_WAIT_BUFFER_MS` or less before playback
    resumes, the stall begins at that moment.
  - stall time inside the startup grace: the first `STARTUP_GRACE_MS = 10000` of
    a plan, counted from whichever comes first after adoption: when its playback
    first runs, or a wait before that. A plan opened paused starts its grace when
    the viewer plays it. A first frame that follows a wait does not restart the
    grace. A stall that begins in the grace counts from the end of the grace.
- **Counted stall**: a stall that lasted at least `STALL_MIN_MS = 1000` when
  playback resumed.
- **Trigger**: either of these, using the web constants:
  - `STALLS_BEFORE_TRIGGER = 2` counted stalls that ended within the last
    `STALL_WINDOW_MS = 120000` when the trigger is evaluated;
  - one stall reaching `LONG_STALL_MS = 6000`, while it is still in progress or
    within the window after it ends.
- **Lower rung**: the highest `available_qualities` entry whose bitrate is below
  the bitrate of the active rung. When the active quality is Auto, or matches no
  `available_qualities` label (a saved plain resolution such as `1080p`), the
  comparison is against the delivered bitrate. This is the web's
  `lowerQualityOption`.
- **Step-down estimate**: `floor(delivered bitrate × 7 / 10)`, in exact integer
  arithmetic: 22120 gives 15484, while multiplying by the floating-point `0.7`
  gives 15483. Auto plans for 80% of an estimate, so the next stream targets
  about 56% of the one that stalled. On Auto, a missing delivered bitrate or an
  estimate below 100 kbps means there is no lower rung: show `cant_lower` once
  per title, report `cant_lower`, and send no prompt or replan. In `never` mode
  the client only detects and reports (`ignored`).
- **Answered**: the viewer has decided about buffering for this title at this
  quality, by **Keep quality** or **Restore quality**. An answered title is not
  lowered or prompted again until the viewer changes quality. The answer, the
  once-per-title notice and the re-offer wait belong to the title: a new
  playback attempt of the same title that the viewer did not choose (a renewal
  after a server restart, Retry, failure recovery) keeps them.
- **Quality change**: a pick from the quality menu, a switch between Wi-Fi and
  cellular quality, or a version switch the viewer chooses. A pick the player
  refuses is not one, and neither is picking the quality already playing. A
  network change counts only when it changes the quality preference or cap a
  replan sends.
- **Clock**: stalls, pill timers and summary durations use elapsed real time
  from a monotonic clock. "Wall-clock time" below means real time rather than
  media time, not the system clock.

## Behaviour

The controller runs while a video plan plays. Inside a Watch Party room it does
not act: no trigger lowers, prompts or reports `ignored`, and no pill or notice
shows; the room's own lower-quality offer applies. Stall tracking and plan
summaries continue, with `buffering_action: none`. Joining or leaving a room is
not a plan boundary. On leaving, the controller resumes on the playing plan
with fresh stall history and the grace over.

Its mode comes from `playback.buffering_quality_mode`:

| Mode | On Auto | On a quality the viewer picked |
| --- | --- | --- |
| `always` (the default) | Lower at once, then offer an undo | Ask |
| `ask` | Ask | Ask |
| `never` | Detect and report stalls only | Detect and report stalls only |

A quality the viewer picked always gets a prompt first, because lowering it
silently would override an explicit choice.

Trigger evaluation follows the media events:

- Evaluate when a stall ends (`playing`) and when a stall in progress reaches
  `LONG_STALL_MS`. A new wait does not re-evaluate old stall history.
- After the long-stall timer fires, keep watching the stall. When a cooldown,
  the re-offer wait or the undo pill ends while that stall is still in
  progress, evaluate again at once. The undo pill ends by its timer or by
  Back; both count. A suppression that ends during a shorter wait, or while
  playing, triggers nothing by itself.
- A suppressed trigger leaves stall history intact. A trigger is suppressed
  while the title is answered, during a cooldown, before a rearm or re-offer is
  due, and while a buffering prompt or undo pill is visible or waiting behind a
  higher-priority pill. A prompt timeout or a rearm clears history, with the
  startup grace already over, and times a stall in progress again from that
  moment.
- Back on an undo pill only hides it; it does not answer the title.
- "Can't lower quality for this video" for a missing lower rung shows only if
  it has not shown for this title yet, whichever rule showed it. A lowering
  whose adopted plan is no lower shows it on every such adoption, then starts
  its cooldown.

The controller acts only when the trigger fires and the title is not answered
at this quality. If there is no lower rung, the client instead shows the
display-only notice **"Can't lower quality for this video"**, once per title.

Both pills use `PROMPT_SECONDS = 15`, measured in wall-clock time.

Display-only notices ("Quality lowered to stop buffering", "Can't lower
quality for this video") use the platform's notice style with an information
tone and show for 5 seconds. A notice waits while the player cannot show it or
a higher-priority notice holds the slot.

### Lower automatically

| Event | Outcome |
| --- | --- |
| Trigger fires on Auto | Send a `quality_change` replan with `quality_preference: "auto"`, `bandwidth_estimate_kbps` set to the step-down estimate, and `bandwidth_estimate_source: "observed_rebuffer"`. Nothing shows yet. |
| The adopted plan is lower | Show the undo pill: the caption **"Quality lowered to stop buffering"** over the action **"Restore quality"**, with its timer running. Focus-driven platforms move focus to **Restore quality**, unless the viewer is mid-scrub or has a menu open (the intro-skip rule). A plan is lower when its delivered bitrate is below the delivered bitrate of the plan the lowering left. A plan with no delivered bitrate counts as lower unless it carries `quality_reduction_unavailable`. |
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
| The timer runs out | The prompt hides. The title is not answered: stall history clears. A new prompt can show no sooner than `REOFFER_MIN_MS = 120000` after this prompt first appeared on screen. Only a timeout starts this wait; **Lower quality** and **Keep quality** do not. A quality change or a new title ends it; a new attempt of the same title does not. |
| The plan adopted after **Lower quality** is lower | Show the display-only notice **"Quality lowered to stop buffering"**. |

### In every mode

| Event | Outcome |
| --- | --- |
| Pause while a pill shows | The timer freezes, and resumes on play. |
| The viewer changes quality (see *Quality change*), or a replan the controller did not send is adopted | Any pill hides and stall history clears. An adoption the controller did not request also drops a pending lowering, with no notice. A quality change also clears the answer and ends the re-offer wait. |
| The plan adopted after a lowering is no lower, or carries `quality_reduction_unavailable` | Show **"Can't lower quality for this video"**. Leave this plan alone for `NO_LOWER_COOLDOWN_MS = 300000`. |
| The replan fails, or its plan is never adopted | Keep playing the current plan. Watch it again after `REARM_AFTER_UNADOPTED_MS = 60000`. Rearming starts fresh stall history without startup grace; an in-progress stall is timed from the rearm. If the player cannot send the replan now, because another replan is in flight, the replan fails: the client does not queue it. |

Presentation rules:

- At most one actionable pill shows at a time. Intro skip, other segment skip
  (including recap and credits), and Next Up take priority over buffering pills.
  If one is showing, a new buffering pill waits with its full timer. If one
  arrives while a buffering pill is visible, hide the buffering pill and freeze
  its remaining timer. When the other pill hides, show the buffering pill and
  resume that remaining timer, only if the plan has not changed. A plan change
  or a manual quality change discards the waiting or hidden pill. An automatic
  lowering does not wait; only its undo pill does.
- A player that cannot show its pills hides them the same way: when it is
  minimized, in picture in picture, in the background or a hidden browser tab,
  playing in a native fullscreen view that draws no app overlays, or covered by
  a menu or overlay that hides the pill. A pill waits with its remaining timer
  and shows when it can be seen again. A platform whose menus leave the pill
  visible keeps it showing.
- Select and Back are handled at the player root, as for intro skip. They reach
  a pill only while the viewer can see it; an open menu or a full-screen overlay
  takes Back first.
- The pills use the platform's notice position. A caption and its action, or
  two actions, are separate elements, never one phrase.

### Later replans after a lowering

A lowering holds only while the client's own replans keep asking for it. An
Auto replan without an estimate returns to the source unless a learned limit
applies. None applies on a server without `buffering_quality_v1`, or on a
request without a device ID or with `learned_limit: "ignore"`. The source then
stalls and the controller lowers again, in a loop.

So the client keeps the step-down estimate of its latest Auto lowering in
memory, from the moment it sends that replan until the playback attempt ends.
A lowering refused before it is sent is not kept.

- Every later Auto replan the client sends for its own reasons, such as a track
  change, seek reanchor, seek failure recovery, failure recovery or output
  change, carries it as `bandwidth_estimate_kbps` without
  `bandwidth_estimate_source`. It repeats an earlier observation; it is not a
  new one.
- The next lowering replaces it.
- **Restore quality**, a quality change the controller did not send (see
  *Quality change*), a new start, and a new attempt of the same title each
  discard it. A new attempt discards nothing else. The **Restore quality**
  replan sends no estimate, and `learned_limit: "clear"` when the server
  advertises `buffering_quality_v1`. A **Restore quality** that cannot be sent
  at once, because another replan is in flight, is sent after it, or its
  `learned_limit: "clear"` rides on the request that replaces it.
- A retry of the lowering request itself resends its original body, source
  included, so the server can replay its decision.
- The kept estimate is never written to storage, and it does not replace the
  server's learned limit.
- Replans never send the speed-test reading (see *When to test*). Without a
  kept estimate, a replan the client sends for its own reasons carries no
  estimate, so after a start the reading limited, such a replan can return Auto
  to the source; repeated rebuffering then lowers it.

### The setting

`playback.buffering_quality_mode` joins the settings manifest in phase 1:

- Label: "Lower quality when buffering". Description: "What Silo does on Auto
  when playback keeps buffering. Silo always asks before lowering a quality you
  picked."
- Values: `never` ("Never"), `ask` ("Ask first"), `always` ("Automatically").
- Scopes: `profile` and `profile_device`, the same as `playback.intro_skip_mode`.
- Default: `always`.

Until the profile's mode loads, a client uses the last mode it saw for this
profile on this device, and the default only when it has seen none. A start
does not wait for the settings read. Against a server whose manifest lacks the
setting, a client hides the row and runs `always`; nothing device-local stands
in for the setting. Native clients, which talk to servers of any version,
detect the setting by the key's presence in the server's settings manifest or
resolved settings, not by comparing a revision number. The web app ships inside
the server it talks to, so it keeps the revision check every web setting uses.

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
  and a new lowering follows. Only a request that carries `observed_rebuffer` is
  an observation. A replan that repeats the kept estimate without a source, and
  any seek replan, records nothing and does not extend the 7 days.
- **Use**: Auto on a matching key plans with `min(request estimate, limit)`, or
  with the limit alone when the request has no estimate. Fixed rungs ignore it,
  as they ignore estimates today. The admin bitrate cap and
  `bandwidth_cap_kbps` still apply on top.
- **Request control**: start and replan gain an optional `learned_limit` field.
  Absent, the limit applies. `ignore` plans without it and leaves it stored;
  clients in `never` mode send this on every request, so a limit learned by
  another profile on the same device does not reach them. `clear` deletes the
  limit for this key, then plans without it; **Restore quality** sends this. A
  request for playback on another device (Cast) sends `ignore` in every mode,
  and no estimate: the limit belongs to the sending device and its connection.
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

## Connection speed test

Every client runs the same test: web, Android and Apple. It measures the path
the media takes, over one connection, because playback uses one connection on
every platform. Media3 runs one loader per stream, hls.js loads one fragment at
a time, and Apple reads through one sequential `AVIOReader` pump or one Aether
relay request per AVPlayer request. A second connection would measure
throughput playback cannot use.

Android removes its "send no estimate" rule, along with the
`UNSENT_NETWORK_EVIDENCE` exemption in `PlaybackProtocolV3ConformanceTest`.
Media3's bandwidth meter is still never sent.

### Probe URL and target

With `connection_speed_v1`, the capability response carries:

| Field | Meaning |
| --- | --- |
| `bandwidth_probe_url` | The probe on the path this client's media takes, on the origin the capability request arrived on. When that is the server's local-network origin (a local route), clients send the probe with the saved server's credentials and trust, as they do for any request to that server. |
| `bandwidth_probe_target_kbps` | The rate a quick test has to prove. Absent when the server has no target, for example before any video is scanned. |
| `bandwidth_probe_automatic` | `false` when the admin turned automatic tests off. **Test speed** still works. |
| `bandwidth_probe_max_age_seconds` | How long a reading stays current. |

The target is the highest bitrate the server would plan for this viewer, plus
25%: `max(ceil(P × 5 / 4), 100)`, at most 1,000,000, the estimate whose 80%
budget carries P untouched, and never below the 100 kbps a probe result needs.
P is
the lower of the administrator bitrate cap for the caller's network, when one
applies, and the 99.9th percentile of the source bitrates of the video files
this profile can play. It is a percentile because one file with a bogus bitrate
would otherwise set the target. The server caches the percentile for each set
of libraries.

Two server settings control the schedule: `playback.bandwidth_probe_automatic`
(default `true`) and `playback.bandwidth_probe_max_age_days` (1 to 30, default
7, the lifetime of a learned limit).

### Measuring

The clock starts at the first byte of the response body, so request latency
does not count. Bytes count in the 500 ms window (`PROBE_WINDOW_MS`) in which
they arrive: window `k` covers `[500k, 500k + 500)` ms. A window's rate is
`floor(bytes × 8 / 500)` kbps. Window 0 is TCP ramp-up and never counts. A window
in which nothing arrived counts at 0, so a stalled transfer lowers the result;
clients close windows on their clock, not only when data arrives.

| | Quick test | Accurate test |
| --- | --- | --- |
| Runs | Automatically | Only from **Test speed** |
| Requests | `size=100000000` | `size=1000000000` |
| Stops | At the end of the first window after window 0 whose rate reaches the target; else 3,000 ms after the first byte, or when the transfer ends | 10,000 ms after the first byte, or when the transfer ends |
| Reports | The target, with `lower_bound: true`, when a window reached it; else the settled rate over the final 1,000 ms | The settled rate over the final 3,000 ms |

The settled rate uses the last complete windows after window 0 that fill the
span: 2 for the quick test, 6 for the accurate one, or every complete window
after window 0 when there are fewer. Its rate is `floor(bytes × 8 / (windows ×
500))`. A window cut short by the end of the transfer does not count.

- A link fast enough to deliver the whole probe before the first complete
  window after window 0 (100 MB in under a second, above about 800 Mbps) is
  measured over the whole transfer instead: `floor(bytes × 8 / ms)` from the
  first byte to the end of the transfer, when the client sees the body end. The
  ramp is inside that average, so it can only read low, and it is reported with
  `lower_bound: true`, or as the target when a quick test's average reaches it.
- Otherwise a test with no complete window after window 0, or with a result
  under 100 kbps, including a target under 100, measured nothing. So does a
  transfer that ends short of the bytes it asked for before a complete window
  after window 0.
- A transfer that fails before the test stops, because the connection drops or
  the body ends short of its `Content-Length`, measured nothing, whatever
  windows completed. `end` in `probe_cases` is a clean end of the body.
- A result above 1,000,000 kbps is reported as 1,000,000 with `lower_bound: true`.
  A client never keeps or sends a result of its own outside 100 to 1,000,000.
- Without a target, the quick test runs its full 3 seconds.
- A test whose first byte has not arrived within 10 seconds of sending the
  request, the redirect included, is abandoned.

The reported `bytes` and `duration_ms` are those of the windows the rate came
from. `probe_cases` in the vectors pin this arithmetic (see *Conformance*).

At a 400 Mbps line and a 115 Mbps target, the quick test stops after about one
second and 30 MB. On a link just under the target it runs the full 3 seconds,
about 45 MB, and slower links move less.

### When to test

A reading is what the server stores for this device on this connection: `probe`
and `playback_sample` from `GET /api/v2/playback/connection`. It is current while
it is younger than `bandwidth_probe_max_age_seconds`.

- A settled rate proves the link.
- A lower bound proves it only while it is at least the current
  `bandwidth_probe_target_kbps`. Lower bounds are a quick test that reached its
  target, or a playback sample. A larger library, a raised cap or a profile with
  more to play makes the test due again.
- Without a target, any current reading proves the link. The server then has
  no bitrates to measure against, and a failed target lookup must not send
  every client holding a lower bound into a test at once.
- A reading is not current until the client knows this server's
  `bandwidth_probe_max_age_seconds`, so a check reads the capability response
  with the connection.

Clients check on every sign-in, on a return to the foreground and after a
network change. A re-login to the same server, account and profile is a sign-in
too, and a result from before a sign-out is discarded. A check reads
`GET /api/v2/playback/connection` once, at once, as soon as the client is
signed in and online, so a playback start in the first seconds already sends
the reading; a client may read again just before a test. Only a test waits: a
few seconds, so the first screen can settle, and it runs only if automatic
tests are on and no current reading proves the link. A reinstalled app, or a
phone back on its usual Wi-Fi, finds the reading it left there. On the web, a
tab shown again less than a minute after it was hidden has not returned to the
foreground: it is still on the network it left, so it reads nothing and only
resumes a test the hide stopped.

A network change is a change of the interface or network in use, of metering,
of VPN state, of the local route a native client uses for this server, or a
return to connectivity after none. A client that cannot observe network changes
(a desktop browser) reads again when the browser reports it is back online.

- There is no periodic test. A test that fails or is refused waits for the next
  check. A connection read that fails changes nothing the client holds and runs
  no test; the next check reads again.
- A `429` from any test, **Test speed** included, holds automatic tests against
  the server that sent it until its `Retry-After`, across profile and account
  changes, for as long as the app runs. A missing or unreadable `Retry-After`
  counts as 30 seconds, and a value under 1 counts as 1.
- Metered means the system marks the link metered or expensive, or the viewer's
  data-saving mode is on, ignoring a VPN's own flag. Where a platform has a
  cellular quality pair, the same test chooses it. On a metered or cellular link
  a check may skip the read; nothing read there is sent.
- A client whose platform cannot report the link type treats the link as
  unmetered when it reads, sends a reading and runs **Test speed**. On a phone
  or tablet it runs no automatic test. The cellular Settings row and the
  cellular quality pair apply only where the platform reports the link type.
  That rule is for a link with no signal at all. The viewer's data-saving
  mode, where the platform reports it, is a metered signal of its own and
  still selects the cellular quality pair.
- Media is any playback in the app: streamed or downloaded video, audiobooks and
  theme music. A paused player counts only while it is still loading, and
  resuming it cancels a running test.
- No automatic test runs while media plays or starts, on a metered or cellular
  link, or while the app is in the background. On the web, that includes a
  hidden tab. A test that is running when one of these begins is cancelled and
  measured nothing. The quiet period after a start holds automatic tests only.
- **Test speed** runs the accurate test whenever no media plays or loads, even
  with automatic tests off or a current reading. It replaces a running automatic
  test and joins a running **Test speed**.
- A client may keep its own result for playback starts only if the server
  accepted it, or the POST failed for a transport reason. A result the server
  refused is never kept.
- A playback start sends the highest current reading as
  `bandwidth_estimate_kbps` with `bandwidth_estimate_source: "probe"`. This holds
  for as long as the reading is current; the 30-minute expiry is gone. A start
  sends a reading only to the server that holds it, taken on the route the
  start's media uses. On a metered or cellular link it sends none, and the
  cellular quality settings apply.
- Replans never send the reading. A replan the client sends for its own reasons
  carries the kept step-down estimate or no estimate (see *Later replans after a
  lowering*).
- Against a server without `connection_speed_v1`, a client may run the quick
  test without a target against `/api/v2/playback/bandwidth-probe`, and keep
  the result on the device for 7 days per network.

The server plans only from the estimate a request carries. It does not put its
stored reading in place of a missing one: only the client knows whether its link
is metered, and the kept-estimate rules stay with the client.

### Media path and limits

The probe measures the path the media takes. Media served from a remote node
can take another route than the API, so the server serves the probe wherever it
serves this client's media, and `bandwidth_probe_url` names that URL. On a
single-host server, it is the API host.

The advertised URL is stable and uses normal authentication. Fetching it can
redirect to a short-lived proxy URL that is valid only for probes; clients
follow the redirect. There is no media item yet, so the server picks the egress
that the direct-play routing policy would use for the caller's network or
provider path. When a proxy-only policy has no proxy that serves probes, the
endpoint returns an unavailable error instead of measuring the API path.

A probe sends at most 1,000,000,000 bytes. Each server runs at most 4 probes at
once, counting the API and each proxy separately. On each server, an account
may run 2 probes at once and draw 2,000,000,000 bytes per 10 minutes; bytes left
unread when the client closed early do not count. One accurate test is never
refused for its size, while a run of back-to-back tests soon is. Beyond any
limit, the probe answers `429` with `Retry-After`.

### Playback samples

A plan's `plan_summary` may carry `throughput_kbps`. It is the rate at which the
plan's long media requests received data, counted only while a request was
receiving, from its first byte to its last. Only this plan's main media
requests count (video and audio segments, or a progressive body), from its
adoption until it stops being the playing plan; not subtitles, images, probes
or other playback in the app. A request's receiving time starts at the first
byte of its body; a platform that sees only the response headers may start
there, which reads low.

- Only a request that spent at least 1,000 ms receiving counts, in bytes and in
  time. A short request on a warm connection, such as one HLS segment, arrives
  at the connection's burst rate, which can be twice what the link sustains;
  Android measured 2–17 MB segments at a median 262 Mbps on a path that tested
  at 98–141 Mbps.
- Time in which several counted requests were receiving at once counts once.
- A client sends it only when its counted requests add up to at least 1,000 ms
  of receiving and it is at least 100, capped at 1,000,000. The server ignores
  a sample under 100: like a probe under 100 kbps, it is not a measurement, and
  no start could send it as `bandwidth_estimate_kbps`.
- The server keeps the highest sample, and a sample at least the target proves
  the link, so an overstated one would stand in for a test the link would not
  pass.
- A summary sent at a later launch carries no sample. The server files a sample
  under the connection of the request that delivers it, so a replay could
  credit one network with another's rate.
- A client that cannot measure this omits it.
- Apple will measure it on Aether's upstream reader once AetherEngine reports
  it, and omits it until then. AVPlayer reads from Aether on 127.0.0.1, so its
  access log only sees that local hop.

The server keeps one sample per device and connection as `playback_sample`. A
new sample replaces it when it is higher, or when the kept one is older than the
reading lifetime. Like a quick test that reached its target, it is a lower
bound.

## Reporting and display

### Probe results

`POST /api/v2/playback/bandwidth-probe/results` takes
`{kbps, bytes, duration_ms, transport, lower_bound}`, where `transport` is
`wifi`, `ethernet`, `cellular` or `other`, and returns 204. `kbps` is 100 to
1,000,000, the bounds of `bandwidth_estimate_kbps`, so a kept reading is always
one a client can send. `lower_bound` is optional and `false` by default. The
server keeps the latest result per learned-limit key.

### `GET /api/v2/playback/connection`

Returns the state for the calling device on the connection it is using now.
Phase 1 returns `connection` and `learned_limit`; phase 2 adds `probe`,
`playback_sample` and `auto_budget_kbps`:

```json
{
  "connection": "remote",
  "probe": { "kbps": 48000, "lower_bound": false, "measured_at": "2026-10-17T19:02:11Z", "transport": "wifi" },
  "playback_sample": { "kbps": 51200, "measured_at": "2026-10-18T20:14:03Z" },
  "learned_limit": { "kbps": 44100, "observed_at": "2026-10-17T16:21:40Z", "expires_at": "2026-10-24T16:21:40Z" },
  "auto_budget_kbps": 35280
}
```

The server returns the latest probe and playback sample until row retention
removes them, current or not, and clients judge whether they are current (see
*When to test*).

`auto_budget_kbps` is computed by the server: 80% of the lower of the reading
and the learned limit, then capped by the admin bitrate cap. The reading is the
higher of the current probe and the current playback sample. Clients display
the budget and never compute it.

`DELETE /api/v2/playback/connection/learned-limit` resets the limit. Missing
probe, sample and budget fields mean that no measurement is available; an
administrator cap alone is not a speed measurement.

### Settings

Phase 1 adds to every client's Playback settings:

- **Lower quality when buffering**: Never, Ask first or Automatically.
- When a limit exists on this connection: "Limited to 44 Mbps after buffering
  on Oct 17", with a **Reset** action.

Phase 2 adds a Connection group:

- **Connection speed**: "48 Mbps · measured 3 minutes ago on Wi-Fi", or
  "Faster than 115 Mbps · measured 3 minutes ago on Wi-Fi" for a lower bound,
  with a **Test speed** action. Without a probe, the row shows a playback sample
  as a lower bound: "Faster than 51 Mbps · seen during playback 2 hours ago".
  **Test speed** runs the accurate test in the foreground, which takes up to 10
  seconds, reports the result and refreshes the row. A `429` shows "Try again in
  N seconds" ("1 second" at one) until N seconds have passed. If the report
  fails, the row says the test could not be saved and keeps showing the
  server's reading.
- "Auto plays videos up to 35 Mbps without converting them," from
  `auto_budget_kbps`.
- On cellular, the row says that speed tests are skipped on cellular, and the
  cellular quality settings apply. On a metered network that is not cellular it
  says "Speed tests are skipped on metered networks." The note replaces the
  speed, and **Test speed** and the budget line are hidden. The cellular note
  appears only where the platform reports the link type.

Settings shows rates in Mbps, rounded down: one decimal below 10 Mbps, whole
from 10 Mbps, and kbps below 1 Mbps, with no digit grouping. Rounding down
keeps a lower bound and the budget from overstating the link.

Settings reads the connection whenever it opens the Playback settings, after a
network change while it is open, and after **Reset** and **Test speed**.

### Admin

`GET /api/v2/admin/devices/{user_id}/{device_id}` gains `connections`: for each
key, the connection label, the latest probe, the learned limit, and the latest
plan summary. The web admin device page shows them, with **Clear limit**.
Each entry has an opaque `id`, `connection`, nullable `probe` (with
`lower_bound`), nullable `playback_sample`, nullable `learned_limit`, and
nullable `plan_summary: {plan_id, reported_at, diagnostics}`.
The admin resets one with `DELETE
/api/v2/admin/devices/{user_id}/{device_id}/connections/{connection_id}/learned-limit`
(204). Both reset endpoints preserve probe and summary observations.

Rows are deleted 30 days after their last update and when the device is
deleted. Only the hash of a public address is stored. Admins can already see
each account's client IPs under account activity, so this adds connection
speed, not location.

## Telemetry

The server first advertises the client feature `playback_plan_summary_v1`. Once
it does, clients send a new route event, `plan_summary`, when a plan stops being
the playing plan, whether it was replaced, stopped or failed.

A plan also stops being the playing plan when the app or page ends: closed,
reloaded, terminated or killed. Send its summary at that moment where the
platform allows it (a keepalive request, a background task). Otherwise keep the
summary on the device, updated each time the app leaves the foreground, and
send it at the next launch while its playback attempt is current (under 24
hours). Any `plan_summary` sent at a later launch, whether the summary kept for
app exit or a live send that failed and is retried, omits `throughput_kbps`.
Each plan's summary has one `event_id`, so a resend is a no-op: the server
dedupes on `(playback_attempt_id, event_id)`.

The event adds these keys to the diagnostics allowlist:

| Key | Value |
| --- | --- |
| `played_ms` | Milliseconds the plan was playing |
| `stall_count` | Counted stalls |
| `stall_total_ms` | The time of every stall, counted or not, up to whatever ended it (resume, pause, seek, source reload or the plan's end). Startup grace and outage time are excluded, including the part of a stall before the outage was detected. |
| `stall_longest_ms` | The longest single stall, on the same terms |
| `delivered_kbps` | The plan's delivered bitrate; omitted when the plan has none |
| `buffering_mode` | `never`, `ask` or `always` |
| `buffering_action` | The last action on this plan: `none`, `ignored` (a trigger in `never`), `lowered` (automatically), `restored`, `accepted`, `kept`, `timed_out` or `cant_lower` |
| `bandwidth_estimate_kbps` | Already on the allowlist: the value the start or replan request for this adoption carried, including a seek reanchor's; omitted when it carried none |
| `throughput_kbps` | Optional: the rate the plan's media requests received data (see *Playback samples*) |

A stall still in progress when its plan stops being the playing plan ends then.
Its time counts in `stall_total_ms` and `stall_longest_ms`, and it counts in
`stall_count` if it had lasted at least `STALL_MIN_MS`. Durations use the
monotonic clock (see *Terms*).

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
| Setting | `playback.buffering_quality_mode`, introduced in manifest revision 23. The revision is informational: native clients detect the setting by the key's presence in the server's settings manifest or resolved settings, not by comparing revision numbers. |
| Telemetry | Feature `playback_plan_summary_v1`, route event `plan_summary`, and the allowlist keys under *Telemetry*. |
| Phase 2 | The server advertises `connection_speed_v1` and adds `bandwidth_probe_url` to the capability response: the probe on the path this client's media takes. With it, clients probe that URL, report to `POST /api/v2/playback/bandwidth-probe/results`, and read `probe` and `auto_budget_kbps` from the connection endpoint. |
| Speed test | With `connection_speed_v1`, the capability response also carries `bandwidth_probe_target_kbps` (optional integer), `bandwidth_probe_automatic` (boolean) and `bandwidth_probe_max_age_seconds` (integer). Probe results accept optional `lower_bound` (boolean), and `bytes` up to 1,000,000,000. The connection endpoint's `probe` gains `lower_bound`, and the endpoint gains optional `playback_sample: {kbps, measured_at}`; the admin device `connections` gain the same. `plan_summary` may carry `throughput_kbps`. Tests run as *Connection speed test* says, and `probe_cases` pins the measurement. |

## Conformance

The tables above are the test oracle, and
[`schemas/buffering-quality/v1/vectors.json`](schemas/buffering-quality/v1/vectors.json)
encodes them. Each case is a list of timed events on one clock, with the
complete list of outputs it must produce. Timeouts come from the clock. The
events are:

- `plan_adopted`, carrying the plan's delivered bitrate, quality, menu and
  warnings. A missing delivered bitrate is `delivered_kbps: 0`. An adoption with
  `controllerInitiated: false` answers a replan the controller did not send;
  without the field, an adoption that follows the controller's own replan
  answers it. A client's harness needs this input to drop a pending lowering.
- `playing`, `waiting` (optionally with a `seek`, `pause`, `reload` or
  `renderer` cause), `pause`, `seek`, `transport_error_start`,
  `transport_error_end` and `replan_failed`. A plain `waiting` during a
  renderer wait means its buffer fell to the threshold.
- The pill answers `select_lower`, `select_keep`, `select_restore` and `back`.
- `pill_obscured` and `pill_unobscured`: a higher-priority actionable pill
  appears, or the last one disappears. Every state in which the player cannot
  show its pills (see *Presentation rules*) sends the same events. A pill that
  comes back emits its `prompt_shown` or `undo_shown` output again.
- `client_replan`, with an `operation` of `track_change`, `seek_reanchor`,
  `seek_failure_recovery`, `failure_recovery` or `output_change`: the client
  sends a replan for its own reasons. It outputs a `replan` whose body holds
  that operation, the adopted plan's quality as `quality_preference` and, on
  Auto with a kept estimate, `bandwidth_estimate_kbps`. The client's other
  request fields are outside the vectors.
- `quality_changed`: a quality change the controller did not send, such as the
  viewer's pick. It has no fields; the new quality arrives with the next
  `plan_adopted`.
- `new_attempt`: the client starts a new playback attempt of the same title
  that the viewer did not choose. It has no fields. It discards the kept
  estimate and nothing else: the answer, the once-per-title notice and the
  re-offer wait belong to the title.

Outputs are `replan` with its body, `prompt_shown`, `undo_shown`, and `notice`
with its copy key. Some cases also give the `buffering_action` each plan's
`plan_summary` must report.

The same file's `connection_budget_cases` is a server-only oracle for the
connection endpoint's 80% margin and administrator cap, over current readings
only. It does not add events to the client controller cases.

`probe_cases` is the oracle for *Measuring*, which every client replays against
its speed test arithmetic. Each case gives:

- the `mode`, `quick` or `accurate`, and an optional `target_kbps`;
- `chunks`, a list of `[t, bytes]` arrivals in milliseconds after the first
  byte (the first chunk is at 0);
- an optional `end`, the time the transfer ended (absent means it ran past the
  test's limit).

`expect` is `null` when the test measured nothing. Otherwise it is
`{kbps, lower_bound, bytes, duration_ms, stop}`, where `stop` is when the client
stops reading.

| Case | Pins |
| --- | --- |
| `quick_stops_once_a_window_beats_the_target` | Ramp-up at 83 Mbps, then 441 Mbps beats a 115 Mbps target: stop at 1,000 ms with a lower bound of 115000. |
| `quick_ramp_window_never_counts` | Window 0 above the target does not stop the test. |
| `quick_target_on_a_later_window` | A window just under the target, then one above it: stop at 1,500 ms. |
| `quick_settles_below_the_target` | A 48 Mbps link reports 48000 from the final second. |
| `quick_without_a_target_runs_to_its_limit` | No target: 3,000 ms and the settled rate. |
| `quick_silence_counts` | A final window with no bytes halves the settled rate. |
| `quick_transfer_ends_early` | A transfer that ends mid-window counts only complete windows. |
| `too_short_to_measure` | No complete window after window 0 measures nothing. |
| `accurate_reports_the_final_three_seconds` | The accurate test ignores the target and reports the final 3 seconds. |
| `accurate_fast_link_is_a_lower_bound` | A 2 Gbps link ends the 1 GB transfer early and reports 1000000 with `lower_bound: true`. |
| `quick_whole_transfer_before_window_1_reaches_the_target` | 100 MB in 600 ms leaves no complete window after window 0; the whole-transfer average reaches the target, so it reports 115000 as a lower bound. |
| `quick_whole_transfer_without_a_target_is_a_lower_bound` | 100 MB in 900 ms with no target reports the average, 888888, as a lower bound. |
| `accurate_whole_transfer_on_a_ten_gigabit_link` | 1 GB in 800 ms reports 1000000 with `lower_bound: true`. |

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

The remaining cases pin the rules added since:

| Case | Pins |
| --- | --- |
| `suppressed_history_retained` | A trigger suppressed by the re-offer wait keeps its history, and a later `playing` evaluates it. |
| `suppressed_ended_long_stall_retained` | A long stall that ended during suppression still qualifies at the next `playing` within the 120-second window, even when the new stall is under one second. |
| `repeated_no_lower_result` | Every adoption that is no lower shows its own notice. |
| `back_hides_undo_without_answering` | Back hides the undo pill without answering, so a later long stall lowers again. |
| `cooldown_expires_during_stall` | A cooldown that expires during a long stall evaluates it at once. |
| `auto_missing_bitrate` | Auto with no delivered bitrate has no lower rung: `cant_lower` once. |
| `auto_estimate_below_floor`, `ask_auto_estimate_below_floor` | A step-down estimate under 100 kbps has no lower rung, in `always` and in `ask`. |
| `obscured_prompt_preserves_remaining_timer`, `obscured_undo_preserves_remaining_timer` | A pill hidden behind a higher-priority pill comes back with the rest of its timer. |
| `obscured_prompt_discarded_on_plan_change` | A plan change discards a hidden prompt. |
| `visible_undo_retains_stall_history` | Stalls while the undo pill shows still count once it hides. |
| `step_down_uses_exact_integer_floor` | 22120 steps down to 15484. |
| `kept_estimate_on_later_replans` | After a lowering, a track change, seek reanchor and failure recovery carry 44100 without a source, including after an adoption the controller did not request. |
| `kept_estimate_replaced_by_next_lowering` | A second lowering replaces the kept estimate: an output change then carries 22120. |
| `restore_discards_kept_estimate` | After **Restore quality**, a track change carries no estimate. |
| `quality_change_discards_kept_estimate` | After the viewer picks a rung and then Auto again, a track change carries no estimate. |
| `new_attempt_discards_kept_estimate` | After a new start, a track change carries no estimate. |
| `renderer_waits_do_not_count` | Waits that begin with media buffered never trigger, however long. |
| `renderer_wait_drains_into_stall` | A renderer wait whose buffer falls to the threshold becomes a stall from that moment. |
| `accepted_lower_has_no_reoffer_wait` | **Lower quality** on a picked quality starts no re-offer wait: the lower plan's own trigger prompts again at once. |
| `reoffer_wait_counts_from_first_display` | A prompt held behind another pill starts its re-offer wait when it first shows, so the next prompt waits until 120 seconds after that. |
| `quality_change_ends_reoffer_wait` | A quality change ends the re-offer wait, so the new plan's trigger prompts at once. |
| `undo_timeout_reevaluates_stall_in_progress`, `undo_expiry_reevaluates_long_stall` | The undo pill's timer ending during a long stall in progress evaluates it at once, including a timer that a hidden pill or a pause froze. |
| `back_on_undo_reevaluates_stall_in_progress` | Back on the undo pill ends its suppression like the timer. |
| `reoffer_end_during_short_wait_waits_for_playing` | A suppression that ends during a wait shorter than `LONG_STALL_MS` evaluates nothing; the next `playing` does. |
| `lowering_result_without_bitrate` | A lowering's plan with no delivered bitrate counts as lower and shows the undo pill. |
| `failure_notice_spends_cant_lower_once` | A no-lower result's notice uses up the once-per-title notice. |
| `new_attempt_keeps_the_answer`, `new_attempt_keeps_cant_lower_once`, `new_attempt_keeps_the_reoffer_wait` | A new attempt of the same title keeps the answer, the once-per-title notice and the re-offer wait. |
| `unrequested_adoption_discards_pending_lowering` | An adoption the controller did not request drops its pending lowering with no notice or cooldown. |
| `startup_wait_then_frame_keeps_grace_anchor`, `slow_start_frame_after_grace` | The grace starts at the first wait before a frame, and the frame does not restart it. |
| `pause_ends_a_stall_uncounted`, `seek_ends_a_stall_uncounted`, `pause_and_reload_waits_never_count` | A pause or seek ends a stall without counting it, and waits caused by a pause or reload never count. |
| `client_replan_keeps_a_picked_quality` | Off Auto, the client's own replans, a seek failure recovery included, carry the picked quality and no estimate. |
| `ask_lower_keeps_the_estimate` | **Lower quality** on Auto keeps its estimate for the client's later replans, as an automatic lowering does. |

## Phases

| Phase | `silo-server` | `silo-android` | `silo-apple` |
| --- | --- | --- | --- |
| 1. Detect and lower | Add `bandwidth_estimate_source` and `learned_limit` to start and replan (optional fields, with schema and fixtures). Add the learned-limit store and its use in Auto, plus `bandwidth_limit` on the plan. Make Auto under a budget go lower or say so. Add `GET /api/v2/playback/connection` (connection and learned limit) and the reset endpoint. Add `playback.buffering_quality_mode` to the manifest with the default `always`, and regenerate bindings. Add `plan_summary` and its allowlist keys. Keep the vectors file in step with any spec change. In web, keep the automatic downshift as `always`, add the undo pill, the prompt for a picked quality, and the `ask` and `never` modes, send the estimate source and `learned_limit`, drop the local observed limit, and add the setting and limit rows. | A `BufferingQualityController` in `shared/` drives phone and TV, following the `IntroAutoSkipController` pattern. One Media3 adapter in `android-shared` feeds it. `PlayerNoticeOverlay` and `TvPlayerNoticeOverlay` gain actions, with `TvIntroAutoSkipBanner`'s focus rules. Revisit the startup detector's same-plan reopen: a slow plan that is still receiving data belongs to this controller. Regenerate `SettingKeys.kt`, and add the setting and limit rows to `PlaybackSettings.kt` and `TvSettingsScreen.kt`. | A shared controller like `IntroSkipPrompt`, fed by `AVPlayer` `timeControlStatus` and wait reasons, `AVPlayerItem` stall notifications, and AetherEngine buffering events. `PlayerNoticeOverlay` gains actions; on tvOS and macOS, focus follows the intro-skip pills. Regenerate `SettingKeys.generated.swift`, and add the setting and limit rows to `PlaybackSettingsView` and `TVPlaybackSettingsView`. |
| 2. Measure and show | Serve the probe on the media path and advertise its URL, target and schedule settings. Add the probe-results endpoint, `probe`, `playback_sample` and `auto_budget_kbps` on the connection endpoint, and the device `connections` field. Lower the probe limits to 4 at once per server. Add the web Connection group, the admin device view, and the quick and accurate tests. | The speed test in `android-shared`, skipped when `PlaybackNetworkMonitor` reports metered. Send the estimate and its source, and `throughput_kbps` from the Media3 loader. Add the Connection group. Update `docs/playback/01-media3-only-player-architecture.md` §3.5–3.6. | The speed test with `URLSession`, skipped when `NWPathMonitor` reports metered. Send `throughput_kbps` from Aether's upstream reader. Add the Connection group. |
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
