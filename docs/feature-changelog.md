# Feature Changelog

## 2026-08-20

### Expand Audiobookshelf compatibility to ebooks

Audiobookshelf-compatible clients can browse, search, open, and synchronize ebooks alongside audiobooks while keeping audiobook-only features unambiguous.

- Adds ebook libraries, files, metadata filters, author and series detail, reader progress, and primary-file selection to the ABS compatibility API.
- Preserves official-client progress fields and supports explicitly marking a completed ebook unfinished without allowing routine autosaves to regress completion.
- Applies profile-aware catalog access and metadata-curation permission checks, redacts operational access errors, and keeps bookmarks, collections, playlists, playback, and RSS feeds audiobook-only.
- The primary ebook file chosen through an Audiobookshelf client is honored by the native reader and mark-as-read flow too, so every client agrees on which file is the book.

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
