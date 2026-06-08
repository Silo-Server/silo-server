# Ebook Reader Full Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring the core ebook reader up to the personal reader feature set with server-persisted config, annotations/bookmarks, selection tools, reading aids, advanced settings, and diagnostics.

**Architecture:** Keep ebook reading in core, matching audiobooks: core owns the ebook media type and reader behavior; metadata remains plugin-provided. Extend the existing `/ebooks/{content_id}` reader API and `EbookReaderHandler` rather than creating a separate plugin-style reader subsystem. Persist per-user/per-profile/per-book state in Postgres so settings, annotations, and bookmarks roam across devices.

**Tech Stack:** Go/Chi handlers, pgx/Postgres migrations, React 19, TanStack Query, Foliate/readest reader wrapper, Vitest, Go unit tests.

---

## File Structure

- `migrations/sql/20260608000300_ebook_reader_state.sql`
  - Creates `ebook_reader_config` and `ebook_reader_annotations`.
- `internal/api/handlers/ebook_reader.go`
  - Adds reader config and annotation types, store interfaces, handlers, validation, and Postgres store methods.
- `internal/api/handlers/ebook_reader_test.go`
  - Adds handler tests for config, annotation CRUD, access scoping, and validation.
- `internal/api/router.go`
  - Wires config/annotation stores and routes under `/ebooks/{content_id}`.
- `web/src/reader/FoliateBookReader.tsx`
  - Adds selection/annotation/readable-text/content-popup reader handles and applies saved config.
- `web/src/pages/EbookReader.tsx`
  - Adds server-backed settings, annotation/bookmark UI, selection tools, TTS controls, wake-lock/e-ink controls, and diagnostics affordances.
- `web/src/hooks/useTTS.ts`, `web/src/hooks/useScreenWakeLock.ts`, `web/src/hooks/useEinkMode.ts`
  - Port focused personal-reader hooks into core web.
- `web/src/reader/ebookReaderApi.ts`
  - Centralizes reader config and annotation API calls for tests and page usage.
- `web/src/pages/EbookReader.test.tsx`, `web/src/reader/FoliateBookReader.test.ts`
  - Adds UI and wrapper tests.

## Task 1: Server-Persisted Reader Config

**Files:**
- Create: `migrations/sql/20260608000300_ebook_reader_state.sql`
- Modify: `internal/api/handlers/ebook_reader.go`
- Modify: `internal/api/handlers/ebook_reader_test.go`
- Modify: `internal/api/router.go`
- Create: `web/src/reader/ebookReaderApi.ts`
- Modify: `web/src/pages/EbookReader.tsx`
- Modify: `web/src/pages/EbookReader.test.tsx`

- [ ] **Step 1: Write failing Go handler tests**

Add tests that:
- `GET /ebooks/{content_id}/reader-config` returns `{ "config": {} }` when no row exists.
- `PUT /ebooks/{content_id}/reader-config` saves JSON config for the authenticated user/profile/content.
- inaccessible content returns 404 through the existing `ItemAccess` path.
- invalid non-object config returns 400.

Run:

```bash
go test ./internal/api/handlers -run 'TestEbookReader.*Config' -count=1
```

Expected: fail because config store/handlers do not exist.

- [ ] **Step 2: Add migration**

Create `ebook_reader_config`:

```sql
CREATE TABLE ebook_reader_config (
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    config JSONB NOT NULL DEFAULT '{}'::jsonb,
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    PRIMARY KEY (user_id, profile_id, content_id)
);

CREATE INDEX ebook_reader_config_profile_updated
    ON ebook_reader_config (user_id, profile_id, updated_at DESC);
```

- [ ] **Step 3: Implement config store and handlers**

Add `EbookReaderConfig`, `EbookReaderConfigStore`, `HandleGetConfig`, `HandleSaveConfig`, and `PGEbookReaderConfigStore` methods in `ebook_reader.go`.

Validation:
- authenticated user required
- `content_id` required
- `config` must decode to a JSON object
- content access checked with `FileAuthorizer.ItemAccess.EnsureAccessible`

- [ ] **Step 4: Wire routes and store**

In `internal/api/router.go`, create the store when `deps.DB != nil`, assign it to `ebookReaderHandler`, and add:

```go
r.Get("/{content_id}/reader-config", ebookReaderHandler.HandleGetConfig)
r.Put("/{content_id}/reader-config", ebookReaderHandler.HandleSaveConfig)
```

- [ ] **Step 5: Add web API wrapper**

Create `web/src/reader/ebookReaderApi.ts` with:

```ts
export function ebookReaderConfigPath(contentID: string): string
export async function fetchEbookReaderConfig(contentID: string): Promise<Record<string, unknown>>
export async function saveEbookReaderConfig(contentID: string, config: Record<string, unknown>): Promise<Record<string, unknown>>
```

- [ ] **Step 6: Move settings persistence from localStorage to server**

Update `EbookReader.tsx` to fetch reader config on load and save settings through the config endpoint. Keep a local fallback only until the server value arrives.

- [ ] **Step 7: Verify and commit**

Run:

```bash
go test ./internal/api/handlers -run 'TestEbookReader.*Config' -count=1
cd web && pnpm test src/pages/EbookReader.test.tsx src/reader/FoliateBookReader.test.ts --run
cd web && pnpm build
```

Commit:

```bash
git add migrations/sql/20260608000300_ebook_reader_state.sql internal/api/handlers/ebook_reader.go internal/api/handlers/ebook_reader_test.go internal/api/router.go web/src/reader/ebookReaderApi.ts web/src/pages/EbookReader.tsx web/src/pages/EbookReader.test.tsx
git commit -m "feat: persist ebook reader config"
```

## Task 2: Server-Persisted Annotations And Bookmarks

**Files:**
- Modify: `migrations/sql/20260608000300_ebook_reader_state.sql`
- Modify: `internal/api/handlers/ebook_reader.go`
- Modify: `internal/api/handlers/ebook_reader_test.go`
- Modify: `internal/api/router.go`
- Modify: `web/src/reader/ebookReaderApi.ts`
- Modify: `web/src/reader/FoliateBookReader.tsx`
- Modify: `web/src/pages/EbookReader.tsx`

- [ ] **Step 1: Write failing annotation handler tests**

Cover list, create, update, delete. Test annotation fields:

```json
{
  "kind": "highlight",
  "cfi_range": "epubcfi(/6/4,/1:0,/1:12)",
  "selected_text": "sample text",
  "note": "note text",
  "style": "highlight",
  "color": "#facc15"
}
```

Run:

```bash
go test ./internal/api/handlers -run 'TestEbookReader.*Annotation|TestEbookReader.*Bookmark' -count=1
```

Expected: fail because annotation APIs do not exist.

- [ ] **Step 2: Add annotation table**

Extend migration with:

```sql
CREATE TABLE ebook_reader_annotations (
    id TEXT PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    content_id TEXT NOT NULL REFERENCES media_items(content_id) ON DELETE CASCADE,
    kind TEXT NOT NULL CHECK (kind IN ('highlight', 'note', 'bookmark')),
    cfi_range TEXT,
    location TEXT,
    selected_text TEXT NOT NULL DEFAULT '',
    note TEXT NOT NULL DEFAULT '',
    style TEXT NOT NULL DEFAULT 'highlight',
    color TEXT NOT NULL DEFAULT '#facc15',
    metadata JSONB NOT NULL DEFAULT '{}'::jsonb,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    updated_at TIMESTAMPTZ NOT NULL DEFAULT NOW(),
    CHECK ((kind = 'bookmark' AND location IS NOT NULL) OR (kind <> 'bookmark' AND cfi_range IS NOT NULL))
);

CREATE INDEX ebook_reader_annotations_book
    ON ebook_reader_annotations (user_id, profile_id, content_id, updated_at DESC);
```

- [ ] **Step 3: Implement annotation store and handlers**

Add handlers:
- `GET /ebooks/{content_id}/annotations`
- `POST /ebooks/{content_id}/annotations`
- `PATCH /ebooks/{content_id}/annotations/{annotation_id}`
- `DELETE /ebooks/{content_id}/annotations/{annotation_id}`

- [ ] **Step 4: Wire Foliate annotation drawing and selection**

Extend `FoliateBookReaderHandle` with:
- `createSelectionAnnotation()`
- `clearSelection()`

Draw stored annotations using Foliate `addAnnotation`.

- [ ] **Step 5: Add reader UI**

Add:
- selection popover
- highlight button
- note button
- bookmark button
- annotation list in the side panel
- delete/update note controls

- [ ] **Step 6: Verify and commit**

Run handler tests, web tests, and build. Commit:

```bash
git commit -m "feat: add ebook annotations and bookmarks"
```

## Task 3: Reader Tools, TTS, And Reading Aids

**Files:**
- Create: `web/src/hooks/useTTS.ts`
- Create: `web/src/hooks/useScreenWakeLock.ts`
- Create: `web/src/hooks/useEinkMode.ts`
- Modify: `web/src/reader/FoliateBookReader.tsx`
- Modify: `web/src/pages/EbookReader.tsx`
- Modify: `web/src/pages/EbookReader.test.tsx`

- [ ] **Step 1: Port focused hooks from personal reader**

Bring over:
- Web Speech TTS controller
- screen wake lock hook
- e-ink body-class hook

- [ ] **Step 2: Add readable text handle**

Expose `getReadableText()` from `FoliateBookReader` for TTS.

- [ ] **Step 3: Add UI controls**

Add side-panel controls for:
- Speak current text
- Pause/resume/stop
- voice/rate/pitch
- wake lock
- e-ink mode

- [ ] **Step 4: Add content popups and helpers**

Add selection popover actions:
- define
- translate

Add reader content popup handling for footnotes/images/tables when Foliate emits them.

- [ ] **Step 5: Verify and commit**

Run focused web tests and build. Commit:

```bash
git commit -m "feat: add ebook reader tools and aids"
```

## Task 4: Advanced Settings And Diagnostics

**Files:**
- Modify: `web/src/reader/FoliateBookReader.tsx`
- Modify: `web/src/pages/EbookReader.tsx`
- Modify: `web/src/pages/EbookReader.test.tsx`

- [ ] **Step 1: Add advanced reader settings**

Add server-persisted settings for:
- RTL
- writing mode
- zoom/scale
- brightness
- hyphenation toggle
- custom font selection/upload if API support is added in a later task

- [ ] **Step 2: Add diagnostics**

Emit local diagnostics for:
- file load start/success/failure
- config load/save
- progress save
- annotation create/update/delete
- TTS state changes

- [ ] **Step 3: Add diagnostics UI**

Add a compact diagnostics panel in the reader side panel. Keep it hidden unless opened.

- [ ] **Step 4: Verify and commit**

Run focused tests and build. Commit:

```bash
git commit -m "feat: add ebook reader diagnostics"
```

## Self-Review

- The plan covers server persistence first, matching the user's explicit preference.
- The plan keeps ebook features in core and metadata in plugins, matching the audiobook pattern.
- The plan avoids extra ebook catalog tables; added tables are reader-user-state only.
- The biggest implementation risk is annotation rendering against Foliate selection APIs; Task 2 isolates it after backend persistence exists.
- The migration filename is intentionally after existing ebook migrations and can be adjusted if another migration is added before implementation.
