# Wholphin → jellycompat Endpoint Coverage Report

Date: 2026-06-09
Scope: every Jellyfin server endpoint the Wholphin Android TV client (jellyfin-sdk-kotlin based) can call, cross-referenced against the routes registered in `internal/jellycompat/router.go`. Wholphin source analyzed from a local clone of `damontecres/Wholphin` (`app/src/main`). Commands assume the repository root is the cwd.

Wholphin's Seerr integration (`/api/v1/...` discover/request endpoints) targets a separate Seerr server, not Jellyfin, and is out of scope here.

## Methodology

1. Grepped Wholphin for every `*Api.method()` SDK call (`itemsApi`, `playStateApi`, etc.), websocket subscriptions, and raw URL construction; mapped each to its HTTP endpoint per the Jellyfin OpenAPI spec.
2. Inventoried every route registered in `internal/jellycompat/router.go`, including path normalization (`path_normalization.go` handles casing and `/emby`//`jellyfin` prefixes).
3. For each endpoint Wholphin uses but silo does not serve, checked whether a server-side gate (user policy flag, DTO field) prevents the client from ever calling it.

## Summary

- **Covered (full):** 22 endpoint families — everything on the core path: auth, browse, item detail, episodes/next-up, resume, playback info, direct/HLS streaming, subtitles, progress reporting, favorites/played, images, genres, persons, suggestions, media segments (skip intro), display preferences.
- **Covered (partial/stub):** 4 — websocket (keep-alive only, no remote control), `/Studios`, `/Users/Public`, `/QuickConnect/Enabled` (deliberate `false`).
- **Missing but safely gated:** 8 — the client never calls them because we don't advertise the capability (delete, subtitle download, trickplay, local trailers, additional parts, quick connect auth, live TV, playlists).
- **Missing and reachable:** 4 — theme songs, universal audio stream, item download, client log upload. These return 404 when the client calls them. None block video playback.

**Bottom line: the entire core video path is covered.** The reachable gaps are quality-of-life features (theme songs, photo screensaver, debug log upload) plus one advertised-but-broken inconsistency (`CanDownload=true` with no `/Items/{id}/Download` route).

## Coverage matrix

### Fully covered

| Wholphin call | Endpoint | silo handler |
|---|---|---|
| `userApi.authenticateUserByName` | `POST /Users/AuthenticateByName` | `handlers_auth.go` |
| `userApi.getCurrentUser` | `GET /Users/Me` | `handlers_auth.go` |
| `userApi.getUserById` | `GET /Users/{id}` | `handlers_auth.go` |
| `systemApi.getPublicSystemInfo` | `GET /System/Info/Public` | `handlers_system.go` |
| `sessionApi.postCapabilities` | `POST /Sessions/Capabilities` | `handlers_playback.go` |
| `itemsApi.getItems` | `GET /Items` | `handlers_items.go` |
| `itemsApi.getResumeItems` | `GET /UserItems/Resume` (+ legacy `/Users/{id}/Items/Resume`) | `handlers_items.go` |
| `userLibraryApi.getItem` | `GET /Items/{id}` (+ legacy user-scoped form) | `handlers_items.go` |
| `tvShowsApi.getEpisodes` | `GET /Shows/{id}/Episodes` | `handlers_items.go` |
| `tvShowsApi.getNextUp` | `GET /Shows/NextUp` | `handlers_items.go` |
| `suggestionsApi.getSuggestions` | `GET /Items/Suggestions` | `handlers_items.go` |
| `genresApi.getGenres` | `GET /Genres` | `handlers_items.go` |
| `personsApi.getPersons` | `GET /Persons` | `handlers_persons.go` (full when PersonRepo is wired; empty stub otherwise) |
| `searchApi.getSearchHints` | `GET /Search/Hints` | `handlers_items.go` (Wholphin's call is currently commented out upstream) |
| `mediaInfoApi.getPostedPlaybackInfo` | `POST /Items/{id}/PlaybackInfo` | `handlers_playback.go` |
| `videosApi.getVideoStreamUrl` | `GET/HEAD /Videos/{id}/stream(.{container})` | `streams.go` |
| (transcode path from PlaybackInfo) | `GET /Videos/{id}/master.m3u8` + HLS playlist/segments | `streams.go` |
| (subtitle delivery URLs) | `GET /Videos/{id}/{source}/Subtitles/{idx}/stream.{fmt}` | `streams.go` |
| `playStateApi.reportPlaybackStart/Progress/Stopped` | `POST /Sessions/Playing[/Progress|/Stopped]` | `streams.go` |
| `playStateApi.markPlayedItem/markUnplayedItem` | `POST/DELETE /UserPlayedItems/{id}` (+ legacy) | `handlers_userdata.go` |
| `userLibraryApi.markFavoriteItem/unmarkFavoriteItem` | `POST/DELETE /UserFavoriteItems/{id}` (+ legacy) | `handlers_userdata.go` |
| `mediaSegmentsApi.getItemSegments` | `GET /MediaSegments/{id}` | `handlers_items.go` (skip intro/credits) |
| `displayPreferencesApi.updateDisplayPreferences` | `GET/POST /DisplayPreferences/{id}` | `handlers_displayprefs.go` |
| `imageApi.getItemImageUrl` | `GET /Items/{id}/Images/{type}[/{index}]` | `handlers_images.go` |
| `imageApi.getUserImageUrl` | `GET /Users/{id}/Images/Primary` | `handlers_images.go` (placeholder avatar) |

### Partial / stub (graceful degradation)

| Endpoint | Status | Client impact |
|---|---|---|
| `GET /socket` (websocket) | Accepts upgrade, answers KeepAlive only (`handlers_websocket.go`) | Wholphin subscribes to `GeneralCommandMessage` and `PlaystateMessage` for server-initiated remote control. Connection stays healthy; remote-control commands are simply never delivered. No errors, feature inert. |
| `GET /Studios` | Empty-array stub | Studio browse rows render empty. |
| `GET /Users/Public` | Empty-array stub | No user tiles on the login screen; manual login works. |
| `GET /QuickConnect/Enabled` | Hardcoded `false` | Deliberate: Wholphin hides Quick Connect UI, so the unimplemented `POST /QuickConnect/Authorize` and `POST /Users/AuthenticateWithQuickConnect` are never called. |

### Missing but safely gated (client never calls them)

| Endpoint | Why it's unreachable |
|---|---|
| `DELETE /Items/{id}` | We return `CanDelete=false` per item (`mapping.go`) and `EnableContentDeletion=false` in user policy (`handlers_auth.go`), so Wholphin hides delete UI. |
| `POST /Items/{id}/RemoteSearch/Subtitles/{subtitleId}` | `EnableSubtitleManagement=false` in user policy hides subtitle search/download. |
| `GET /Videos/{id}/Trickplay/...` | We never emit trickplay metadata on item DTOs, so the scrubber never requests tiles. |
| `GET /Items/{id}/LocalTrailers` | Wholphin gates on `localTrailerCount > 0` (`TrailerService.kt`); we never set `LocalTrailerCount`. |
| `GET /Videos/{id}/AdditionalParts` | Gated on `partCount > 1` (`PlaylistCreator.kt`); we never set `PartCount`. |
| Quick Connect auth endpoints | Gated by `/QuickConnect/Enabled` = false. |
| Live TV suite (`/LiveTv/Channels`, `/Programs`, `/Recordings`, timers) | We expose no live-TV views or channels, so the feature never activates. |
| Playlists (`GET/POST/DELETE /Playlists/{id}/Items`) | We expose no playlist items in the catalog, so playlist screens are unreachable. Becomes a real gap the day silo grows playlists. |

### Missing and reachable (return 404 today)

| Endpoint | Wholphin caller | Impact | Severity |
|---|---|---|---|
| `GET /Items/{id}/ThemeSongs` | `ThemeSongPlayer.kt` | ~~404 → feature silently dead.~~ **Resolved 2026-06-09:** now stubbed with an empty `ThemeMediaResult` (including the `OwnerId` field jellyfin-sdk-kotlin requires). | Resolved |
| `GET /Audio/{id}/universal` | `ThemeSongPlayer.kt`, `MusicService.kt` | Audio streaming for theme songs and music playback. No music libraries are exposed today, but this is the second half of the theme-song path and the blocker for any future audio support in jellycompat. | Low (today) |
| `GET /Items/{id}/Download` | `SlideshowViewModel.kt`, `ScreensaverService.kt` | ~~Inconsistency: `CanDownload=true` while the route 404s.~~ **Resolved 2026-06-09:** `mapping.go` now sets `CanDownload=false`, so clients no longer attempt downloads. Revisit if a download route is ever implemented. | Resolved |
| `POST /ClientLog/Document` | `MediaReportService.kt`, `DebugPage.kt` | "Upload logs to server" debug action fails. | Low |
| `GET /Audio/{id}/Lyrics` | `NowPlayingViewModel.kt` | Music-only; unreachable until audio libraries exist. | Low |

## Recommendations (priority order)

1. **Merge the repeated-`Fields` fix** (PR #110). `parseItemsQuery` still reads `q.Get("Fields")` (`internal/jellycompat/query.go`), which truncates jellyfin-sdk-kotlin's repeated query params and breaks Wholphin episode auto-advance. This is the only *covered* endpoint with a known correctness bug for this client. Follow up by sweeping the remaining single-value reads (`Ids`, `GenreIds`, `PersonIds`, `Filters`, `SortBy`, `SortOrder`) to `q.Values`, matching how `IncludeItemTypes`/`MediaTypes`/`ImageTypes` are already handled.
2. ~~Resolve the `CanDownload` inconsistency~~ — **done 2026-06-09**: `CanDownload=false` until a download route exists.
3. ~~Stub `GET /Items/{id}/ThemeSongs`~~ — **done 2026-06-09**: empty `ThemeMediaResult` stub registered.
4. **Websocket server-push** (remote control: server-initiated pause/stop, display messages) — tracked in issue #122.
5. **Optional, later:** `POST /ClientLog/Document` accept-and-discard stub and real `/Studios` data. Playlists, lyrics, and universal audio only matter once silo exposes those content types through jellycompat.

## Notes

- Path normalization (`path_normalization.go`) already handles Wholphin's casing and any `/emby`//`jellyfin` prefixes, so none of the covered routes are at risk from URL-shape differences.
- Unmatched jellycompat paths fall through to chi's default 404 with no body; jellyfin-sdk-kotlin surfaces these as `InvalidStatusException`, which Wholphin generally catches per-feature.
