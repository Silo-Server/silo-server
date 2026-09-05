# Jellyfin compatibility API

Jellycompat exposes Silo's movie and TV catalog, viewer state, and playback through
Jellyfin-shaped routes. It is a supported subset of the Jellyfin protocol; a
registered route does not imply every Jellyfin parameter or media type is
supported. The reference contract for this work is Jellyfin 10.11.8.

The route inventory is maintained in
`internal/jellycompat/testdata/media_routes.txt`. `internal/jellycompat/router.go`
owns registration; the native `/api/v1` contract is separate.

## Viewer state and preferences

| Routes | Behavior |
|---|---|
| `GET`, `POST /UserItems/{itemId}/UserData` | Read or partially update the current profile's state; POST returns the resulting user-data DTO. |
| `GET`, `POST /Users/{userId}/Items/{itemId}/UserData` | Legacy aliases with the same profile and item access checks. |
| `POST`, `DELETE /UserPlayedItems/{itemId}` and `/Users/{userId}/PlayedItems/{itemId}` | Mark played or unplayed; return HTTP 200 and the resulting DTO. POST accepts `datePlayed`. |
| `POST /Users/Configuration`, `/Users/{userId}/Configuration` | Persist profile settings and client presentation preferences; return 204. Current-user responses return effective settings. |
| `GET`, `POST /DisplayPreferences/{displayPreferencesId}` | Store preferences separately by account, profile, client, and preference ID. Writes return 204. |
| `GET /Localization/Cultures` | Language choices with two- and three-letter ISO codes. |

User-data updates support `Played`, `IsFavorite`, `PlaybackPositionTicks`,
`PlayedPercentage`, `LastPlayedDate`, and `PlayCount` values 0 or 1. Omitted
fields retain their values. An explicit historical `LastPlayedDate` remains the
reported date without making a new edit disappear behind a history tombstone.
Positional updates require a playable item; marking a series or season played
uses its child episodes. Ratings, likes, aggregate unplayed counts, and play
counts above 1 return 400. Inaccessible items and another profile's user ID are
rejected before mutation.

Configuration maps audio language, subtitle language, autoplay, and subtitle
mode into Silo's canonical profile settings. `Default` and `Smart` map to
`auto`, `Always` to `always`, and `None` to `off`. `OnlyForced` is not currently
supported and returns 400. Other declared presentation preferences round-trip
for clients. Storage failures produce errors instead of success responses.

## Browse and response fields

Item queries compose genre, year, search, selected-ID, collection, favorite,
and watched-state predicates rather than selecting one filter and discarding
the others. SQL state predicates bind both account and profile. Series and
season episode queries apply their scope and supported predicates before
counting and paging; detail and user-state hydration run on the selected page.

`/Shows/{id}/Episodes` accepts numeric `Season`, `SeasonId`, `StartItemId`,
`StartIndex`, and `Limit`. As in Jellyfin 10.11.8, an explicit `SeasonId` selects
its owning series and takes precedence over the path series and numeric season.
Episode SQL queries default to 24 rows and cap each page at 1,000. Clients should
page using `TotalRecordCount` and `StartIndex`.

`EnableImages=false`, `EnableImageTypes`, `ImageTypeLimit`, and
`EnableUserData=false` control item response presentation. Fields requiring
real detail are hydrated from the catalog; list responses no longer invent
media-source IDs or person IDs from titles.

| Routes | Behavior |
|---|---|
| `GET /Items/{id}/Ancestors` | Visible episode/season/series/library ancestry. When an item belongs to multiple libraries, chooses its first visible library parent. |
| `GET /Items/Filters`, `/Items/Filters2` | Visible catalog genre facets; the legacy shape includes years and official ratings. |
| `GET /Studios` | Visible catalog studios with paging. |
| `GET /Shows/Upcoming` | Scoped episodes dated from yesterday in UTC onward, with paging. |
| `GET /Items/{id}/ThemeMedia` | `ThemeSongsResult` and `ThemeVideosResult` envelopes after validating the owner. |
| `GET /Items/{id}/ThemeSongs`, `/ThemeVideos` | Valid empty theme result for a visible owner; theme ingestion is not implemented. |
| `GET /Persons`, `/Persons/{name}` | People with credits in movies or series visible to the current profile. Person image access rechecks this visibility before using cached artwork. |

These changes do not implement every advanced query option. Random and compound
sorts, full `IsMissing` semantics, multiple person-ID predicates, and populated
tag/language facets remain outside this subset.

## Playback negotiation and media

`GET` and `POST /Items/{id}/PlaybackInfo` evaluate source and output
capabilities, including client bitrate ceilings, audio-channel limits, container
conditions, and subtitle delivery profiles. The negotiated source records the
selected output constraints so local and remote encoders use the same decision.
Unknown or excessive source bitrate prevents copying under a client ceiling.
An automatic VideoToolbox bitrate must not override an explicit client cap.
Query `StartTimeTicks` is honored. Remux-only URLs use `static=false`.

`POST /Sessions/Capabilities` and `/Sessions/Capabilities/Full` persist device
profiles in PostgreSQL when available, keyed by a hash of the login/API token
and the client device ID. A request without a device ID uses the legacy empty
scope. Database failures return 503 instead of silently negotiating with a
profile lost on another API node. Expired registrations are removed in bounded
batches by the existing hourly cleanup.

Media requests require a login/API token or an unexpired `PlaySessionId` grant.
A grant authorizes GET/HEAD for its negotiated item and source; catalog item and
source IDs alone are not credentials. An invalid explicit token does not fall
back to a playback grant. Revoked owner credentials invalidate the grant.

Subtitle inventory preserves text and bitmap tracks. Selected embedded text or
bitmap subtitles can burn through the existing local or remote full-encode
path when that output is supported. Unsupported output combinations are not
advertised as playable.

| Routes | Behavior |
|---|---|
| `GET /Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/stream.{format}` | VTT/SRT conversion and requested timing window; raw ASS when compatible with the request. |
| `GET /Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/{startPositionTicks}/stream.{format}` | Path start position, with query `StartPositionTicks` taking precedence. |
| `GET /Videos/{itemId}/{mediaSourceId}/Attachments/{index}` | Actual embedded font bytes by original container stream index, with item/source access checks. |
| `GET /Playback/BitrateTest` | Returns the requested bounded byte count; default 102,400 bytes. |

Text subtitles support `EndPositionTicks`, `CopyTimestamps`, and
`AddVttTimeMap`. Raw ASS requests requiring conversion or time-window rewriting
return 406. There is no fallback-font service, external/downloaded subtitle
burn-in, or subtitle HLS playlist implementation. Changing a subtitle filter
requires fresh playback negotiation.

## Sessions and socket

`GET /Sessions` lists started playback mappings owned by the caller's token,
including mappings persisted by another API process. Device and activity filters
apply to the returned list. Current native play state is included when locally
available and its account/profile ownership matches; unavailable remote state
is omitted.

`POST /Sessions/Playing/Ping` touches the caller-owned playback activity without
changing position or paused state. `/socket` uses the Jellyfin keepalive
exchange: `ForceKeepAlive` with a 60-second timeout and `KeepAlive`
acknowledgements. Connections are bounded and periodically revalidate login/API
credentials. Remote-control capabilities are false; accepting a socket does not
claim remote-control command support.

## Scope and deployment

Migration `20260905013651_jellycompat_device_profiles.sql` creates the shared
capability registration table. Migration
`20260905015236_preserve_explicit_progress_event_time.sql` preserves an explicit
Jellycompat event date while the write timestamp and sync cursor advance. The
writer selects this behavior within its transaction; ordinary native writes
retain their existing timestamp behavior. These migrations do not change native
client API shapes.
Apple and Android native clients keep their existing settings and playback
contracts; shared font extraction retains the native font-bundle format.

The compatibility surface does not add audio-library playback, Live TV, IPTV,
DVR, or `.strm` support. See `docs/non-goals.md` for permanent product boundaries.
