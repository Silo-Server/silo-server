# Artwork API

Native clients should treat artwork URLs returned by catalog responses as opaque capabilities. Do not construct a storage key, bucket URL, or direct-library identity.

## Capability

`GET /api/v1/artwork/capability` is authenticated like the surrounding native API and reports:

- the effective `storage_backend` and portable `storage_format`;
- whether storage is portable;
- live `store_health`; compatibility fields `delivery_policy`, `delivery_modes`,
  and `automatic_recovery` are constant `"resilient"`, `["api"]`, and `true`;
- selected provider-art materialization and local-source policies;
- storage-management support for accounting, safe purge, and direct-library fallback;
- portability support for copied trees, source-adoption indexes, and verified seed import;
- the variant names supported for each image type.

Clients should capability-detect these fields rather than infer behavior from a URL shape.
The companion `GET /api/v1/images/capability` endpoint maps the client-facing
`image_size=small|medium|large|original` values onto these same live variants.

## Server settings

Artwork storage exposes `artwork.storage_backend`, `artwork.local_path`,
`artwork.remote_materialization`, and `artwork.url_ttl`. Local roots all use the
same cautious mount semantics; there is no ownership-mode setting. Imported,
unreferenced seed revisions have a fixed 30-day adoption grace.

`artwork.remote_materialization` decides storage, not delivery.
`"passthrough"` means provider and plugin images are not copied into the
artwork store; catalog responses still return this server's artwork URLs, and a
cold request fetches from the source within the request-time budget described
under [Resilient delivery](#resilient-delivery). `"selected"` copies the artwork
Silo selects into the store so those requests serve stored bytes.

## Admin storage controls

The admin-authenticated storage endpoints are documented in
[`admin-api.md`](admin-api.md). In addition to accounting, refresh, import, and
safe purge, `POST /api/v1/admin/artwork/rebuild` explicitly rebuilds an empty
local store after its pinned root is unavailable or has the wrong markers. A
successful response is the new storage-accounting state with
`store_health: "empty_rebuilding"`. S3 returns `422 unsupported_backend`.

## Resilient delivery

`GET|HEAD /api/v1/artwork/{signed-capability}/{variant}` is the default URL for
local, S3, and not-yet-materialized artwork. The opaque capability binds a
stable catalog target (surface name and primary keys), image slot, expected
logical revision when known, route version, requested variant, and quantized
expiry. The handler reloads the target on every request, so a still-valid URL
follows a newer conditional catalog publication instead of selecting displaced
bytes.

Delivery tries the current stored revision first. An authoritative object miss
is durably deduplicated into the repair queue; transport errors mark the backend
unavailable and never prove deletion. While repair runs, Silo may serve a
validated provider/plugin source or a confined sidecar from a bounded
process-local emergency cache. Those bytes use a source-derived `ETag` and a
short private lifetime. If no verified source can be served within the bounded
request budget, Silo returns a compiled image placeholder with `200` and
`Cache-Control: no-store`. It never returns an HTML or JSON error body to a
valid image capability.

The placeholder is for artwork that exists but is temporarily unservable. An
item with no artwork selected in a slot gets no capability at all: the
response's URL field is empty, exactly as before capability URLs existed.

Stored responses support `ETag`, `If-None-Match`, `Range`, and private caching
bounded by the signed expiry. An invalid or malformed capability is a
non-enumerable `404`; an expired valid capability returns `401` so the client
can refresh its catalog response.

The requested `image_size` is mapped to the capability's variant when the URL
is minted. Portable revisions created before the current ladder may lack the
new `w780` poster/still or `w1280` logo object. The delivery handler reads that
revision's manifest and serves its nearest smaller listed rung instead. This is
compatibility selection, not object loss: it does not mark inventory missing,
enqueue repair, or return a placeholder. Legacy revisions without manifests use
bounded object-existence checks for the same walk-down behavior.

After safe purge transitions an accessible sidecar out of canonical storage, catalog responses may instead contain a signed direct-library artwork URL. It has the same opaque-client contract and conditional/range support. Silo revalidates the current catalog reference, owning library root, confinement, file type, and size on every cache miss. Clients must never persist or interpret its embedded identity.
