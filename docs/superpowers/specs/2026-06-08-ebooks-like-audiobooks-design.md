# Ebooks Like Audiobooks Design

## Goal

Add ebooks to silo by matching the audiobook architecture in `main` as closely
as possible. Ebooks are a core media type in `silo-server`; only metadata
provider lookup lives in a plugin.

## Non-Goals

- Do not add or keep a scanner/runtime plugin named `silo-plugin-ebooks`.
- Do not treat ebooks as an audiobook sub-feature.
- Do not add ebook narrators.
- Do not add ebook ASIN handling.
- Do not introduce an ebook details repository/table unless the audiobook path
  gains the same shape first.

## Audiobook Pattern In Main

Audiobooks have three cooperating pieces:

1. Core scan:
   - `internal/scanner/scanner.go` routes `audiobook` and `audiobooks` library
     types to `ScanAudiobookFolder`.
   - `internal/scanner/audiobook_scan.go` writes `media_items`,
     `media_files`, `item_people`, `audiobook_series`,
     `media_item_libraries`, and durable provider IDs.
2. Core metadata enrichment:
   - `cmd/silo/main.go` wires `audiobooks.NewEnricher`.
   - `internal/taskmanager/tasks/sync_audiobook_metadata.go` registers the
     periodic/manual task.
   - `internal/audiobooks/enrichment.go` sweeps unenriched `audiobook` items,
     resolves the configured metadata provider chain at
     `content_level = 'audiobook'`, calls `Search` then `GetMetadata`, and
     persists provider IDs, poster/overview/scalar metadata, and people.
3. First-party metadata plugin:
   - `silo-plugin-audiobook-metadata` exposes `metadata_provider.v1` with an
     audiobook capability and provider implementations.

## Ebook Equivalent

Ebooks should mirror that shape:

1. Core scan:
   - `internal/scanner/scanner.go` routes `ebook` and `ebooks` libraries to an
     ebook scan path.
   - `internal/scanner/ebook.go` parses local ebook metadata from supported
     files.
   - `internal/scanner/ebook_scan.go` writes:
     - `media_items.type = 'ebook'`
     - `media_files.base_type = 'ebook'`
     - `item_people` author credits only
     - `ebook_series`
     - `media_item_libraries`
     - `media_item_provider_ids` using provider `isbn` when an ISBN is parsed
2. Core metadata enrichment:
   - Add `internal/ebooks/enrichment.go`, shaped after
     `internal/audiobooks/enrichment.go`.
   - Add `internal/taskmanager/tasks/sync_ebook_metadata.go`, shaped after
     `sync_audiobook_metadata.go`.
   - Wire `ebooks.NewEnricher` in `cmd/silo/main.go` next to the audiobook
     enricher.
   - Resolve provider chains at `content_level = 'ebook'`.
   - Search and fetch metadata with `ContentType = "ebook"`.
   - Persist provider IDs, poster/overview/scalar metadata, and author people.
3. First-party metadata plugin:
   - Use `silo-plugin-ebook-metadata` as the first-party ebook metadata
     provider plugin.
   - The core app should rely on that plugin for provider-specific metadata
     lookups, just as audiobooks rely on `silo-plugin-audiobook-metadata`.

## Ebook Domain Rules

- Ebooks have ISBNs.
- Ebooks have authors.
- Ebooks do not have narrators.
- Ebooks do not have ASINs.
- Ebook metadata plugins may return provider IDs, but core scanner-derived ISBN
  should be stored under provider `isbn`.
- Local parsing may read embedded ebook metadata from EPUB/FB2 and format/file
  facts from other supported ebook formats.

## First PR Scope

The first implementation PR should establish the mirrored foundation:

- ebook library type and walk mode
- local ebook parser
- ebook scan routing and persistence
- `ebook_series` migration
- tests proving ebook authors-only behavior, ISBN provider ID behavior, and
  scanner-owned ebook series behavior

Deferred follow-up PR scope:

- ebook metadata enrichment package/task/wiring, analogous to
  `internal/audiobooks.Enricher`
- plugin chain resolution at `content_level = 'ebook'`

UI/detail page, ebook reader experience, ABS ebook compatibility endpoints, and
advanced provider-specific plugin improvements can be separate PRs unless they
are required to prove the core path works.
