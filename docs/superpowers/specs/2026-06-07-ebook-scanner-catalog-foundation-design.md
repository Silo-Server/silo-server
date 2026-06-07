# Ebook Scanner And Catalog Foundation Design

## Goal

Add first-party ebook library support to Silo core, scoped to scanner and
catalog foundation. This MR should make local ebook files discoverable as normal
catalog items while preserving enough local metadata for later ebook metadata
provider and reader features.

## Scope

In scope:

- Route `ebook` and `ebooks` media folders through a dedicated ebook scanner.
- Support `.epub`, `.pdf`, `.mobi`, `.azw`, `.azw3`, and `.fb2`.
- Extract local embedded metadata where available: title, authors, publisher,
  year/release date, language, genres, ISBN, description, series, series
  position, page count, and cover image.
- Use sidecar covers when there is no embedded cover: `cover.jpg`,
  `cover.png`, or `folder.jpg`.
- Persist ebooks as first-party catalog rows using existing shared tables:
  `media_items`, `media_files`, `media_item_libraries`, `item_people`, and
  `media_item_provider_ids`.
- Add an ebook-specific details table for metadata that does not fit cleanly
  into `media_items` or `media_files`, such as format, page count, ISBN,
  publisher, series name, and series index.
- Make ebooks visible through existing catalog browse/search surfaces.

Out of scope:

- Ebook reader UI.
- OPDS, Kobo, Kindle, send-to-device, or sync integrations.
- Reading progress, annotations, bookmarks, highlights, or reading stats.
- Ebook request workflows.
- External metadata enrichment. The scanner stores ISBN/provider hints so
  a later MR can add metadata provider integration.

## Architecture

Ebooks are first-party `media_items` with `type = 'ebook'`. They follow the
audiobook pattern where useful, but stay file-scoped because one ebook file is
one catalog item in this first slice.

New scanner code:

- `internal/scanner/ebook.go` contains supported-extension checks, parsed ebook
  structs, tag normalization helpers, and safe local metadata extraction.
- `internal/scanner/ebook_scan.go` walks ebook libraries, skips unchanged files,
  upserts catalog rows, and writes file, people, provider ID, details, and cover
  data.

New database shape:

- `ebook_details`
  - `content_id text primary key references media_items(content_id) on delete cascade`
  - `format text not null default ''`
  - `isbn text not null default ''`
  - `publisher text not null default ''`
  - `page_count integer not null default 0`
  - `series_name text not null default ''`
  - `series_index text not null default ''`
  - `metadata_json jsonb not null default '{}'::jsonb`
  - timestamps

Shared tables:

- `media_items`: title, sort title, type, year, overview, genres, studios,
  original language, release date, poster path, and status.
- `media_files`: one row per ebook file with `base_type = 'ebook'`,
  file size/mtime/hash, root paths, container/format, and identity confidence.
- `item_people`: authors use `models.PersonKindAuthor`.
- `media_item_provider_ids`: ISBN is stored as a durable provider ID when present.
- `media_item_libraries`: library membership.

Catalog updates add `ebook` to type parsing and API response unions. Ebook
facets in this MR are author, genre, and series. Format-specific filtering is
deferred.

## Identity

The scanner must not merge ebooks by title/year. Books can share titles, unknown
years, editions, translations, or ISBNs that do not map cleanly to one local
copy.

Identity order:

1. Reuse an existing item already linked to the same media folder and file path.
2. Reuse an item already linked to the same observed root path if the file has
   moved within the same root in a way the existing file repository can prove.
3. Otherwise create a new `media_items` row.

ISBN is persisted for later matching, but it does not collapse local items in
this first MR.

## Data Flow

1. `ScanFolder` detects `ebook`/`ebooks` and delegates to `ScanEbookFolder`.
2. The ebook scanner walks configured library paths and considers only regular
   files with supported ebook extensions.
3. For each file:
   - stat file size and mtime
   - skip if the existing `media_files` row is unchanged
   - parse local metadata with panic recovery
   - create or update the `media_items` row
   - write one `media_files` row
   - write `ebook_details`
   - replace author links
   - persist ISBN provider IDs when present
   - cache embedded cover art, or sidecar cover art when embedded cover is absent
   - ensure `media_item_libraries` membership exists
4. Missing files are marked by setting `media_files.missing_since` through the
   existing file repository path. The first MR does not hard-delete ebook
   catalog rows.
5. Metadata enrichment is not queued yet.

## Error Handling

- A broken ebook file logs a warning, increments the failed count, and does not
  stop the whole library scan.
- A parser panic is converted to an error for that file.
- If every attempted ebook file fails, the scanner returns a non-nil aggregate
  error so the scan does not look clean.
- If directory traversal encounters unreadable subtrees, cleanup for unseen
  files is skipped for that scan to avoid marking present files missing because
  of an incomplete walk.
- Cover extraction failures are logged and do not fail the item scan.

## Testing

Parser tests:

- Supported extension routing for EPUB, PDF, MOBI, AZW, AZW3, and FB2.
- Metadata extraction for title, author, year/release date, language, ISBN,
  series, page count, description, and cover where practical fixtures exist.
- Panic recovery and unsupported/corrupt file handling.

Scanner tests:

- Unsupported extensions are ignored.
- Unchanged files are skipped.
- Same-title ebooks in different paths create separate items.
- ISBN is stored as a provider ID but does not force merging.
- Author links are written with `PersonKindAuthor`.
- Embedded cover wins over sidecar; sidecar is used when embedded cover is
  absent.
- All-failed scan returns an aggregate error.
- Unreadable traversal prevents cleanup.

Catalog/API tests:

- `ebook` is accepted as a media scope.
- Ebook items appear in browse/search for ebook libraries.
- Existing movie, series, episode, and audiobook catalog behavior is unchanged.

Migration tests:

- `ebook_details` applies and rolls back cleanly.
- Foreign key cascade removes ebook details when the media item is removed.

## MR Boundaries

This MR ends once local ebook libraries scan into the catalog and can be
browsed/searched. Follow-up MRs can add:

- ebook metadata provider chain integration
- richer ebook facets and detail pages
- reader/OPDS/Kobo/Kindle integrations
- reading progress and annotations
- request workflows
