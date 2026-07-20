# Ebook Reader Appearance: Themes, Custom Fonts, Columns, Justification

Date: 2026-07-20
Status: approved (visual palette review)

Second of the planned reader improvements, following the page-navigation
redesign (this branch stacks on `feat/reader-navigation` because both rework
the reader settings surface). Reading stats, dictionary/translate, and the
PDF engine remain separate future sub-projects.

## Problem

The reader offers three fixed themes (light/sepia/dark), four built-in font
stacks, no multi-column layout, and no justification control. Comparable
readers ship rich paired-palette theme sets, user-supplied fonts, and 1–4
column layouts.

## Design

### Themes

- Ten themes, each defined as a **palette pair** (light + dark variant):
  Default, Sepia, Gray, Dawnlight (Solarized), Ember (Gruvbox), Aurora
  (Nord), Ocean, Meadow, Rosewood, and AMOLED (dark-only: its "light"
  variant is the same true-black palette).
- Palette values are those approved in the visual review (hex pairs for
  background/foreground per variant; highlight/accent derived from the
  existing annotation color logic, unchanged).
- The reader gains a **light/dark variant toggle independent of the app
  theme**. Theme selection = palette name + variant toggle, replacing the
  current single `theme: light|sepia|dark` enum.
- Settings migration: stored `theme` values map as `light → Default/light`,
  `sepia → Sepia/light`, `dark → Default/dark`. Unknown values fall back to
  Default. The persisted settings shape adds `themeName` and `themeVariant`
  and keeps writing the legacy `theme` field for one release so older
  clients/settings snapshots stay coherent.
- Theme colors apply through the existing foliate styling path
  (`applyReaderSettings`); no reader-content CSS rework.

### Custom fonts (per-user, server-stored)

- Upload .ttf/.otf/.woff/.woff2, max 5 MB per file, max 10 fonts per
  profile.
- New API inside the existing `/api/v1/ebooks` route group (same
  `RequireProfile` middleware as all reader endpoints):
  - `GET /api/v1/ebooks/reader-fonts` — list `{ id, name, filename, created_at }`
  - `POST /api/v1/ebooks/reader-fonts` — multipart upload; server validates
    magic bytes (sfnt/woff/woff2 signatures), size, and per-profile count caps;
    display name derived from the sanitized filename (no font name-table
    parsing).
  - `DELETE /api/v1/ebooks/reader-fonts/{id}`
  - `GET /api/v1/ebooks/reader-fonts/{id}/file` — serves the blob with immutable
    cache headers, per-profile authorization (resolved from the session's
    active profile, like the other reader endpoints), and a font content
    type.
- Storage: filesystem at `SILO_READER_FONTS_DIR` (default
  `/var/lib/silo/reader-fonts`, matching the container's `/var/lib/silo/*`
  bind convention), laid out `<user_id>/<profile_id>/<id>.<ext>`, metadata in a new
  `reader_fonts` table (id, user_id, profile_id, name, filename, format,
  size, created_at). **Per-profile scope**, matching the existing reader
  state tables (`ebook_reader_config`, `ebook_reader_annotations`,
  `ebook_reader_progress` are all user+profile scoped) — each household
  profile manages its own font list. Caps apply per profile.
- Reader font picker gains an "Uploaded" group; selecting one injects a
  `@font-face` into the foliate content styles pointing at the file URL.
  Books with embedded fonts keep the existing "Book default" behavior.
- Additive API only; capability discovery via the fonts list endpoint
  itself (404/501 absent on older servers is not a concern — same-build
  web UI).

### Columns and justification

- `columns: auto | 1 | 2 | 3 | 4` replacing/subsuming the current
  `spread` control (foliate `maxColumnCount`); "auto" keeps today's
  width-based behavior. Matches BookOrbit's range; foliate still collapses
  to fewer columns when the viewport can't fit readable lines.
- `columnGap: 0–50%` (default matches foliate's current gap), applied with
  the column count through the content CSS.
- `justify: boolean` toggle beside the existing hyphenation toggle, applied
  through the same content-CSS path.
- Both persist in reader settings like every other knob; both inert for
  comics.

### Settings panel layout

The settings panel groups into "Theme" (palette grid with variant toggle),
"Typography" (font incl. uploads, size, weight, line height, justify,
hyphenate), and "Layout" (margins, width, columns, flow, writing mode,
brightness) — same controls plus the new ones, organized; profiles and
ruler untouched.

## Error handling

- Font upload rejections return structured errors (too large, bad type,
  cap reached); UI surfaces them inline in the picker.
- Deleting a font that's referenced by current settings falls back to
  "Book default" locally.
- Missing/corrupt stored font file → font-face simply fails to load;
  browser falls back through the stack; no reader breakage.
- AMOLED + variant toggle: toggle is disabled (single-variant theme).

## Testing

- Palette table: unit test that every theme has both variants (AMOLED
  aliased) and valid hex values.
- Settings migration: legacy `theme` values map correctly; round-trip
  persistence includes the legacy field.
- Fonts API: Go handler tests for upload validation (magic bytes, size,
  cap), authorization (cross-profile access denied), list/delete.
- Reader: font-face injection present when an uploaded font is selected;
  columns/columnGap/justify reach the content CSS; comics unaffected.

## Out of scope

- Reading stats, dictionary/translate, PDF engine (later sub-projects).
- Per-book font/theme overrides (global reader settings remain global).
- Font sharing between users or server-wide font libraries.
- App-shell theming (this is reader-content theming only).
