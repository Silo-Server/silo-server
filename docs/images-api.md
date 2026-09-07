# Images API

Silo caches artwork at a fixed ladder of widths and returns a presigned URL for
one of them. By default the server picks the width from context — card rows get
narrow images, hero areas get wide ones. A client that knows better can ask for a
specific size instead.

Commands and paths in this document are relative to the repository root.

## The parameter

Add `image_size` to a request. It applies to the whole response: every artwork
URL in the body is resolved at that size, so a screen never mixes resolutions.

```http
GET /api/v1/catalog?image_size=large
GET /api/v1/catalog/items/{id}?image_size=small
GET /api/v1/home/sections?image_size=medium
```

Accepted values are `small`, `medium`, `large`, and `original`. Anything else is
`400 invalid_image_size` — a typo is a client bug, and quietly serving a default
would hide it behind artwork that is merely the wrong resolution.

Omitting the parameter keeps the per-context defaults exactly as they were, so no
existing client is affected.

The parameter is accepted on:

- catalog browse and query
- item detail and watch detail
- seasons, a single season, and episodes
- home and library sections, including single-section items
- the personal lists: `/favorites`, `/watchlist`, and `/history`

Other surfaces ignore it.

Within item detail this covers cast and crew headshots too: they follow the
`profile` ladder, which has no wide rung, so `large` and `medium` land on the
same 500px image.

The `/people` endpoints do **not** take the parameter — their headshots are
always the 500px variant. Browsing a person's filmography does honor it, because
that is `/api/v1/catalog?source=person` rather than a person endpoint.

On the personal lists the per-slot defaults are asymmetric — a 500px poster
beside a 300px backdrop — so an explicit size changes both, not just the one that
looks wrong.

## Widths

| Image type | `small` | `medium` | `large` | `original` |
| --- | --- | --- | --- | --- |
| poster | 300 | 500 | 780 | up to 1920 |
| still | 300 | 500 | 780 | up to 1920 |
| backdrop | 300 | 1920 | 1920 | up to 1920 |
| logo | 500 | 500 | 1280 | up to 1920 |
| profile | 300 | 500 | 500 | up to 1920 |

`medium` is the width the server chose before this parameter existed, which is
why it is not always the middle rung. `original` is the cached original, capped
on ingest at 1920px on its longest edge — it is not the provider's untouched
file.

Artwork hosted by a metadata plugin rather than cached in the bucket has no
fixed width. For those the size is forwarded to the plugin as a semantic variant
hint — `card`, `featured`, `large`, or `original`, out of the SDK's open
`card`/`featured`/`large`/`full`/`original` vocabulary — and the plugin picks the
closest image it has, so the widths above are indicative rather than exact.

Do not hardcode this table. Read it from the capability endpoint: the ladder is
allowed to change, and the endpoint is generated from it.

## Capability endpoint

```http
GET /api/v1/images/capability
```

```json
{
  "schema_version": 1,
  "param": "image_size",
  "season_list_artwork_param": "include_artwork",
  "sizes": ["small", "medium", "large", "original"],
  "widths": {
    "poster": { "small": 300, "medium": 500, "large": 780 },
    "backdrop": { "small": 300, "medium": 1920, "large": 1920 },
    "still": { "small": 300, "medium": 500, "large": 780 },
    "logo": { "small": 500, "medium": 500, "large": 1280 },
    "profile": { "small": 300, "medium": 500, "large": 500 }
  },
  "original_max_width_px": 1920
}
```

A `404` here means the server predates `image_size`. Keep using the server's
defaults rather than sending a parameter it will ignore.

## Publication and fallback

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

Before external delivery is verified, the server avoids newly added wide rungs
and selects a published smaller size or original. Legacy artwork without a
publication manifest uses an established smaller size until the ladder backfill
regenerates it. A completed delivery check restricts selection to working keys.
If none remain, the URL is empty and the client should display its placeholder.
Images can still fail at a particular client or CDN edge; this must not prevent
rendering titles, episode counts, progress, or navigation.

`image_size=large` expresses the preferred size; the response may contain a
smaller variant. The server caches resolved artwork URLs for at most five minutes
so background recovery becomes visible without waiting for signature expiry.
Cold resolver caches read the same durable records and never rediscover image
availability through network probes.

## Season selectors without artwork

`GET /api/v1/catalog/series/{id}/seasons?include_artwork=false` skips poster URL
preparation and omits poster thumbhashes. Season metadata, counts, and user data
are unchanged. The default is `true`. Invalid boolean values return
`400 invalid_include_artwork`.

The images capability response advertises this option as
`"season_list_artwork_param": "include_artwork"`. tvOS uses it for text-only
season selectors; clients that render season posters should keep the default.

## Jellyfin compatibility

The Jellyfin-protocol surface maps its own `MaxWidth`/`MaxHeight`/`FillWidth`/
`FillHeight` parameters onto the same ladder: up to 320px is `small`, 780px to
1199px is `large`, 1200px and above is `original`, and everything else is
`medium`.

The shared cached-artwork resolver applies the same persisted availability
selection to Jellyfin image URL resolution. Its protocol parameters and image
response shapes are unchanged; image fetching remains independent of catalog
metadata responses.
