# Because You Watched Automatic Anchor Rollup Design

## Problem

Automatic `because_you_watched` sections use the three most recently completed leaf item IDs as recommendation anchors. Completed TV episodes therefore reach the recommendation engine as episode IDs even though recommendation embeddings are generated for their parent series. The cache worker cannot generate results for those episode anchors, and the section reader looks up the same empty cache keys, so the Web UI hides the empty section.

The fix is limited to automatically selected anchors. Explicitly configured anchors retain their current semantics.

## Design

`SignalReader.RecentCompletedItemIDs` will return recommendation-ready automatic anchor IDs rather than raw completed leaf IDs. Episodes will resolve to their parent series. Movies, series, audiobooks, and ebooks will retain their original IDs.

Canonical anchors will remain ordered by the newest completion activity. Multiple completed episodes from the same series will collapse to one series anchor, retaining the position of the newest episode. The requested limit will apply after canonicalization and deduplication, so repeated episodes from one series do not prevent older distinct titles from filling the automatic anchor set.

Both supported signal sources will implement these semantics:

- The repository-backed path will select canonical activity IDs from `watched_activity`, group repeated IDs by their newest activity timestamp, and apply the limit after grouping.
- The user-store path will page completed progress in recency order, resolve episode IDs through the catalog, merge completed ebook progress, deduplicate canonical IDs, and stop once it can prove that older pages cannot change the limited result.

The cache worker and section reader will continue consuming `RecentCompletedItemIDs` without adding their own normalization. This keeps cache generation and lookup on identical series keys and makes the signal boundary the single owner of automatic-anchor selection.

## Error Handling

Catalog resolution and signal-store failures will be returned to the caller. Existing worker behavior will log the failure and skip the affected profile refresh; existing reader behavior will return the error. Unknown IDs that do not resolve as episodes will remain unchanged, matching the current treatment of non-episode content.

## Compatibility

The change is internal and does not alter the `/api/v1` contract, section configuration schema, or Web UI behavior. Existing automatic movie, series, audiobook, and ebook anchors retain their IDs. Existing explicit `because_you_watched` anchors are outside this change.

## Testing

Tests will verify that:

- recent episodes resolve to their parent series;
- episodes from the same series deduplicate while preserving the newest occurrence;
- deduplication occurs before the requested anchor limit;
- older distinct items fill remaining anchor slots;
- non-episode IDs remain unchanged;
- repository-backed and user-store-backed signals produce the same ordering semantics; and
- the worker and reader use the canonical series IDs as cache keys.

Commands assume the repository root is the current working directory.
