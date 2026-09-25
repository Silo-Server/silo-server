# Jellyfin compatibility API

Jellycompat exposes Silo's movie and TV catalog, viewer state, and playback through
Jellyfin-shaped routes. It is a supported subset of the Jellyfin protocol; a
registered route does not imply every Jellyfin parameter or media type is
supported. The reference contract is Jellyfin 12.1: Jellyfin 12.0 carried the
API changes, and 12.1 is a bug-fix release with the same OpenAPI surface.
`/System/Info` and `/System/Info/Public` report the configured emulated
version, `12.1.0` on new installs.

`/System/Info` also returns `CastReceiverApplications: []`. Silo does not
advertise Chromecast receiver applications; the empty array lets Jellyfin Web
initialize playback preferences without attempting to iterate a missing field.

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
| `GET`, `POST /DisplayPreferences/{displayPreferencesId}` | Store preferences separately by account, profile, client, and preference ID. Writes return 204. Reads always include `skipBackLength` and `skipForwardLength`, defaulting to 10000 and 30000 ms as Jellyfin does. As in Jellyfin 12, a write without either stores 15000 ms for it, and empty `landing-*` values are dropped. |
| `GET /Localization/Cultures` | Language choices with two- and three-letter ISO codes. Accepts case-insensitive paths, including `/Localization/cultures`. |
| `GET /UserViews/GroupingOptions`, `/Users/{userId}/GroupingOptions` | Authenticated empty array; library grouping is unsupported. The legacy alias requires the current profile's user ID. |
| `GET /SyncPlay/List` | Authenticated empty array for client discovery. Group playback remains unsupported and user policy reports `SyncPlayAccess: "None"`. |

User-data updates support `Played`, `IsFavorite`, `PlaybackPositionTicks`,
`PlayedPercentage`, `LastPlayedDate`, and `PlayCount` values 0 or 1. Omitted
fields retain their values. An explicit historical `LastPlayedDate` remains the
reported date without making a new edit disappear behind a history tombstone.
Positional updates require a playable item; marking a series or season played
uses its child episodes. Parent reads and mutation responses derive `Played`,
`PlayCount`, and `UnplayedItemCount` from those episodes while retaining the
parent's favorite status; an empty parent remains unplayed. A combined
played/favorite update commits the child progress and history together with the
series or season's favorite status; a storage failure rolls back the entire
update. Marking played or unplayed clears the resume position unless the request
supplies an explicit position. Read-only
echoed fields such as `UnplayedItemCount`, `Key`, and `ItemId` are ignored.
Non-null `Rating` or `Likes` values return 400. `PlayCount` accepts only 0 or 1;
other values return 400. Inaccessible items and another profile's user ID are
rejected before mutation.

Configuration maps audio language, subtitle language, autoplay, and subtitle
mode into Silo's canonical profile settings. Field names are case-insensitive;
duplicate casing variants of the same field return 400. Other declared
presentation preferences round-trip for clients. Storage failures produce
errors instead of success responses.

`SubtitleMode` sets both `playback.subtitle_mode` and
`playback.show_forced_subtitles`, so it reads back unchanged and native clients
behave the same way:

| Jellyfin mode | Silo settings |
|---|---|
| `Smart` | `auto` (Silo's auto is Jellyfin's Smart) |
| `Default` | `auto`; the saved Jellyfin configuration keeps `Default` |
| `Always` | `always` |
| `OnlyForced` | `off`, forced subtitles shown |
| `None` | `off`, forced subtitles hidden |

A profile with no stored subtitle mode reads as `Default`, Jellyfin's default.
Modes set outside a Jellyfin client read as the matching row (`off` with forced
subtitles shown reads as `OnlyForced`).

Audio and subtitle language preferences use three-letter ISO codes for recognized
languages (for example, `eng`). They match Jellyfin Web's selector only when
`/Localization/Cultures` offers that language; `fil`, for example, has no option.
Unrecognized and undefined tags are preserved. Native settings retain canonical
BCP 47 tags. Returning an unchanged language choice preserves a native region or
script preference, such as `pt-BR`; selecting a different language replaces it,
and an empty or null preference clears it.

`AudioLanguagePreference` `OriginalLanguage` stores the settings-contract tag
`x-silo-original` and reads back as `OriginalLanguage`; playback then prefers
each item's original-language audio, as native clients do.

Movie and episode detail responses select `DefaultSubtitleStreamIndex` from the
viewer's effective subtitle mode and language, including downloaded subtitles.
The detail-page selection therefore carries into playback instead of sending
an unintended Off choice. Explicit playback choices, including Off, still win.
If playback negotiates a different audio language, clients must omit
`SubtitleStreamIndex` to request a fresh automatic subtitle selection. An echoed
`-1` remains Off because it is indistinguishable from an intentional Off choice.

`PlaybackInfo` defaults follow the viewer's settings. `DefaultAudioStreamIndex`
is the audio track Silo selects for the viewer (audio language preference,
original language, and the series' remembered track), falling back to the
file's default track. `DefaultSubtitleStreamIndex` follows Jellyfin 12.1's
`MediaStreamSelector` for the effective subtitle mode and language, judged
against the starting audio track: external files (including downloaded
subtitles) sort first, and an unset subtitle language matches any language.
Silo's per-series remembered subtitle track is not applied, and an explicit
`SubtitleStreamIndex` in the request still wins.

## Browse and response fields

Item queries compose genre, year, search, selected-ID, collection, favorite,
and watched-state predicates rather than selecting one filter and discarding
the others. SQL state predicates bind both account and profile. Series and
season episode queries apply their scope and supported predicates before
counting and paging; detail and user-state hydration run on the selected page.

As in Jellyfin 12, a `/Items` request at the user root (no `ParentId`)
returns the user's libraries only when it carries no filter. Any parameter that
sets Jellyfin's `HasFilters` (item types, genres, tags, studios, media types,
IDs, `Is*`/`Has*` flags, name bounds, languages, and so on) makes it a
recursive search, including filters Silo does not apply. `LocationTypes` and
`ExcludeLocationTypes` are the exception: Silo has no virtual items, and older
clients send `ExcludeLocationTypes=Virtual` with their library-list request.

`AudioLanguages` and `SubtitleLanguages` (comma-separated or repeated) keep
items with a present file the viewer may play (library access and playback
quality limit) carrying an audio track, or an embedded or external subtitle,
in any listed language. Series match through their episode files. The
`Filters2` language facets count the same files.
ISO 639-2 codes such as `eng` match the stored canonical codes.
`HasSubtitles=false` ignores `SubtitleLanguages`, as upstream does.

`/Shows/{id}/Episodes` accepts numeric `Season`, `SeasonId`, `StartItemId`,
`StartIndex`, and `Limit`. As in Jellyfin 12.1, an explicit `SeasonId` selects
its owning series and takes precedence over the path series and numeric season.
Episode SQL queries default to 24 rows and cap each page at 1,000. Clients should
page using `TotalRecordCount` and `StartIndex`.

`EnableImages=false`, `EnableImageTypes`, `ImageTypeLimit`, and
`EnableUserData=false` control item response presentation. Fields requiring
real detail are hydrated from the catalog; list responses no longer invent
media-source IDs or person IDs from titles. When `Fields` requests
`MediaSourceCount`, library, Latest, and NextUp lists report the number of
present, accessible versions of each movie or episode.

Items carry Jellyfin 12's `OriginalLanguage` (movies and series). Episodes set
`ParentPrimaryImageItemId` and `ParentPrimaryImageTag` to their season's poster,
or to the series poster when the season has none; both are removed when the
request disables Primary images.

| Routes | Behavior |
|---|---|
| `GET /Items/{id}/Ancestors` | Visible episode/season/series/library ancestry. When an item belongs to multiple libraries, chooses its first visible library parent. |
| `GET /Items/Filters`, `/Items/Filters2` | Visible catalog genre facets; the legacy shape includes years and official ratings. For Movie, Series, Season, or Episode queries, `Filters2` also lists `AudioLanguages` and `SubtitleLanguages` as `{Name: "English (en)", Value: "en"}` pairs sorted by name. |
| `GET /Items/{id}/Collections` | Jellyfin 12 "Included In": visible BoxSets that store the item, sorted by name and paged by `StartIndex`/`Limit`. An item the viewer cannot see returns 404 regardless of membership; visible episodes and any season return an empty result. Smart collections have no stored membership and are not listed. |
| `GET /Studios` | Visible catalog studios with paging. |
| `GET /Shows/Upcoming` | Scoped episodes dated from yesterday in UTC onward, with paging. |
| `GET /Items/{id}/ThemeMedia` | `ThemeSongsResult` and `ThemeVideosResult` envelopes after validating the owner. |
| `GET /Items/{id}/ThemeSongs`, `/ThemeVideos` | Local theme songs for a visible owner; theme videos remain empty. |
| `GET /Persons`, `/Persons/{name}` | People with credits in movies or series visible to the current profile. `/Persons` accepts Jellyfin 12's `StartIndex`, `NameStartsWith`, `NameLessThan`, and `NameStartsWithOrGreater` (lowercased name comparisons) and a library or movie/series `ParentId`; other parents match nobody. Pages without `SearchTerm` hold up to 100 people; searches stay capped at 20. Person photo tags are signed and appear only in responses that passed this visibility check. `GET /Items/{personId}/Images/Primary` accepts a matching signed `tag` without authentication, as Jellyfin Web sends image requests without credentials; otherwise the session must see a credit for the person. Either check runs before cached artwork is used. |

These changes do not implement every advanced query option. Random and compound
sorts, full `IsMissing` semantics, multiple person-ID predicates, populated tag
facets, and the `Tags`, `StudioIds`, and `HasSubtitles` item filters remain
outside this subset.

## Playback negotiation and media

`GET` and `POST /Items/{id}/PlaybackInfo` evaluate source and output
capabilities, including client bitrate ceilings, audio-channel limits, container
conditions, and subtitle delivery profiles. The negotiated source records the
selected output constraints so local and remote encoders use the same decision.
Progressive remux evaluates container constraints against its MP4 output;
direct play evaluates them against the original source container.
Unknown or excessive source bitrate prevents copying under a client ceiling.
An automatic VideoToolbox bitrate must not override an explicit client cap.
The server also applies the account's effective per-stream bitrate limit for
the request's location (local or remote) at PlaybackInfo negotiation, before it
advertises direct or transcoded sources.
A lower client limit wins. Over-limit sources require a compliant video
transcode; if none is available, PlaybackInfo returns `PlaybackUnavailable`.
Static direct-play requests without PlaybackInfo cannot transcode an over-limit
source and receive `PlaybackUnavailable` instead. Negotiated limits are kept
with the playback session, so policy edits affect only new sessions.
Query `StartTimeTicks` is honored. Remux-only URLs use `static=false`.

Silo gives each version its own `MediaSources[i].Id`, while real Jellyfin reuses
the item id. Some clients therefore send a media-source id where an item id
belongs. `PlaybackInfo`, `GET /Items/{id}`, `MediaSegments`, `Download`, static
`/Videos/{id}/stream`, and the user-data and played-state routes accept a
media-source id there and resolve it to the item that owns its file (the
episode for an episode file). On `PlaybackInfo` the id selects that version
unless the body names a `MediaSourceId`. A stale body `MediaSourceId` falls back
to the route's version, and a route version the item no longer has answers
`404`. The negotiated session keeps the client's id as its route item id, so the
stream URLs it hands out and later session reports can carry that id.

The managed Jellyfin Web build opts into `SiloSeekReanchor=true` on
`PlaybackInfo`. For a copied-video HLS source, the response echoes
`SiloSeekReanchor=true`. The client can seek locally only within the available
media range and at or after the current generation's requested start position.
Otherwise it requests fresh `PlaybackInfo` with `StartTimeTicks` and uses the
new playback-session URL. This applies to forward seeks, backward seeks, and
initial resume. Video remains copied; audio conversion follows the negotiated
profile.

An opted-in nonzero start resolves the actual copied-video keyframe before
starting HLS. The source-aligned playlist uses that origin and actual fragment
durations; any preceding gap entries represent unavailable media, not playable
fragments. Client positions and progress remain source-relative Jellyfin ticks.
Clients that omit the extension retain the existing source-zero HLS bootstrap.
Unmodified clients still cannot seek beyond a growing copied-video playlist
without renegotiating; this extension does not claim a complete copy-HLS VOD
index.

Long-running copied-video sessions retain one observed playlist window and carry
its source origin forward using actual fragment durations. If an fMP4 session
loses that observation, bounded probes recover its origin when the generation's
first fragment remains available to calibrate any muxer timestamp shift.
MPEG-TS requires an observed window because its timestamps wrap. When the
required timing evidence is unavailable, start a new playback session rather
than guessing the source origin. Restarted generations never reuse an older
generation's timeline.

Deploying the server change requires reinstalling the managed Jellyfin Web
component to enable its seek handling. The installer applies the source patch
before building and records `silo-seek-reanchor-v1` in the component provenance.
If upstream source no longer matches the patch, installation fails while the
previous active bundle remains available.

`POST /Sessions/Capabilities` and `/Sessions/Capabilities/Full` persist device
profiles in PostgreSQL when available, keyed by a hash of the login/API token
and the client device ID. A request without a device ID uses the legacy empty
scope. Database failures return 503 instead of silently negotiating with a
profile lost on another API node. Expired registrations are removed in bounded
batches by the existing hourly cleanup.

Capabilities and `PlaybackInfo` requests accept bodies up to 1 MiB. A stored
device profile may contain up to 256 KiB of JSON and 1,024 entries total across
its profile arrays and nested conditions. Larger requests or profiles return
413. Device IDs longer than 256 bytes are stored under their SHA-256 hash.
Jellyfin Web derives its device ID from the browser's user agent, and
Jellyfin accepts these long IDs.

Each login/API token can register up to 64 active device IDs. Registering a new
ID at capacity returns 429; an existing ID can still update its profile. Expired
registrations release their slots when the token next registers a profile.
The quota is enforced across API processes.

Media requests require a login/API token or an unexpired `PlaySessionId` grant.
A grant authorizes GET/HEAD for its negotiated item and source; catalog item and
source IDs alone are not credentials. An invalid explicit token does not fall
back to a playback grant. Revoked owner credentials invalidate the grant.

Copied-video HLS master playlists name copied audio as Jellyfin 12 does:
HE-AAC as `mp4a.40.5`, TrueHD as `mlpa`, and DTS as `dtsc`, `dtsh` (DTS-HD HRA
and MA), or `dtse` (DTS Express). HE-AACv2 is `mp4a.40.29` (RFC 6381), where
Jellyfin writes `mp4a.40.2`. For Dolby Vision without a compatible base
layer (HEVC profile 5, AV1 profile 10), a client whose device profile lists
`DOVI` in a `VideoRangeType` condition also gets Jellyfin 12's `dvh1`/`dav1`
variant, listed before the `hvc1` fallback. MPEG-TS remuxes keep the single
variant. Audio and subtitle streams carry `LocalizedLanguage`, and audio
streams carry `LocalizedOriginal`, in English.

Subtitle inventory preserves text and bitmap tracks. Selected embedded text or
bitmap subtitles can burn through the existing local or remote full-encode
path when that output is supported. Unsupported output combinations are not
advertised as playable.

| Routes | Behavior |
|---|---|
| `GET /Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/stream.{format}` | VTT/SRT conversion, Jellyfin `.js` track-event JSON, and requested timing windows; raw ASS when compatible with the request. |
| `GET /Videos/{itemId}/{mediaSourceId}/Subtitles/{index}/{startPositionTicks}/stream.{format}` | Path start position, with query `StartPositionTicks` taking precedence. |
| `GET /Videos/{itemId}/{mediaSourceId}/Attachments/{index}` | Actual embedded font bytes by original container stream index, with item/source access checks. |
| `GET /Playback/BitrateTest` | Returns the requested bounded byte count; default 102,400 bytes. |

Text subtitles support `EndPositionTicks`, `CopyTimestamps`, and
`AddVttTimeMap`. JSON track events apply the same clipping and timestamp
rebasing; an empty timing window returns `TrackEvents: []`. Raw ASS requests requiring conversion or time-window rewriting
return 406. There is no fallback-font service, external/downloaded subtitle
burn-in, or subtitle HLS playlist implementation. Changing a subtitle filter
requires fresh playback negotiation.

Extracting an embedded text subtitle reads the whole source file, which can
take minutes for a large remux on network storage. As in Jellyfin, the first
request for an embedded text track extracts every text track of the file in
one pass, and the results are cached on the serving node. A request for a
track that is already being extracted waits for that pass, and a track with no
cues is remembered so the file is not read again for it. When `PlaybackInfo`
offers an embedded text subtitle with `DeliveryMethod: External` (Jellyfin Web
does) and the viewer's `SubtitleMode` is not `None`, the server starts that
extraction in the background for the source the client will play, so a later
switch to any text track is served from the cache. Background extractions share
the subtitle cache's two server-wide warm slots and are skipped when both are
busy.

Chrome on macOS decodes H.264 with VideoToolbox, which rejects some open-GOP
Blu-ray encodes whose I-frames carry recovery points instead of IDR frames and
a new PPS per GOP. Copied video from such a file stops with
`PIPELINE_ERROR_DECODE` (`-12909`); Jellyfin Web then reloads the stream, which
shows as periodic stutter. The bitstream is valid, and Jellyfin copies H.264 the
same way. Turning off hardware video decoding in Chrome avoids it.

## Sessions and socket

Sign-in refuses an account holding a temporary password with `401` and a message to
sign in to Silo first: Jellyfin clients cannot run the password change it requires
(see [temporary passwords](auth-api.md#temporary-passwords)). The account's other
state is unaffected, and signing in works again once the password is changed.

`GET /Sessions` lists started playback mappings owned by the caller's token,
including mappings persisted by another API process. Device and activity filters
apply to the returned list. Current native play state is included when locally
available and its account/profile ownership matches; unavailable remote state
is omitted.

Direct players such as webOS may send an empty `PlaySessionId` on
`/Sessions/Playing`, `/Sessions/Playing/Progress`, and `/Sessions/Playing/Stopped`.
Silo saves their reported position when the authenticated item/source identifiers
match exactly one started, active playback. Pending negotiations and terminal
sessions do not qualify; ambiguous matches are ignored. A final stop sample is
saved even when the viewer did not pause first. Samples remain last-write-wins,
including backward seeks to a positive position. Zero or omitted positions leave
the bookmark unchanged so startup reports cannot erase a saved resume point.
Without a per-play identifier a delayed sample cannot be distinguished from a new one. Unidentified stops do not change audio selection
or tear down resources. Native idle cleanup reaps the upstream session, but the
compatibility mapping remains routable until its configured expiry. A later play
that creates another mapping for the same item can make ID-less reports ambiguous
until the old mapping expires. Clients that send a valid per-play identifier
retain immediate, generation-scoped teardown.
ID-less static requests reject ambiguous matches and failed durable identity
lookups rather than selecting another session. Durable identity checks remain
fresh on every request; full session payloads use the normal per-session cache.

`POST /Sessions/Playing/Ping` touches the caller-owned playback activity without
changing position or paused state. The native session owner consumes persisted
activity before idle cleanup, so pings remain effective across API replicas.
Shared expiry and pings are serialized: a successful ping prevents stale cleanup,
while an already-retired session rejects the ping. A shared-store failure defers
compat session cleanup instead of treating unknown activity as inactivity.
Retained compatibility sessions count toward stream and transcode limits until
removal; shared-store failures conservatively retain that capacity.
Pings do not extend the absolute playback-grant lifetime; expiry requires fresh
playback negotiation. `/socket` uses the Jellyfin keepalive
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

New installs report Jellyfin `12.1.0` and install Jellyfin Web `12.1`.
Migration `20260923181531_jellyfin_compat_12_1_new_install_defaults.sql`
replaces the `10.12.0` emulated-version seed (a version Jellyfin never
released) only on databases that have not completed setup, and pins
configured servers that never stored a Jellyfin Web version to the previous
default, `10.11.6`. Existing servers keep what they report and install until an
admin changes it in the Jellyfin compatibility settings.

Jellyfin 12 behavior not yet provided: `MediaStream.IsOriginal` (needs the
probed `original` disposition in the native track model), the
`VideoRotation` profile condition and `VideoRotationNotSupported` reason
(rotation is not probed), external delivery of PGS and VobSub tracks during
direct play or remux (they burn in), `excludeActiveSessions` on resume lists,
remembered per-item subtitle selections, and `Accept-Language` localization.
`/Devices`, `/Playlists`, QuickConnect initiation, and SyncPlay group operations
are not served. `/SyncPlay/List` only provides the empty discovery response
described above.

The compatibility surface does not add audio-library playback, Live TV, IPTV,
DVR, or `.strm` support. See `docs/non-goals.md` for permanent product boundaries.

## Local theme audio

`GET /Items/{id}/ThemeSongs` and `/Users/{userId}/Items/{id}/ThemeSongs`
return `Items`, `TotalRecordCount`, `StartIndex`, and the resolved `OwnerId`.
The corresponding `ThemeMedia` routes wrap that result in `ThemeSongsResult`;
`ThemeVideosResult` and `SoundtrackSongsResult` remain empty. Both discovery
forms honor `inheritFromParent` and `sortBy=Random`, and recheck item visibility.
Omitting `inheritFromParent` defaults to `false`, as in Jellyfin.
Theme IDs are stable numeric encodings that survive a server restart.
Theme items include `ServerId` and can be fetched through `GET /Items/{id}`
or `/Users/{userId}/Items/{id}` before playback. These lookups recheck visibility.

The authenticated `GET|HEAD /Audio/{itemId}/stream`,
`/Audio/{itemId}/stream.{container}`, and `/Audio/{itemId}/universal` routes
serve theme audio. The original is served, with range and conditional-request
support, when the accepted containers, codecs, channel limits, and bitrate
limits permit it. Otherwise the request may receive a progressive AAC
conversion in audio-only MP4 if it names an MP4 target and accepts AAC: a
`stream.mp4`/`stream.m4a` route or `container=mp4|m4a` on the stream routes, or
`TranscodingContainer=mp4|m4a` over HTTP on the universal route. `static=true`
asks for the original only. The conversion honors `MaxAudioChannels=1` or
`TranscodingAudioChannels=1`, caps its bitrate at 192 kbps and at any bitrate
limit in the request, and seeks to `StartTimeTicks`. It has no byte ranges.
HLS theme transcoding (`TranscodingProtocol=hls`, which Jellyfin Web requests)
and requests that name no MP4 target return `400 PlaybackUnavailable`.
Selecting a specific audio stream is unsupported. Transcode fallback hints on
a universal request do not prevent direct play when the original fits.
Theme audio follows the playback routing policy like compatibility video: a
theme routed through a proxy is a `307` redirect to that proxy, and a policy no
route satisfies answers `503` with the routing-policy or capacity code.
For universal audio, `Container` declares accepted direct-play formats, including
`container|codec` entries. `AudioCodec`, `AudioBitRate`, and
`TranscodingAudioChannels` describe the fallback encoder. When the source bitrate
is unknown, `MaxStreamingBitrate` uses Jellyfin's conservative 40 Mbps estimate. The
stream routes treat `AudioCodec` as a constraint on the original audio.
Themes do not create playback sessions or update watched state.

See [local theme songs](catalog-api.md#local-theme-songs-v2) for file conventions,
ownership, inheritance, and routing.
