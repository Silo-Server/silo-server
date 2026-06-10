# Ebook Reader Shell Parity Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring the core ebook reader up to the first useful parity slice from `personal/silo-plugin-ebooks`: TOC, search, progress scrub, reader settings, and keyboard navigation.

**Architecture:** Keep the current core reader API and persistence model. Extend `FoliateBookReader` with imperative methods for TOC, search, href/fraction navigation, and runtime style updates; keep `EbookReader` responsible for the chrome/panels/settings state. Store reader preferences in localStorage for this first local slice so no backend schema is added.

**Tech Stack:** React 19, React Router, TanStack Query, foliate-js custom element, existing Silo UI components, Vitest.

---

### Task 1: Reader Handle Capabilities

**Files:**
- Modify: `web/src/reader/FoliateBookReader.tsx`
- Test: `web/src/reader/FoliateBookReader.test.ts`

- [ ] Add exported types `ReaderSettings`, `ReaderSearchOptions`, `ReaderSearchResult`, and `ReaderReadyState`.
- [ ] Extend `FoliateBookReaderHandle` with `goToFraction`, `goTo`, `search`, `clearSearch`, and `applySettings`.
- [ ] Add a `settings?: ReaderSettings` prop and convert `readerStyles()` to accept settings.
- [ ] Preserve existing progress save/restore behavior.
- [ ] Verify with `pnpm vitest run src/reader/FoliateBookReader.test.ts`.

### Task 2: TOC and Search Panel

**Files:**
- Modify: `web/src/pages/EbookReader.tsx`
- Test: `web/src/pages/EbookReader.test.tsx`

- [ ] Add panel state for `toc`, `searchResults`, `searchTerm`, and current tab.
- [ ] Render side panel tabs for Contents, Search, and Settings.
- [ ] Wire TOC items to `readerRef.current.goTo(href)`.
- [ ] Wire search form to `readerRef.current.search(term)` and result clicks to `goTo(cfi)`.
- [ ] Verify with `pnpm vitest run src/pages/EbookReader.test.tsx`.

### Task 3: Reader Settings

**Files:**
- Modify: `web/src/pages/EbookReader.tsx`
- Modify: `web/src/reader/FoliateBookReader.tsx`
- Test: `web/src/pages/EbookReader.test.tsx`

- [ ] Add localStorage-backed settings for theme, font size, font family, line height, margin, max width, spread, and flow.
- [ ] Render compact controls in the Settings tab.
- [ ] Apply settings live through `FoliateBookReader`.
- [ ] Verify settings persist across remount in tests.

### Task 4: Progress Scrub and Keyboard Navigation

**Files:**
- Modify: `web/src/pages/EbookReader.tsx`
- Modify: `web/src/reader/FoliateBookReader.tsx`
- Test: `web/src/pages/EbookReader.test.tsx`

- [ ] Add a progress range input in the header.
- [ ] Call `goToFraction` on commit/change and update display from relocate events.
- [ ] Add ArrowLeft/ArrowRight keyboard navigation on the reader page.
- [ ] Verify with page-level tests.

### Task 5: Local Verification

**Files:**
- No source changes.

- [ ] Run `cd web && pnpm run build`.
- [ ] Run `GOWORK=off go test ./internal/api/handlers ./internal/scanner`.
- [ ] Keep changes local; do not push.
