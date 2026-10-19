# Images API

> **API lifecycle:** this documents the stable `/api/v2` native contract, which locks with Silo
> 1.0. The frozen alpha `/api/v1` surface carries the same artwork ladder through the pre-1.0
> bridge window and is then retired. See
> [the native API contract](architecture/api-contract.md).

Silo caches artwork at a fixed ladder of widths and returns a URL for one of them.
The URL is either a direct object-storage URL or a signed server route. By default the server picks the width from context — card rows get
narrow images, hero areas get wide ones. A client that knows better can ask for a
specific size instead.

Commands and paths in this document are relative to the repository root.

## The parameter

Add `image_size` to a request. It applies to the whole response: every artwork
URL in the body is resolved at that size, so a screen never mixes resolutions.

```http
GET /api/v2/catalog?image_size=large
GET /api/v2/catalog/items/{id}?image_size=small
GET /api/v2/home/sections?image_size=medium
```

Accepted values are `small`, `medium`, `large`, and `original`. The enum is part of
the contract, so anything else is rejected by request validation as
`422 validation_failed` — a typo is a client bug, and quietly serving a default
would hide it behind artwork that is merely the wrong resolution.

Omitting the parameter keeps the server's per-context defaults, described
below.

The parameter is accepted on:

- catalog browse and query
- item detail and watch detail
- seasons, a single season, and episodes
- home and library sections, including single-section items
- the personal lists: `/api/v2/favorites`, `/api/v2/watchlist`, and `/api/v2/history`
- shuffles, the not-interested list, and notification series
- the recommendation reads: discover, For You, a recommendation section,
  because-watched, popular, recently added, similar, similar users, the taste
  seed grid, Watch Tonight and Watch Tonight cards

Other surfaces ignore it. The frozen `/api/v1` recommendation routes ignore it
too.

Within item detail this covers cast and crew headshots too: they follow the
`profile` ladder, which has no wide rung, so `large` and `medium` land on the
same 500px image. Without the parameter they are that 500px image as well.

Without the parameter, an episode still standing in for a backdrop in a section
hero or Continue Watching uses the widest still rung that fits, 1280px.

The `/api/v2/catalog/people` operations do **not** take the parameter — their headshots are
always the 500px variant. Browsing a person's filmography does honor it, because
that is `GET /api/v2/catalog?source=person` rather than a person operation.

On the personal lists the per-slot defaults are asymmetric — a 500px poster
beside a 300px backdrop — so an explicit size changes both, not just the one that
looks wrong.

Watch Tonight cards carry each cast member's `photo_url` on the profile ladder:
500px without a size, like item detail.

## Per-slot widths

One size for a whole response cannot serve a screen that draws one image of an
item large and another small: a TV home row shows each item's poster as a card
and its backdrop full-screen behind the row when the card has focus. Wherever
`image_size` is accepted, a client can instead give the width in device pixels
it draws each slot at:

| Parameter | Slot |
| --- | --- |
| `poster_width` | every `poster_url` |
| `backdrop_width` | every `backdrop_url` and `backdrop_urls` entry |
| `logo_width` | every `logo_url` |
| `still_width` | an episode row's `still_url` |
| `photo_width` | a cast or crew `photo_url` |

```http
GET /api/v2/home/sections?image_size=medium&backdrop_width=2688&logo_width=1760
```

The server returns the narrowest cached size at least that wide on the image's
own ladder, including sizes `image_size` cannot name (a 1000px backdrop slot gets
the 1280px rung), and the original when every size is narrower. A slot is named
for the field, not the image in it: an episode's `backdrop_url` often holds its
still, and `backdrop_width` resolves it on the still ladder. A slot width
overrides `image_size` for that slot; slots without one keep `image_size`, or the
per-context default when it is absent too. Widths run from 1 to 7680; anything
else is `422 validation_failed`. Artwork a metadata plugin hosts has no fixed
widths and gets a semantic hint instead; a headshot keeps the `large` hint cast
lists already use unless its width needs the original. Where a listing's continuation cursor
is bound to `image_size`, it is bound to the widths too.

## Widths

| Image type | `small` | `medium` | `large` | `original` |
| --- | --- | --- | --- | --- |
| poster | 300 | 500 | 1000 | up to 3840 |
| still | 300 | 500 | 1280 | up to 3840 |
| backdrop | 300 | 1920 | 2560 | up to 3840 |
| logo | 500 | 500 | 1280 | up to 3840 |
| profile | 300 | 500 | 500 | up to 3840 |
| banner | 500 | 500 | 1000 | up to 3840 |
| thumb | 300 | 500 | 1280 | up to 3840 |
| clearart | 500 | 500 | 1000 | up to 3840 |
| disc | 300 | 500 | 1000 | up to 3840 |

The last four rows are the [extended item artwork](#extended-item-artwork); only
`/api/v2` lists them.

`medium` is the width the server chose before this parameter existed, which is
why it is not always the middle rung. `original` is the cached original at the
source's full resolution, re-encoded as WebP and capped on ingest at 3840px on
its longest edge — it is not the provider's untouched file.

Artwork hosted by a metadata plugin rather than cached in the bucket has no
fixed width. For those the size is forwarded to the plugin as a semantic variant
hint — `card`, `featured`, `large`, or `original`, out of the SDK's open
`card`/`featured`/`large`/`full`/`original` vocabulary — and the plugin picks the
closest image it has, so the widths above are indicative rather than exact.

Do not hardcode this table. Read it from the capability endpoint: the ladder is
allowed to change, and the endpoint is generated from it.

The capability response also includes `storage_backend` (`local` or `s3`) and
`delivery` (`server` or `direct`). Local artwork URLs are root-relative signed
routes such as `/api/v2/artwork/{key}`. They return `404` for an invalid or
expired signature, `503` for a storage failure, support `HEAD`, `Range`, and
`ETag`, and enqueue repair for a missing revisioned cache object.

Clients can cache image bytes by URL. A revisioned artwork URL, whose filename
carries a content revision such as `w500.<revision>.webp`, stays the same for a
UTC day on every server and is valid for at least the configured lifetime after
it is handed out. S3 presigned URLs follow the same rule. Cloudflare token URLs
change every quarter of the token lifetime and are valid for at least three
quarters of it. Other artwork URLs change every 15 minutes or sooner. The path
identifies the bytes and the query only authorizes, so a client may key its
cache of revisioned artwork by path to keep entries across the daily change.

## Capability endpoint

```http
GET /api/v2/images/capabilities
```

`getImageCapabilities` returns an `ImageCapabilities` document. It supports
`If-None-Match` and answers `304` when the caller's copy is current.

```json
{
  "revision": "1",
  "state": "available",
  "allowed": true,
  "param": "image_size",
  "slot_width_params": {
    "poster": "poster_width",
    "backdrop": "backdrop_width",
    "logo": "logo_width",
    "still": "still_width",
    "photo": "photo_width"
  },
  "season_list_artwork_param": "include_artwork",
  "episode_still_provenance": true,
  "extended_item_artwork": true,
  "sizes": ["small", "medium", "large", "original"],
  "widths": {
    "poster": { "small": 300, "medium": 500, "large": 1000 },
    "backdrop": { "small": 300, "medium": 1920, "large": 2560 },
    "still": { "small": 300, "medium": 500, "large": 1280 },
    "logo": { "small": 500, "medium": 500, "large": 1280 },
    "profile": { "small": 300, "medium": 500, "large": 500 },
    "banner": { "small": 500, "medium": 500, "large": 1000 },
    "thumb": { "small": 300, "medium": 500, "large": 1280 },
    "clearart": { "small": 500, "medium": 500, "large": 1000 },
    "disc": { "small": 300, "medium": 500, "large": 1000 }
  },
  "original_max_width_px": 3840
}
```

Read `state` before sending `image_size`: anything other than `available` means the
server will not honor the parameter, so keep using its per-context defaults.

## Publication and fallback

Historical GC manifests are verified in the background before they become
publication records; their listed keys alone do not prove upload completion.

Catalog reads select cached artwork from durable publication and delivery
records. They never issue storage HEAD requests or fetch artwork delivery URLs.
The publisher records the exact variant keys after every upload succeeds;
partially uploaded revisions are not advertised. The revision remains part of
each key, so an old manifest cannot establish availability for new artwork.

A bounded background task verifies both storage and the client-facing GET path.
Publication makes a revision eligible for verification. Workers claim up to 100
revisions per run, use at most 12 concurrent checks, and stop after one minute.
Completed checks become eligible again after 15 minutes; a large backlog can
extend that interval. A worker failure leaves a recoverable two-minute lease.
Delivery verification is scoped to the storage and delivery configuration.
Transport errors preserve the last completed verdict and are retried. Confirmed
storage loss schedules image caching for regenerable provider artwork while
retaining catalog pointers and surviving variants;
delivery-only failures retain the catalog pointers and are checked again.

On every storage backend, a size the artwork's revision did not publish, such as
a rung a ladder change added that the ladder backfill has not generated yet,
resolves to a smaller size it did publish. Legacy artwork without a
publication manifest uses an established smaller size until the ladder backfill
regenerates it. Before external delivery is verified, the server also avoids
newly added wide rungs and selects a published smaller size or original. A
completed delivery check restricts selection to working keys.
If none remain, the URL is empty and the client should display its placeholder.
Images can still fail at a particular client or CDN edge; this must not prevent
rendering titles, episode counts, progress, or navigation.

`image_size=large` expresses the preferred size; the response may contain a
smaller variant. The server caches resolved artwork URLs for at most five minutes
so background recovery becomes visible without waiting for signature expiry.
Cold resolver caches read the same durable records and never rediscover image
availability through network probes.

## Season selectors without artwork

`GET /api/v2/catalog/series/{id}/seasons?include_artwork=false` (`listSeriesSeasons`)
skips poster URL preparation and omits `poster_url` and `poster_thumbhash` from each
season. Season metadata, counts, and user data are unchanged. The default is `true`.
Invalid boolean values return `422 validation_failed`.

The images capability response advertises this option as
`"season_list_artwork_param": "include_artwork"`. tvOS uses it for text-only
season selectors; clients that render season posters should keep the default. See
the [season-list contract](catalog-api.md#season-list-artwork) for the full
response semantics.

## Extended item artwork

`"extended_item_artwork": true` means movie and series detail
(`getCatalogItem`) can carry artwork beyond the poster, backdrop and logo:

| Field | Image |
| --- | --- |
| `banner_url`, `banner_thumbhash` | A wide banner strip (Kodi `banner.jpg`, about 1000x185) |
| `thumb_url`, `thumb_thumbhash` | A 16:9 landscape thumb, which usually carries the title (Kodi `landscape.jpg`) |
| `clearart_url` | Transparent cut-out art with the title (Kodi `clearart.png`) |
| `disc_url` | Transparent disc art (Kodi `disc.png`); movies only |
| `backdrop_urls` | Every backdrop in display order, `backdrop_url` first |

Each field is absent when the item has no such image. `backdrop_urls` lists only
`backdrop_url` when the item has one backdrop, and is absent when it has none.
Season and episode details carry none of these fields, and neither do catalog
cards. The URLs follow `image_size` like the other artwork on the detail; with no
size they resolve at `medium`.

Today the images come from local sidecar files next to the media (see
[local NFO metadata](architecture/local-nfo-metadata.md#sidecar-artwork)). No
metadata plugin supplies them, so a library without such files has none. The
frozen `/api/v1` detail and capability documents do not carry them.

## Episode still provenance

`"episode_still_provenance": true` means the server can say whether an episode
image is the episode's own still: `poster_is_episode_still` and
`backdrop_is_episode_still` on catalog cards and details, and
`still_is_episode_still` on episode rows. Responses carry these fields only where
the server knows the image's source; an absent field means unknown, even when the
capability is true. Clients that hide unwatched episode stills use them to keep
series and season artwork visible. See
[episode spoiler protection](catalog-api.md#episode-spoiler-protection).

## Jellyfin compatibility

The Jellyfin-protocol surface maps its own `MaxWidth`/`MaxHeight`/`FillWidth`/
`FillHeight` parameters onto the same ladder. It serves the narrowest of `small`,
`medium` and `large` that is at least four fifths of the requested width on the
ladder of the image that serves the request (`Primary` is a poster, an episode
still or a headshot, and an item without the requested type can fall back to
another), and `original` when even `large` is narrower. A height counts as the
width it implies at that image's usual aspect ratio: 2:3 for posters and
headshots, 16:9 for stills, `Backdrop` and `Thumb`. `MaxWidth` and `MaxHeight`
bound the image, so with both the smaller width wins; `FillWidth` and
`FillHeight` describe a box the image covers, so the larger wins. With no
parameters, `Backdrop` is `medium` and every other type is `small`.

Movies and series advertise their banner, thumb, clear art and disc art under the
Jellyfin image types `Banner`, `Thumb`, `Art` and `Disc` in `ImageTags`, and every
backdrop in `BackdropImageTags`. `/Items/{id}/Images/Backdrop/{index}` serves the
backdrop at that position; see the
[Jellyfin compatibility API](jellycompat-api.md#extended-artwork).

The shared cached-artwork resolver applies the same persisted availability
selection to Jellyfin image URL resolution. Its protocol parameters and image
response shapes are unchanged; image fetching remains independent of catalog
metadata responses.

## Bridge note

The frozen alpha surface exposes the same ladder at `/api/v1/catalog`,
`/api/v1/home/sections`, and the other v1 reads, and its capability document at
`GET /api/v1/images/capability` (singular, with a numeric `schema_version` instead of
`revision`/`state`). It rejects a bad size with `400 invalid_image_size` and a bad
`include_artwork` with `400 invalid_include_artwork`. Those paths are frozen, and Silo
1.0 answers the whole `/api/v1` namespace with `410 Gone` and the
`client_upgrade_required` problem code. Build against `/api/v2`.

### Marker thumbnail lifecycle

Marker preview state is stored separately from embedded chapters. Served keys
share the protected chapter-image namespace, using
`chapter-images/{file_id}/marker-{occurrence_sha256}-{image_sha256}/w{width}.webp`.
The occurrence digest includes the file identity, path and exact kind/start/end;
the image digest makes uploads immutable even after worker lock loss.

The chapter-image URL resolver and collector also recognize marker references.
A database trigger clears stale marker metadata on file/range changes and queues
displaced images atomically, with at least 48 hours of grace and any later issued
URL deadline preserved. File deletion retains the existing namespace cleanup.
Width replacement keeps the previous image usable until the new image commits.
