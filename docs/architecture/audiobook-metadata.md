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
- its `status` is `matched`, which Match Item writes through the metadata
  pipeline in the same transaction as the match, whatever IDs it carries.

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

The sweep does not run again on a matched book. A book whose fields were
mixed before this rule existed stays that way until an admin runs an item's
Refresh Metadata in `reset` mode or Match Item.

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
  one. Behind a series tag's prefix the tags may agree less exactly. `album`
  also wins when it differs from the rest of `title` only in punctuation,
  when `title` repeats the series or `album` after it
  (`Saga Name 2 - Book Title Saga Name`), or when it adds a subtitle. When
  `album` instead goes on to name the series or a position
  (`Book Title: Saga Name 2`, `Book Title, Part 2`), `title` without its
  prefix wins. A numbered prefix that names the series among other words
  (`The Saga Name 2`) counts when `album` follows it. A prefix carrying the
  series position tag's number, in digits or as a Roman numeral, was composed
  from the two tags, so `title` without it wins whatever `album` says, and
  when there is no `album`. Otherwise `title` stays, because a single file's `album` may name its
  series or a collection. A multi-file book whose part titles name the parts
  uses `album`.
- Authors: the `artist` tag, as in Audiobookshelf. `album_artist` is read when
  `artist` is empty, or when it names some of the people in `artist` and no
  one else (`artist` then also lists the narrator). inAudible writes the
  series to `album_artist`, so it never wins over an `artist` naming someone
  else. `composer` is the last resort.
- Series without a position: a `grouping` tag counts only when it ends in a
  position, because it often holds a category or a work name, or when
  `album_artist` repeats it and no author's name appears in it, as inAudible
  writes a series.
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
book only when its files change or a metadata refresh reads its tags again in
place: the library Refresh Metadata in `full` mode, or an item's Refresh
Metadata, as the next section describes.

## Reading the tags again

A scan skips a book whose files match their stored size and modification
time (`audiobookFolderShouldSkip`), so a change to how tags are read reaches
an unchanged book only when someone asks for it. A metadata refresh marks its
scan so the scanner reads books again as if their files had changed:
`scanner.WithAudiobookTagReread` names one book, and the scan skips every
other unchanged book it reaches, such as loose books beside it at the library
root; `scanner.WithAudiobookLibraryTagReread` covers every book the scan
reaches. Scheduled and library scans, real-time monitoring, and autoscan
never set either mark.

The book keeps its item: the scan finds it by the root path of its stored
files, so the content ID, file rows, series row, progress, and favorites
stay, and the ownership rules above and admin field locks decide which
fields the tags write. Ebook scans ignore the mark.

## Admin Refresh Metadata

An admin refresh never sends an audiobook to the general metadata service
(`metadata.MetadataService`): its matching and its item rebinding know none
of the rules here, so it could retitle a book, replace tagged credits, match
another edition, match an untagged book by title alone, or fold a book into
another library's item. Provider work on an audiobook goes through the
enricher (`internal/audiobooks/enrichment_refresh.go`).

- Library Refresh Metadata in `full` mode (`library_refresh`, and the manual
  Refresh All Library Metadata task, which runs it for each library) on an
  audiobook library runs one library scan under
  `WithAudiobookLibraryTagReread`. The job runs it directly, as one scan run
  with the `admin_library_refresh` trigger, so the scanner's worker pool reads
  every book and the walk, grouping, held folders, and missing-file
  reconciliation are those of any library scan. The job reports the scan's
  own progress, and the scan stops when its scan run is canceled or failed
  elsewhere. When a library scan of the library is already accepted or
  running (`idx_scan_runs_active_scope`), the job fails with
  `ErrLibraryScanInProgress` instead of starting a second scan. After the
  scan, `Enricher.RequeueUnmatchedInLibrary` makes every unmatched book due
  in one statement: its
  `no_match`, `failed`, or `skipped` outcome and retry counts are cleared, so
  the next sweep tries it under these rules. A matched book keeps its match
  and provider-owned fields; a book whose match an admin ignored
  (`match_ignored_at`) stays as it is. The job asks no provider. `quick` mode
  leaves audiobooks out.
- An item's Refresh Metadata first claims the book (`Enricher.ClaimRefresh`)
  and holds the claim until it ends, so the sweep cannot take the book
  between its tag read and its provider step. A delegated curator's refresh
  claims, and writes, only while every library holding the book is in the
  curator's scope (`catalog.GuardMetadataCurationItems`, checked again inside
  each write transaction).
- `quick` mode reads the book's tags again in its scan, then calls
  `Enricher.Refresh`. A matched book fetches the records its stored IDs name,
  without searching, and they pass the same admission checks as a sweep
  match; when no provider returns them the book keeps its match and fields
  and the job fails. An unmatched book is matched as one sweep item would be.
  An ignored book is left alone.
- `reset` mode clears the match (`Enricher.ResetMatchTx`): provider IDs,
  author and narrator credits, the fields a match owns, artwork a provider
  supplied, the field locks, and the enrichment state. It does so inside the
  scanner transaction that writes the book's re-read tags
  (`scanner.WithAudiobookTagRereadReset`), after the files were read, so a
  scan that fails or is canceled leaves the book as it was. A cleared cached
  poster is queued for collection by `media_items_artwork_revision_gc_update`
  in that transaction. `Refresh` then matches the book again. The job fails
  when the scan did not read the book (`scanner.AudiobookTagRereadDone`),
  since a scan of a path shared with other books succeeds when only one of
  them failed; the cache invalidation still runs.
- `complete` mode runs as `reset` for an audiobook. A complete refresh clears
  its scope's content links before it scans, which would give the book a new
  content ID and orphan its progress and favorites.

None of these changes a book's content ID, files, library membership,
progress, or favorites, or merges it into another item.

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

The gate (`internal/audiobooks/enrichment_gate.go`) pauses a provider that
answers rate limited, for every call. It counts outages separately for the
provider's two routes, search and metadata: three transient failures in a row
on one route pause that route, for 15 minutes at first and twice as long each
time it is still failing. A paused search leaves lookups by a tagged ASIN
running, so those books keep matching while a book that needs the search is
deferred. A paused metadata route stops searches too, since a search result
could not be fetched.

The sweep never claims a book whose match an admin ignored
(`match_ignored_at`).

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
