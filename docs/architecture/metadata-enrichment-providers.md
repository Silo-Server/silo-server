# Enrichment-only metadata providers

An enrichment-only provider is a `metadata_provider.v1` plugin capability that
never identifies items. It fills fields other providers left empty, and it looks
an item up by other providers' IDs. The MDBList plugin is one: it adds ratings,
advisory ages and a few fill-empty fields to titles that TMDB or TVDB already
matched.

A capability declares this in its manifest metadata. The host reads each key at
the top level or inside the SDK's `metadata` envelope; the top level wins.

- `lookup_provider_ids` is a list of provider-ID keys, such as
  `["imdb", "tmdb"]`. The host calls `GetMetadata` when the item carries any of
  them, even without an ID of the provider's own. In that case `provider_id` is
  empty and the plugin reads `provider_ids`.
- `bulk_lookup_limit` is a positive integer: how many concurrent `GetMetadata`
  calls the provider combines into one upstream request. Declaring it opts the
  provider into the bulk enrichment pass below. Values above 200 are capped at
  200; anything that is not a positive integer opts out.

## Error contract for bulk providers

Declaring `bulk_lookup_limit` also promises this: a provider returns an item
with no data only when the source has nothing for the title. It reports
everything else as a gRPC status, because the pass records an empty answer as
"nothing to find" and does not ask again for 30 days.

| Situation | Status |
|---|---|
| Quota or rate limit spent | `RESOURCE_EXHAUSTED` |
| Upstream outage | `UNAVAILABLE` |
| Missing or rejected credentials | `FAILED_PRECONDITION` or `UNAUTHENTICATED` |
| A problem with this one item | `INVALID_ARGUMENT`, `NOT_FOUND` or `INTERNAL` |

During matches and refreshes the host treats a provider error as a warning and
continues with the other providers. From an enrichment-only provider, the
provider-wide statuses above (`RESOURCE_EXHAUSTED`, `UNAVAILABLE`,
`FAILED_PRECONDITION`, `UNAUTHENTICATED`, `PERMISSION_DENIED`) are logged at
debug level instead. A small daily quota runs out routinely, so the plugin
should log when it stops answering, and the host does not log once per item.

## Bulk enrichment pass

The `bulk_metadata_enrichment` task runs hourly on one server at a time, held by
a PostgreSQL advisory lock. For each opted-in provider, it finds the matched
movies and series that:

- belong to a library whose movie or series provider chain includes the
  provider, resolved exactly as a refresh resolves it;
- carry at least one of the provider's lookup IDs;
- have no settled answer from the provider in `metadata_enrichment_state`.

It then pages through them in content ID order. Each page has
`bulk_lookup_limit` items, all looked up at once, which is what lets the
provider fill its upstream batches. A page's results are written by four
workers, so the lookup concurrency does not become database concurrency.

Each result merges into the item with the fill-empty rules and field locks of a
scheduled refresh. The write is not a refresh, though. It leaves these alone:

- refresh bookkeeping: `last_refreshed`, `refresh_failures`, `matched_at`,
  `status`, and refresh debt;
- identity: the stored provider IDs, with none added from the provider's
  answer, and no rebinding or promotion of a local content ID;
- artwork: paths, source paths and thumbhashes stay as stored, including
  artwork still waiting to be cached;
- people and remote videos. A refresh replaces those wholesale from every
  provider's answer, so one provider's answer would wipe the rest;
- series seasons and episodes, and the episode-completeness state
  (`episode_metadata_incomplete`) that only a refresh which rewrites episodes
  recomputes.

`metadata_enrichment_state` records one row per item and provider:

- `found`: the provider returned data. Never looked up again.
- `empty`: the provider had nothing. Looked up again after 30 days.
- `failed`: this item's lookup or write failed. Tried again after a day.

A match or refresh that runs the provider in its chain also records `found`,
so the pass does not repeat that lookup.

An error that concerns the provider as a whole ends the provider's turn and
records nothing for the affected items. That covers every status not listed as
per-item above, a failure to reach the plugin, and shutdown. Answers already
received are kept. The next run starts again from the items with no settled
answer, so the pass resumes after a restart, a cancellation or a spent quota.
