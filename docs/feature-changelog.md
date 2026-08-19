# Feature Changelog

## 2026-08-19

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
