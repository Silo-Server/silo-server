# Card overlays

Card overlays are the badges clients paint on poster art (4K, Dolby Vision, a rating) and the
watch-status indicator in a poster's top-right corner. Every client draws them live from data the
server sends; the server never burns badges into artwork, so per-profile choices and per-profile
watch state stay possible. This page covers the stored document, the rules every client's
renderer follows, and the uploaded images badges can use.

## Where the configuration lives

- `ui.card_overlays` is a profile setting holding one document, validated against
  `contracts/settings/v1/schemas/card-overlays.json`. `null` means the profile has not chosen, and
  the server-wide default applies.
- The server-wide default is the `defaults.card_overlays` server setting, returned as `defaults`
  by the overlay-config endpoint. It has the same shape.
- `ui.card_overlays_enabled` turns badges on or off. It does not affect the watch-status corner.
- The web client is the only editor today. A client that parses the document must keep ids,
  fields and values it does not render, or a later save from it erases another client's choices.

## The document

- `preset` picks a built-in look: `minimal`, `classic`, `vibrant`, `pill`, `square`, `plate`
  (large plates, ratings with a coloured source chip) or `band` (each edge's badges in one strip
  of marks over a scrim). Clients draw the same visual intent; exact pixels may differ per
  platform. A band strip shows the marks that fit whole, in order, and drops the rest; only a
  first mark too wide on its own truncates.
- `style` overrides parts of the preset: `size` (0.75–2, scales text, padding and icons), `shape`,
  `background` and `borderColor` (`#RRGGBBAA`), `textColor`, `borderWidth` (0–3 at the 185-unit
  reference card width), `fontWeight`, `uppercase`, and `shadow`. An absent field keeps the
  preset's value. A per-badge `accentColor` still applies on top: it tints a custom background in
  presets that fill with the accent, and replaces a custom text or border colour in presets that
  paint the accent there.
- A `background` that isn't fully transparent, or a `borderWidth` above 0, gives a badge padding
  of at least 0.6 times the font size across and 0.2 times down. Presets other than `minimal` and
  `band`, which draw bare text, already have about that much. From 50% opacity the fill sits
  behind the text: without a `textColor` the text is black or white, whichever reads better on
  it, and the preset's text shadow is dropped. An accent the preset writes as text keeps that role
  only at a contrast ratio of at least 3 against the fill; below that it tints the fill instead.
- `watchStatus` decides what the top-right corner shows (see below). Absent means `remaining`.
- `items` holds one entry per badge: `enabled`, `position`, optional `accentColor` and `showIcon`,
  and optional uploaded-image references (see below). `order` ranks badges that share a position;
  an empty list means registry order.

## Positions and the status corner

A badge sits in one of five places: `top-left`, `bottom-left`, `bottom-right`, or a rail centred
on the `left-edge` or `right-edge`. A corner draws at most three badges and a rail four; the rest
are dropped rather than overlapping.

The top-right corner belongs to the watch-status indicator, and no badge is drawn there. The
schema still accepts `top-right` because documents stored before settings revision 23 use it: a
client draws such a badge on the `right-edge` rail and writes `right-edge` when it saves. The top
badge row stops short of the corner so a long badge never runs under the indicator.

The indicator shows:

| `watchStatus` | Watched title | Started series or season | Anything else |
|---|---|---|---|
| `remaining` | check | episodes left | nothing |
| `watched` | check | nothing | nothing |
| `off` | nothing | nothing | nothing |

"Started" means the profile has watched at least one episode and has not finished; the counts come
from `user_state.watched_count` and `user_state.unplayed_count`, which the v2 catalog sends only
for a series or season the profile has not finished. An untouched show shows nothing, so a
library of new shows is not covered in numbers.

## Uploaded images

An administrator can upload images (PNG, JPEG or WebP, up to 4 MiB) to a server-wide library of up
to 256 images. Silo re-encodes each one as WebP fitted within 600 by 300 pixels and stores it under
the `overlay-images/` blob prefix, keyed by the SHA-256 of the stored bytes, so the reference names
the content and `GET /api/v2/overlay-images/{ref}` can be cached forever. The overlay-config
endpoint lists the library, and `supports_overlay_images` on the settings contract capabilities
says whether the server has one. Silo ships no logo artwork of its own: rating and format owners
restrict their marks, and an uploaded image is the administrator's to license.

A badge uses images two ways:

- `image` draws the image in the icon's place, beside the label. On a rating badge the image
  stands for the source, so the label drops the mark and shows only the score.
- `valueImages` maps a value key to an image that replaces the whole badge while the badge shows
  that value. The image is drawn about 1.6 times the height of the text badge it replaces.

An image that no longer loads (the administrator deleted it) falls back to what the badge draws
without it.

### Value keys

Keys are the values the API sends, not the labels a client formats from them, so every client
matches the same keys:

| Badge | Key |
|---|---|
| `resolution` | `overlay_summary.resolution`, such as `2160p` or `1080p` |
| `hdr` | `overlay_summary.hdr`, such as `DV`, `DV HDR10`, `HDR10`, `HLG` |
| `resolution_hdr` | the resolution and HDR keys joined by a space (`2160p DV HDR10`), or the resolution alone |
| `audio`, `audio_channels`, `video_codec`, `container`, `aspect_ratio`, `release_type`, `edition` | the `overlay_summary` field of the same name |
| `content_rating`, `original_language`, `studio`, `network` | the item field of the same name |
| `show_status` | the normalized status: `Returning`, `Ended`, `Cancelled` or `Upcoming` |

Ratings, runtime, year, advisory age, the multi-audio and subtitle flags, and request status take
no value images: their values are continuous or carry a single meaning.

## Compatibility

Settings revision 23 added the edge positions, the `plate` and `band` presets, `watchStatus`,
`style`, and the image references. A server below revision 23 rejects a document that uses any of
them, so a client writes them only when the server advertises revision 23 or later; the web client
otherwise maps the edge rails back to the corner on the same side and drops the rest.
