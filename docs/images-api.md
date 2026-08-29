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

## Fallback while artwork is being regenerated

The wide rungs (780px posters and stills, 1280px logos) were added after this
ladder shipped, so artwork cached by an earlier version has no object at those
keys. A one-shot background pass regenerates it, and until that pass reaches a
given image the server serves the next narrower rung it does have, ending at the
original.

The practical consequence for a client is that shortly after a server upgrade,
`image_size=large` may return an image narrower than the table above. It is never
a broken URL, and no client action is required: the correct width appears once
the pass completes. URLs served from a fallback carry a shortened expiry so the
real rung is picked up promptly rather than a day later.

## Jellyfin compatibility

The Jellyfin-protocol surface maps its own `MaxWidth`/`MaxHeight`/`FillWidth`/
`FillHeight` parameters onto the same ladder: up to 320px is `small`, 780px to
1199px is `large`, 1200px and above is `original`, and everything else is
`medium`.
