# Ebook reader API

> Release scope: the ebook reader is a **beta** feature. It stays available
> as-is in 1.0, outside the support promise, until a consolidated Books effort
> replaces it (no assigned release date). See the
> [scope decision](architecture/v1-scope.md#library-scope-books-deferred).

The v2 reader progress operations share the existing reader stores and media
authorization with the frozen v1 bridge. They require account authentication and
a verified `X-Profile-Id`. Current item access is checked on reads; writes also
check that the accessible ebook file belongs to the requested item.

| Operation | Method and path |
| --- | --- |
| getEbookCapability | GET `/api/v2/capabilities/ebooks` |
| getEbookProgress | GET `/api/v2/ebooks/{content_id}/progress` |
| saveEbookProgress | PUT `/api/v2/ebooks/{content_id}/progress` |

The capability reports `ordered_progress` independently of `kindle_conversion`.
Conversion retains its source formats, served format, and fallback header.
Capability availability describes configuration, not a health check.

Progress reads and writes return `{"progress": {...}}`. When no position is
saved, the progress member is absent. A position contains `content_id`, a string
`file_id`, `location`, fractional `progress` from 0 to 1, and `updated_at` in
UTC with millisecond precision. Responses are private and must not be cached.

A write requires `file_id`, `location`, `progress`, and a client event time in
`updated_at`. Clients capture the event time when the user changes position and
retain that exact value for a retry. The server rejects future timestamps.
The database accepts an event only when its timestamp is strictly newer than
the stored timestamp; equal and older events keep the current file, location,
and progress. The response contains the current saved position, which may be
newer than the submitted event.

The existing finished-book rule remains: after a book reaches the finished
threshold, ordinary autosaves can change its location but cannot mark it unread.
Explicit unread deletes the saved progress. V1 continues assigning server times
and does not gain v2 retry guarantees.

The progress request body is capped at 16 KiB; locations are limited to 8192
characters. Invalid input returns a validation problem. Inaccessible files and
files belonging to another item return not found. Reader config, annotations,
and binary delivery migration are tracked separately; the progress capability
does not advertise completion of those flows.

## Reader configuration

`GET /api/v2/ebooks/{content_id}/reader-config` returns `content_id`, a `config`
object, optional `updated_at`, and an `ETag` header. A profile that has never
saved configuration sees an empty object with its own validator.

`PUT` on the same path takes `{"config": {...}}` and requires `If-Match` from
that read. Missing validators return 428; stale validators return 412 with the
current ETag. `If-None-Match`, when supplied, is evaluated after `If-Match`.
The configuration object is client-owned, and the entire request is limited to
256 KiB. The server does not interpret renderer-specific keys.

The database locks the same configuration row that v1 writes. Concurrent first
writes serialize; a failed guard leaves no persisted default. A legacy write
invalidates the v2 validator. Clients should retain their validator while the
user edits and handle a conflict by reloading and reconciling, rather than
retrying with an unconditional overwrite. The capability's `guarded_config`
field advertises this contract.

## Ebook bytes

`GET` and `HEAD /api/v2/ebooks/{content_id}/files/{file_id}/read` use the same
verified profile and current file/parent authorization as reader state. The
`reader_files` capability advertises this route. Files remain inline with their
native ebook MIME type and `X-Content-Type-Options: nosniff`.

These operations stream bytes outside JSON buffering. They preserve byte ranges
(including multipart ranges), 206 and Content-Range, unsatisfiable-range 416,
HTTP conditional requests, and HEAD body suppression. The reader's existing
rolling write deadline applies. HEAD for an uncached Kindle conversion never
starts the conversion: it advertises EPUB without a Content-Length, while GET
produces the authoritative representation. A failed conversion returns the raw
original with the conversion-failed header and no-store caching. Cached converted
EPUB responses retain their conversion-key ETag and revalidation policy.

The web reader loads these bytes through the v2 session boundary using its
captured profile authority. The existing 512 MiB Content-Length check remains;
files beyond that size require downloading instead of an in-tab blob.

## Annotations

The capability's `guarded_annotations` field advertises the v2 annotation flow.
`GET /api/v2/ebooks/{content_id}/annotations` returns an `items` array and
`page` continuation. The default page contains at most 20 annotations; `limit`
accepts 1–50. Continue with `page.next_cursor` until `page.has_more` is false.
Cursors bind the account, profile, access policy, and content. Ordering is newest
`updated_at`, then ID descending. This is a live browse, not a snapshot: edits
during paging can move an annotation ahead of the cursor; refresh to reconcile.

`POST` on that path takes a client-selected string `id` (at most 128 characters,
a UUID is recommended) alongside the existing annotation fields. Retain that
identity on retries. A new annotation returns 201; the same existing scoped
identity returns 200 with its current state, preserving intervening edits.
An identity owned by a different account, profile, or content returns 409.

Each annotation carries `etag`; create and edit also return the `ETag` header.
`PATCH /api/v2/ebooks/{content_id}/annotations/{annotation_id}` and `DELETE`
require this validator in `If-Match`. Missing validators return 428; stale
validators return 412. Optional `If-None-Match` is evaluated second.
Guards run under the same database row lock used by legacy edits.
Successful deletion returns a bodyless 204; a missing annotation returns 404.

PATCH omission retains a field, explicit null clears a string, and metadata null
clears the client-owned object. The merged annotation must still have a valid
kind and a CFI range or location. Create and edit requests are capped at 256 KiB.
All operations check current item access before reaching scoped annotation storage.
The frozen v1 routes retain their existing shapes and unguarded semantics.
