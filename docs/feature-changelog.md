# Feature Changelog

## 2026-08-25

### Keep card menus accessible on touchscreen computers
Media-card action buttons now remain visible on desktop-width touchscreen and hybrid computers
whose primary input cannot hover. Mouse and trackpad systems retain the existing reveal-on-hover
behavior.

### Clear stale library troubleshooting entries after path changes
Full library scans now reconcile troubleshooting entries across the whole library, so diagnostics for removed or replaced root paths disappear after the path-change scan completes. Subtree and single-file scans remain scoped and cannot clear diagnostics elsewhere in the library.

## 2026-08-24

### Restore loudness when Silo downmixes surround audio to stereo
Surround sound mixed down by Silo could be about 6 dB quieter than the same
file in other media apps. Server-owned stereo conversions now restore that
headroom after the channels are combined, with a limiter to prevent clipping.
Direct play, copied audio, stereo sources, mono output, and surround output are
unchanged.

### Start audiobook playback once after browser capability detection
The web audiobook player now waits for the browser's capability check to finish before requesting a playback session. It previously reacted to each intermediate capability result, creating and immediately replacing multiple sessions when one play request should have produced only one.

### Play HDR video on SDR-only devices
An HDR source used to be a dead end for a device that cannot display HDR: Silo had no recipe that could convert one, so the plan refused rather than washing the picture out. Silo can now tone-map HDR to SDR, on streaming and on prepared downloads alike, so those devices get a watchable picture with the colors mapped rather than clipped.

It is off until an administrator turns it on. Playback settings gain two independent switches — hardware tone mapping and software tone mapping — both default off, because the result is a re-encode and the quality is a judgement call that belongs to whoever runs the server. Turning on only one pins every tone-map to that path; turning on both prefers hardware and falls back to software when no hardware executor has capacity, so a busy GPU no longer refuses a stream an idle CPU could serve.

Dolby Vision Profile 7 sources benefit too. Where the source declares a standards-compatible base layer, Silo now plays that base layer instead of refusing the file. Sources whose metadata cannot prove a usable base layer — Profile 5, and anything with incomplete signaling — are still refused rather than guessed at, and are re-examined automatically the next time the file is scanned.

Admin activity shows which path a session actually used, so a stream that fell back from hardware to software reads as software rather than as whatever was planned. Direct play and direct stream of HDR are unchanged, and a device that manages HDR itself is never pushed onto a tone-map route. When tone mapping and 4K transcoding are enabled, the quality menu still offers lower resolutions during source-preserving HDR playback; choosing one validates the executor then starts the tone-mapped transcode. Quality choices now include explicit High, Medium, and Low bitrate steps at 4K, 1080p, and 720p, while omitting same-resolution steps that would not actually reduce the source bitrate.

### Fix Jellyfin-compatible playback negotiation for optional codec-profile conditions
Jellyfin clients send codec profiles whose conditions are frequently optional, and Silo previously
failed every condition it did not model — including the `IsAnamorphic` check that jellyfin-web's
webOS profile sends — so compatible H.264 and HEVC files were pushed into a needless transcode.
Condition evaluation now matches Jellyfin's semantics and answers from real scanner data.
- Honors `IsRequired` on profile conditions: an optional condition Silo cannot evaluate no longer
  blocks direct play, while a required one still does. An omitted `IsRequired` key now decodes as
  required, matching Jellyfin's own deserialization default.
- Evaluates interlacing, frame rate, video and audio bitrate, audio sample rate, and audio profile
  from the scanned track data instead of leaving those conditions unanswered.
- Derives `IsAnamorphic` by comparing the track's display aspect ratio against its storage aspect
  ratio, and reports the same derived value on the media stream returned to clients.
- Restores direct play for compatible webOS H.264 and HEVC media that previously transcoded.

## 2026-08-23

### Never wait on H.264 stream-copy analysis to start playing
The check that decides whether an H.264 file can be stream-copied reads the opening seconds of the source, which on remote or cloud storage takes seconds. It used to run before playback could start, and a file it had never seen was held at the Play button; when the check itself failed, playback fell back to a full transcode even though nothing had actually proven the file unsafe.

It no longer runs on the request path at all. A file with no stored verdict is now played optimistically — the cheap stream-copy route — and the analysis runs behind the stream that is already playing. Watch pages behave the same way: they start the analysis and render immediately.

If the analysis then finds the file genuinely cannot be copied, Silo moves the sessions playing it off that route. Clients that advertise the new `plan_invalidated_v1` capability are told to switch, and they re-plan onto a transcode without the viewer seeing more than a brief rebuffer. Any other client — including every app version shipped before this change — has its session ended and recovers the way it already does; because the verdict is now stored, its next attempt starts on the transcode directly. An analysis that fails or is inconclusive changes nothing: nothing is stored and playback continues untouched, instead of the old behavior of transcoding on a failed check.

### Browsing no longer waits on H.264 stream-copy analysis
Silo checks each H.264 file once for a bitstream quirk that makes stream-copying unsafe. That check reads the opening seconds of the file, and it used to run while a media page was loading and be forgotten on every restart — so browsing a library, especially after a reboot, re-read part of every H.264 file. On remote or cloud storage that was the difference between an instant page and a slow one.

Three things change. Media pages no longer trigger the analysis at all; it now happens when a play is actually being prepared, so browsing is fast regardless of where the files live. The result is stored on the file instead of being kept only in memory, so it survives restarts and is reused while the file's size and modification time still match. And the check itself reads 5 seconds instead of 15.

A file that changes on disk is re-checked automatically: the stored answer is only trusted while the file's size and modification time still match, so re-encoding or replacing a file in place invalidates it without any manual step. Nothing is recorded when an analysis fails, so a transient error never turns into a stale verdict — the next request simply retries. No configuration changes, and playback behavior is unchanged.

### Let capable original-file players manage HDR presentation
Playback protocol v3 now recognizes the delivery-scoped `client_managed_dynamic_range_v1` claim on `original_http`. A client with a runtime-probing original-file engine can receive a declared HDR or Dolby Vision source even when the connected display does not natively advertise that source range, then choose its local presentation path after loading the file. The companion `client_selected_audio_track_v1` claim lets original delivery keep the complete source when a non-default audio track is selected; the plan's selected-track ordinal tells the claiming client which probed stream to activate instead of forcing a server remux, while unclaimed clients retain the old gate. Progressive and HLS outputs remain display-gated, explicit server-selected Dolby Vision transformations remain preferred, and a typed failure excludes the attempted original plan instead of looping; until server tone mapping exists, an exhausted HDR route still reports that limitation honestly.

### Serve tokenless playback from proxy nodes again
Playback protocol v3 now advertises the engine-neutral `authorized_media_origins_v1` opt-in, which a client sends together with `header_authenticated_media_v1`. Plans for such an attempt may return absolute, still credential-free media URLs on server-designated proxy origins (`/stream/v3/...`), so direct play, progressive remux, and HLS egress from the node pool instead of the API server. The proxy validates the caller's own access token against the same live login session the API checks, so revoking a session stops proxy playback immediately; replans and every other control-plane call stay on the API. A client that sends only `header_authenticated_media_v1` keeps today's API-local behavior unchanged, and so does a deployment with no proxy pool.

## 2026-08-22

### Measure delivered bytes on every serving path
Silo now measures what it actually sends, rather than trusting what a client reports it is watching.
- Every byte-serving route across the API server, Jellyfin-compatibility layer, standalone proxy, audiobook listener and transcode nodes reports what it served, to whom and how fast, off the hot path.
- Measurement is on by default and needs no configuration: every media route family — native, jellycompat, proxy, audiobooks and transcode nodes — is observed out of the box. `SILO_STREAM_TELEMETRY_ENABLED=false` is the per-process kill switch, `SILO_STREAM_TELEMETRY_FAMILIES` narrows observation or kills one misbehaving family without losing the rest, and the distributed merge turns itself on wherever Redis is configured, so a single-node install measures locally and a cluster merges without either setting a variable.
- Adds `GET /api/v1/admin/stream-telemetry/parity`, which puts the merged measurement beside the two live-session views admins read today and diffs them. See [docs/admin-api.md](admin-api.md).
- Makes no decisions: nothing is blocked, throttled or ended, and no existing admin view was repointed onto it.
- Fixes four defects on the byte paths themselves — proxied streams recorded against no owner, the proxy's own address recorded as the viewer's, the kernel sendfile fast path dead through the proxy chain, and stream tokens with no reliable creation time.

### Qualify bounded software video decoders without weakening evidence tiers
Playback protocol v3 now advertises the engine-neutral `software_video_decode_v1` opt-in. Exact and platform-attested clients may use bounded `hardware: false` entries from `video_decode[]` for original/direct eligibility only when they send the feature. Download creation accepts the same additive feature, evidence tier, and detailed decoder entries so persistent originals do not flatten a 1080p software claim into a device-wide 4K claim. Existing clients remain hardware-only at strict playback tiers and existing flat download payloads remain unchanged.

## 2026-08-21

### Keep signed playback credentials out of client-visible media URLs
Playback protocol v3 now advertises the engine-neutral `header_authenticated_media_v1` opt-in. Capable clients receive API-local direct, remux, HLS, subtitle, and font URLs without a signed stream token in the query or path, and attach their current API Authorization header to every media request instead. Existing clients keep the restart-resilient token URLs unchanged. Remote HLS executors can still run behind the API route, while direct/progressive proxy delivery is bypassed in this mode; transparent reconstruction after an API restart is intentionally replaced by a fresh client playback attempt.

### Admin accounts are never capped by an access group
An account promoted to admin kept its access group, so the Default Group's stream cap and library list still applied to it. Admins are now ungrouped everywhere: promoting clears the group, demoting lands the account on the default group unless the request names one, and `POST /admin/users`, `PUT /admin/users/{id}`, and `POST /admin/invitations` reject `role: "admin"` together with an `access_group_id` with `422`. Policy resolution ignores any group an admin row still carries, and a migration clears the admins that were grouped before this change.

## 2026-08-20

### Make featured heroes read like editorial summaries
Home and Library Recommended now present concise title metadata without taking technical quality details away from movie and episode pages.
- Prefers catalog runtime over progress duration and safely omits invalid or unavailable values.
- Orders movie and series metadata as year, runtime, IMDb rating, up to two normalized genres, and content rating.
- Gives episode heroes their own season/episode, runtime, and content-rating presentation.
- Keeps resolution, HDR, and audio-quality badges on movie and episode detail heroes.

### Give each profile its own watch-provider server
Plugin watch providers can now ask for connection details per profile instead of forcing every profile on a Silo server to share one installation-wide configuration.
- Lets a self-hosted provider give each household member their own server URL and credentials.
- Renders the provider's own setup fields beside the API key on the profile's watch-provider screen, so connecting stays a single step.
- Encrypts every field the provider declares as a secret and keeps submitted setup data out of admin-facing plugin configuration.
- Prefers a profile's own values over installation-wide values of the same name, so existing connections keep working until they are reconnected.

### Serve downloads from proxy nodes
Download delivery can now be spread across proxy and transcode nodes instead of always flowing through the API server.
- Adds `proxy_delivery` to the download capability response and the opt-in `/downloads/{id}/file-proxy` and `/direct-download-proxy` routes, which redirect to an eligible proxy node per request and serve bytes directly otherwise.
- Prepared downloads can run on transcode nodes; the result stays on that node and is relayed through the authenticated artifact API, so nodes need no shared mount.
- Bandwidth-limited downloads stay on the API server so server-wide and per-user limits remain exact.
- The existing `/file` and `/direct-download` routes are unchanged.

## 2026-08-19

### Scope API keys to the admin routes they need
`sa_` API keys can carry scopes. A key without scopes behaves exactly as before — full access as its owning user — while a scoped key is an allowlist credential admitted only to the routes its scopes name. Scopes narrow, they never grant: the owning user's role checks still apply afterwards.
- Two scopes ship: `admin:users` (user lifecycle plus reading a user's profiles) and `admin:access-groups:read`. Impersonation is outside both.
- A scoped `admin:users` key cannot escalate: it may not create or promote an `admin` account and may not set a password on an existing admin, so a leaked key cannot mint an unscoped login.
- `POST /api/v1/api-keys` and `POST /api/v1/admin/api-keys` accept `scopes`; `GET /api/v1/api-keys/scopes` advertises the supported scopes for feature detection. Jellyfin-compat surfaces refuse scoped keys.

### Per-user policy overrides inherit from the access group
User policy fields stop being "strictest of user and group wins" and become inherit/override: a field left unset on the account takes the access group's value, and a field set on the account is authoritative in either direction — an admin can grant downloads to one member of a no-downloads plan, or cap one member of an unlimited plan.
- Makes every user policy field nullable (`max_streams`, `max_transcodes`, `max_playback_quality`, `transcode_allowed`, `audio_transcode_allowed`, `download_allowed`, `download_transcode_allowed`, `library_ids`, plus a new `requests_allowed`); `null` means inherit. `0` on a stream or transcode cap now means an explicit "unlimited" override instead of "defer to the group".
- Adds `transcode_allowed` and `audio_transcode_allowed` to access groups so every account field has a group value to inherit, and lets users override the group's media-request gate.
- Admin user API: `GET` responses carry the stored overrides (null when inherited) plus an `effective_policy` block with the resolved values; `PUT` accepts an explicit `null` on any policy field to clear an override back to inherit. Login and `/auth/me` now report the resolved `download_allowed`.
- Migration maps existing rows so an account that was deferring to its group keeps doing so (a cap of 0 or less, an empty quality, and a permissive boolean become inherit), while explicit restrictions (false, positive caps, a named quality, a library list) stay as overrides. Two behavior changes come with it: a stored cap above the group's cap now wins instead of being clamped, and `download_transcode_allowed` — the one field whose old column default was "off", so nearly every account stores false — maps false to inherit rather than to an explicit deny, which means members of a group that allows transcoded downloads now get them. Only an explicit `true` on the account survives as an override; an account with no group still defaults to "off" for that field.
- Web admin: user forms gain per-field Inherit/Override controls and show the effective value next to each inherited field; the access-group editor gains the two transcode gates.

### Make published server builds easy to compare
Every successful default-branch container build now carries an ordered build number alongside its exact source revision.
- Publishes `build-N` beside the existing mutable `latest` and short-commit-SHA image tags.
- Shows `Build N · SHA` in the admin sidebar, with the build timestamp available on hover.
- Keeps build identifiers separate from deliberate Semantic Versioning releases; skipped workflow numbers simply leave harmless gaps.

### Make metadata refresh finish with the right artwork
Manual Quick and Complete Refresh now finish the selected item's artwork before reporting success instead of leaving it behind the global image-cache backlog.
- Chooses a text-bearing poster in the library's metadata language, then English, another language, and finally textless artwork.
- Honors an optional `includes_text` signal from metadata plugins so a language-tagged but textless poster cannot outrank one that actually carries a title.
- Claims only the refreshed item's cache jobs — including its seasons, episodes, and localizations — retries delayed ones once, and waits briefly on a worker that already holds a job without draining unrelated artwork.
- Keeps the refresh itself successful when artwork cannot be cached in time: the item, its new artwork paths, and the page it lives on still update, and the leftover artwork is reported as a warning and finishes on the background queue.
- Replaces the web app's refresh spinner with the final success, warning, or failure message.

## 2026-08-16

### Let viewers turn the intro prompt off
Skipping intros stops being a switch and becomes a choice of three: leave intros alone, offer a Skip Intro button, or skip automatically and offer an undo.
- Adds `playback.intro_skip_mode` (`never` / `ask` / `always`, default `ask`) at contract revision 7 and deprecates `playback.auto_skip_intro`, which could not express "never".
- Migrates every stored auto-skip-intro preference onto the new key, on both the PostgreSQL and per-user SQLite backends, so nobody's existing choice changes.
- Mirrors the two keys at write time for one release, so a preference set on an older phone, TV, or browser still shows up correctly on an updated one.
- Replaces the web profile and device switches with a three-way selector, while retaining the old switch only against servers older than contract revision 7.
- Gives the web player a timed Skip Intro prompt for **Ask**, no overlay for **Never**, and an immediate skip with a five-second **Watch Intro** undo for **Always**.

## 2026-04-09

Covers commits from 2026-04-08 22:32 EDT through 2026-04-09 20:02 EDT.

### Add artwork selection in the metadata editor
Admins can now browse and apply poster, backdrop, and logo images from enabled metadata providers without leaving the edit flow.
- Adds an `Images` tab in the item metadata editor for movies, series, seasons, and episodes.
- Pulls image options from provider plugins, shows provider badges and popularity ordering, and highlights the current selection.
- Applies the chosen image through new admin endpoints, caches it to storage, and locks image fields so future refreshes do not overwrite the manual choice.

### Add native ASS and SSA subtitle support
The player and backend now preserve styled anime subtitles instead of flattening them into simpler text formats.
- Serves ASS tracks natively across the main playback API, Jellyfin compatibility routes, and the proxy subtitle path.
- Integrates JASSUB in the web player so fonts, positioning, karaoke effects, and other advanced subtitle styling render correctly.
- Keeps existing SRT and VTT handling unchanged while exposing subtitle format badges in the subtitle picker.

### Show live server activity in the web app
Admins now get a Plex-style live activity indicator directly in the UI.
- Adds a real-time dropdown that summarizes direct play, remux, and transcode sessions alongside task progress and active library scans.
- Keeps the activity affordance visible on admin screens and only exposes it on regular app pages when activity is actually happening.
- Reuses existing realtime channels instead of introducing new backend plumbing.

### Improve playback reliability and control flow
Recent playback work focused on making copy-mode streaming behave more like direct play while reducing restart-related failures.
- Preserves copy-mode when seeking, enables seek-anywhere manifests when duration is known, and allows supported HDR content to stay in direct play or remux paths instead of forcing full transcodes.
- Retries Firefox copy-mode startup with a compatibility fallback to reduce failed starts on that browser.
- Separates player exit and minimize controls so watch-page navigation is less ambiguous.
- Handles transcode restarts more cleanly during segment waits and fixes copy-mode buffering after a restart so old segments are not served back to the player.

### Refresh playback surfaces after watch-state changes
A few related UI changes now keep browsing and episode detail views aligned with what the viewer just watched.
- Refreshes home sections after watched-state updates so progress-sensitive shelves stay current without a manual reload.
- Shows sticky version preferences on episode detail pages so preferred cuts or versions remain visible when choosing playback options.

### Add a cinematic Playing Next experience
Episode-to-episode playback now transitions through a richer post-roll flow.
- Replaces the bare post-roll screen with a `Playing Next` screen that can appear about 30 seconds before the end of an episode.
- Keeps the current episode running in a resizable mini-player while showing the upcoming episode with a more prominent background treatment.
- Adds auto-play-next playback settings and supports cross-season next-episode lookups.

### Support S3 key prefixes for stored media and assets
Storage configuration can now target a scoped path inside an S3 bucket instead of requiring a bucket root layout.
- Adds S3 key prefix configuration in server config loading, admin storage settings, and setup flows.
- Updates the S3 client so Silo reads and writes through the configured prefix consistently.

### Add chapter thumbnails and chapter-aware navigation
Playback now has much deeper chapter support, including generated preview images and more resilient thumbnail processing.
- Scans and stores embedded chapter metadata, exposes it through watch detail responses, and adds player chapter menus and seek-bar chapter affordances.
- Introduces library settings and backfill tasks for chapter thumbnail generation, with realtime thumbnail delivery into active playback sessions.
- Tracks thumbnail failure state, retry timing, and HDR thumbnail policy so generation can recover more predictably when extraction fails.
