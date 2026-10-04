# Recommendation administration API

Recommendation administration uses acting-administrator authorization. All six
routes remain registered when the recommendation worker is absent and return
`503` until it is available. The frozen v1 handlers remain unchanged.

| Endpoint | Result |
| --- | --- |
| `GET /api/v2/admin/recommendations/status` | Counts, last runs, running flags, lock conflict and cache refresh time |
| `POST /api/v2/admin/recommendations/trigger/embeddings` | `200` with `status: "started"` |
| `POST /api/v2/admin/recommendations/trigger/taste-profiles` | `200` with `status: "started"` |
| `POST /api/v2/admin/recommendations/trigger/cowatch` | `200` with `status: "started"` |
| `POST /api/v2/admin/recommendations/trigger/recommendations` | `200` with `status: "started"` |
| `POST /api/v2/admin/recommendations/embeddings/reset` | `200` with the number of rows deleted |

## Status

Status contains `embeddings`, `taste_profiles`, `cowatch`, and `recommendations`.
Each has a `running` boolean and integer `count`; embeddings includes `total`
when nonzero. Counts come from persisted catalog/recommendation data. Running
flags describe the responding process and are sampled separately from counts.
They do not form a consistent cluster-wide snapshot.

Each job also carries `last_run`, the newest finished run recorded by any
server, once one exists: `status` (`completed` or `failed`), `started_at`,
`completed_at`, `error` on a failed run, and `result`, the counts the run
reported. A run that finished with partial failures is `completed` and counts
them in `result`; its keys differ by job and may grow. The error is the job's
own message with credential assignments and URL user info masked. Runs that a
busy job skipped are not recorded.

`lock_conflict` is empty unless the stored embedding lock rejects the
embedding base URL or model this server runs with, or records a vector size
Silo cannot store. It is then a sentence for the administrator that never
quotes a base URL. Embedding settings take effect after a restart, so a saved
but not yet applied change does not show here; the embedding connection check
reports it instead. `cache_refreshed_at` is when the newest cached
recommendation row was written, by the cache job or a profile refresh; it is
absent when nothing is cached.

## Triggers

The embeddings kind also covers the catch-up pass, which every 15 minutes
embeds items that have no embedding or one from another model: while it runs,
an embeddings trigger answers `409`, and the embeddings `last_run` may be a
catch-up run, marked `missing_only: true` in `result`. Catch-up runs that found
nothing to do are not recorded; failed ones are, so a lock conflict shows as a
failed run every 15 minutes until it is resolved.

Trigger success means the responding process claimed its running flag and the
job kind's cluster-wide lock, then launched background work. It does not mean
that work completed. There is no job ID, Location header, durable acceptance, or
automatic recovery promise. A trigger returns `409` while the same kind is
running in that process or on another server; the problem detail says which.
Failing to take the lock returns `500`. Scheduled runs take the same lock, so
each kind runs on one server at a time. A restart loses the local running flag
and releases any lock the process held.

## Embeddings reset

The reset deletes the embedding lock, every item embedding, every taste
profile and taste cluster, and every profile's cached rows in one
transaction, and answers with the counts it deleted (`embeddings`,
`taste_profiles`, `taste_clusters`, `cached_rows`). It is the recovery path
for a server whose lock pins a model it can no longer use, and the way to
switch embedding models. Global rows (popular, recently added, top rated,
genre samplers) and co-watch pairs do not use embeddings and stay.

The reset runs synchronously while holding the embeddings, taste profile and
recommendation jobs' claims and the stale profile sweep's lock, so none of
them runs beside it on any server. It returns `409` while any of them is
running and `500` when the transaction fails, which leaves every row in place.
A profile refresh that was already running when the reset committed can still
write one taste profile from the old embeddings; the profile's next refresh
after re-embedding replaces it.

Every item that lost its embedding gets a catalog search index update, so the
next index syncs replace its stored vector with none. Semantic search stays
off until the new model covers enough of the catalog again. The embedding
catch-up pass starts re-embedding the catalog within 15 minutes, or run the embedding job
after the reset; the next embedding writes a new lock for the configured model.

## Clients

The four triggers and the reset are non-retryable. The web disables both
TanStack mutation retries and authentication refresh/replay for these actions,
including after `401`. After an uncertain result, inspect status before
deciding whether to trigger again. The status query continues to poll every
five seconds. The web asks for confirmation before a reset and disables it
while the embeddings, taste profile or recommendation job runs on the
responding process.

These existing administration flows have no Apple or Android callers and no
Jellyfin compatibility equivalent. Viewer recommendation endpoints are separate.
