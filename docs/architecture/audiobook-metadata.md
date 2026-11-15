# Audiobook metadata ownership

Two writers fill an audiobook's `media_items` row. The scanner
(`internal/scanner/audiobook_scan.go`) reads the files' tags on every scan
that sees a change. The enricher (`internal/audiobooks/enrichment.go`) writes
a metadata provider's record once, when it matches the book. Each field has
one owner, so a rescan never leaves a book describing two sources.

## Matched means a provider identity

A book is provider-matched when any of these holds:

- it has a `media_item_provider_ids` row other than `asin` (the scanner stores
  the ASIN tag as an `asin` row, so that row alone is not a match);
- its last enrichment outcome in `audiobook_enrichment_state` is `success`,
  which covers an automatic match that stored only the ASIN, such as a lookup
  by the tagged ASIN or a match whose provider ID another item already owns;
- its `status` is `matched`, which Match Item and Refresh Metadata write
  through the metadata pipeline in the same transaction as the match,
  whatever IDs it carries.

`catalog.AudiobookProviderMatchedWhere` is the one predicate; the scanner and
the admin unmatched list both use it.

## Who owns which field

Admin field locks win over both writers.

| Field | Unmatched book | Matched book |
| --- | --- | --- |
| Title, original title, series | tags | tags (the enricher never writes them) |
| Year, genres, studios | tags; year follows the tag even when the tag is empty, genres and studios merge | provider; tags fill only an empty field |
| Authors, narrators | every tagged author and narrator (multi-value tags are split) replaces the stored credits when they differ | provider; tagged credits are added only for a kind (author or narrator) the book has no credit for |
| Overview, release date, language | tags fill an empty field | provider; tags fill an empty field |
| Cover | the folder image or embedded art | unchanged; a provider poster fills only a missing cover |
| Runtime | the active files | the active files |

A match replaces credits per kind: only the kinds the provider names. A
provider without narrators keeps the narrators from the tags. A record
reached through the book's tagged ASIN is matched even when its narrators
contradict the narrator tag (taggers often write the author there), so its
narrators replace the tagged ones like any other match's. The credits are
written in the same transaction as the rest of the match and its `success`
outcome. When any part of that write fails, the transaction rolls back and the
failure is recorded like a provider failure, so the book stays unmatched and
is retried under the enrichment backoff. When the
enricher writes a poster it also writes that poster's thumbhash, empty for an
uncached remote URL, which image caching fills in later.

When a folder is renamed or moved, the scanner looks for the same recording
under another path. It reuses an item only when the item's credits contain
every tagged credit and the item's indexed files are gone from disk. The year
it compares is the tag year stored on the files (`media_files.base_year`), not
`media_items.year`, which a match owns.
Files indexed before `base_year` existed hold 0 until their folder is
rewritten; for those the check falls back to `media_items.year`.
Rename detection still depends on the stored author and narrator matching
the tags, so a matched book whose provider spells them differently gets a new
item when its folder is renamed.

The enricher does not run again on a matched book. A book whose fields were
mixed before this rule existed stays that way until an admin runs Refresh
Metadata or Match Item.

## Reading the tags

`parsedAudiobook.populateFromTags` (`internal/scanner/audiobook.go`) reads a
book's tags from its first part; the rules for tags that Audible export tools
write are in `internal/scanner/audiobook_tags.go`:

- Title: the `title` tag, unless `album` is the cleaner name. `album` wins
  when `title` is `album` behind a series prefix (`Saga Name 2 - Book Title`),
  or when `album` is `title` plus a subtitle (`Book Title: The Long Subtitle`).
  The prefix, followed by `-` or `:`, is the book's series tag with an
  optional position; a book without a series tag accepts any text ending in a
  number. A "subtitle" that names the series or ends in a position is not
  one. Otherwise `title` stays, because a single file's `album` may name its
  series or a collection. A multi-file book whose part titles name the parts
  uses `album`.
- Narrators: an explicit narrator tag (`narrator`, the MP4 `©nrt` atom, or
  `narratedby`) when present. Otherwise the first of `performer` and
  `composer` that names someone other than the tagged authors: some files put
  the author in `performer` and the narrator in `composer`. When both name
  only authors, the book is read by its author and `performer` is used.
- Genres: when any `tmp_genreN` tag is present, those tags are the genres, one
  each, with a trailing `;` or `,` dropped. Otherwise `genre` splits on `/` and
  `;` when it holds either, and on `,` when not. The comma split keeps the
  Audible names `Mystery, Thriller & Suspense` and
  `Relationships, Parenting & Personal Development` whole.

These rules decide what the tags say. The table above still decides whether
the tags or a provider's record fills each field.

An unchanged book is skipped on rescan, so a change to these rules reaches a
book only when its files change or an admin runs a complete Refresh Metadata
on it, which clears the book's file links and makes the next scan read its
tags again.

## The enrichment sweep

The scheduled `sync_audiobook_metadata` task runs `Enricher.Run`
(`internal/audiobooks/enrichment_sweep.go`). One node sweeps at a time: the
run holds a Postgres advisory lock on a session of its own, and a node that
finds it taken does nothing. A run claims a batch of due books
(`SILO_AUDIOBOOK_ENRICH_BATCH_SIZE`), enriches it, and claims the next one
after a short gap, so a large backlog drains at the rate the provider plugin's
own limiter allows rather than one batch per scheduled run. It stops when:

- the run is canceled (shutdown, or a runtime limit set on the task);
- a batch comes back short or empty, so nothing more was due;
- no book in the batch reached an outcome: all were deferred, as when the
  provider gate has paused the whole chain, or failed;
- the lock's session is gone, since another node may then take the lock.

A batch that still matched books while one provider was paused goes on; the
gate keeps calls away from the paused provider. Books a pause deferred come
due when the pause ends and the next scheduled run takes them.

## Unmatched audiobooks and `status`

Audiobooks keep `media_items.status` empty. Several consumers read
`'unmatched'` there with meanings that do not fit books: the audiobook skip
check re-probes such a folder on every scan, orphan cleanup deletes
provisional rows, and the movie and series match queues pick them up. The
admin unmatched-items list instead uses `catalog.AudiobookUnmatchedWhere`: a
book that is not matched by the predicate above and whose last enrichment
outcome was `no_match` or `failed` (its providers kept failing until the retry
limit), or which was stamped before `audiobook_enrichment_state` existed and has no
state row. A book in a library with no provider chain (`skipped`) or still
waiting for its first attempt is not listed. Rows from before the state table
recorded no outcome, so a book skipped back then is listed too: it has no
match either, and the enricher no longer revisits it. A match removes a book
from the list.
