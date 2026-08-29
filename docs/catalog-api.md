# Catalog API

## Saved browse sort

`PUT /api/v1/collections/sort-preference` saves the active profile's sort for a
library collection, user collection, Watchlist, or Favorites. The request is:

```json
{
  "collection_kind": "watchlist",
  "field": "added_at",
  "order": "desc"
}
```

`collection_kind` accepts `library`, `user`, `watchlist`, or `favorites`.
`collection_id` is required for collection kinds and is omitted or ignored for
Watchlist and Favorites. Personal lists accept the same non-personalized sort
fields as the personal catalog browse; `added_at` means the date the item was
added to the list. Personalized sorts (`progress`, `date_viewed`, and `plays`)
are rejected, matching the browse. An empty `field` pins the profile to list
source order. `DELETE /api/v1/collections/sort-preference?collection_kind=watchlist`
removes the saved preference. Collection kinds also require `collection_id` on
DELETE.

When a catalog request has no explicit sort, its saved preference is applied
before the source default. `/api/v1/catalog` reports an applied saved/default
sort as `effective_sort`; source order omits that field. `effective_sort` is
reported the same way for `group=work` requests, and `sort_metrics` on each item
describes the effective sort rather than the (possibly empty) requested one.

## Feature detection

`GET /api/v1/collections/capabilities` returns `sort_preference_kinds`, the
`collection_kind` values this server accepts:

```json
{ "sort_preference_kinds": ["library", "user", "watchlist", "favorites"] }
```

Check it before saving a Watchlist or Favorites preference. The older
`collection_sort_preferences` boolean is also true on servers that predate the
personal-list kinds and reject them with a 400, so it cannot be used to detect
them. When `sort_preference_kinds` is absent, assume `library` and `user` only.
