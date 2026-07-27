# Reader Appearance Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Ten paired-palette reader themes with an independent light/dark toggle, per-profile custom font uploads, 1–4 columns with gap control, and a justification toggle.

**Architecture:** A pure palette module (`readerThemes.ts`) feeds the existing `readerStyles`/`readerColors` pipeline in `FoliateBookReader.tsx`; settings gain `themeName`/`themeVariant`/`columns`/`columnGap`/`justify` with legacy migration in `normalizeReaderSettings`. Fonts are a small Go feature beside the existing reader handlers: a `reader_fonts` table + filesystem blobs under `SILO_READER_FONTS_DIR`, four routes in the `/ebooks` group, and client `@font-face` injection through `renderer.setStyles`.

**Tech Stack:** React 19 + TypeScript + vitest; Go + chi + pgx + Goose migrations.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-20-reader-appearance-design.md` — binding values live there; re-read before starting.
- Themes: Default, Sepia, Gray, Dawnlight, Ember, Aurora, Ocean, Meadow, Rosewood, AMOLED (dark-only; variant toggle disabled). Palette hexes in Task 1 are the approved values — copy verbatim.
- Legacy mapping: `light → Default/light`, `sepia → Sepia/light`, `dark → Default/dark`; unknown → Default/light. Keep writing legacy `theme` ("light"|"sepia"|"dark", from themeVariant + Sepia special-case: Sepia/light → "sepia", any dark → "dark", else "light").
- Fonts: `.ttf/.otf/.woff/.woff2`, magic-byte validation (sfnt `00 01 00 00` or `OTTO`, `wOFF`, `wOF2`), 5 MB/file, 10 fonts/profile, scoped user_id+profile_id, routes inside the `/ebooks` group.
- Columns `auto|1|2|3|4` (strings in the select, number|"auto" in settings), `columnGap` 0–50 (percent), `justify` boolean; all inert for comics (the settings UI for them is already gated by `!isComicFormat` alongside existing typography controls).
- API rules: additive only; new endpoints, no changes to existing response fields.
- Gates for every task: focused vitest/`go test` per step; before each commit the touched-language gate (`pnpm exec tsc --noEmit -p tsconfig.app.json` 0 errors, `pnpm run lint` 0 errors/159-warning baseline, prettier for web; `gofmt`/`go vet` clean for Go). Conventional Commits with trailer `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>`.
- Web commands run from `web/`; commits from repo root.

---

### Task 1: Palette module

**Files:**
- Create: `web/src/reader/readerThemes.ts`
- Test: `web/src/reader/readerThemes.test.ts`

**Interfaces:**
- Produces (Tasks 2–4 depend on these exact names):

```typescript
export type ReaderThemeVariant = "light" | "dark";
export type ReaderThemeName =
  | "default" | "sepia" | "gray" | "dawnlight" | "ember"
  | "aurora" | "ocean" | "meadow" | "rosewood" | "amoled";
export type ReaderPalette = { background: string; foreground: string; link: string };
export const READER_THEMES: Record<ReaderThemeName, { label: string; darkOnly?: true; light: ReaderPalette; dark: ReaderPalette }>;
export function readerPalette(name: ReaderThemeName, variant: ReaderThemeVariant): ReaderPalette & { scheme: ReaderThemeVariant };
export function legacyThemeFor(name: ReaderThemeName, variant: ReaderThemeVariant): "light" | "sepia" | "dark";
export function themeFromLegacy(theme: string): { themeName: ReaderThemeName; themeVariant: ReaderThemeVariant };
```

- [ ] **Step 1: Write the failing tests**

```typescript
// web/src/reader/readerThemes.test.ts
import { describe, expect, it } from "vitest";
import {
  READER_THEMES,
  legacyThemeFor,
  readerPalette,
  themeFromLegacy,
} from "./readerThemes";

const HEX = /^#[0-9a-f]{6}$/;

describe("READER_THEMES", () => {
  it("defines ten themes with valid hex palettes in both variants", () => {
    const names = Object.keys(READER_THEMES);
    expect(names).toHaveLength(10);
    for (const theme of Object.values(READER_THEMES)) {
      for (const variant of ["light", "dark"] as const) {
        const palette = theme[variant];
        expect(palette.background).toMatch(HEX);
        expect(palette.foreground).toMatch(HEX);
        expect(palette.link).toMatch(HEX);
      }
    }
  });

  it("aliases AMOLED's light variant to its dark palette", () => {
    expect(READER_THEMES.amoled.darkOnly).toBe(true);
    expect(readerPalette("amoled", "light")).toEqual(readerPalette("amoled", "dark"));
    expect(readerPalette("amoled", "light").scheme).toBe("dark");
  });

  it("keeps the existing default and sepia palettes stable", () => {
    expect(readerPalette("default", "light").background).toBe("#ffffff");
    expect(readerPalette("sepia", "light")).toMatchObject({
      background: "#f4ecd8",
      foreground: "#2f261b",
    });
  });
});

describe("legacy mapping", () => {
  it("maps stored legacy themes onto palette pairs", () => {
    expect(themeFromLegacy("light")).toEqual({ themeName: "default", themeVariant: "light" });
    expect(themeFromLegacy("sepia")).toEqual({ themeName: "sepia", themeVariant: "light" });
    expect(themeFromLegacy("dark")).toEqual({ themeName: "default", themeVariant: "dark" });
    expect(themeFromLegacy("nonsense")).toEqual({ themeName: "default", themeVariant: "light" });
  });

  it("derives the legacy field for backward-compatible persistence", () => {
    expect(legacyThemeFor("sepia", "light")).toBe("sepia");
    expect(legacyThemeFor("ocean", "dark")).toBe("dark");
    expect(legacyThemeFor("amoled", "light")).toBe("dark");
    expect(legacyThemeFor("meadow", "light")).toBe("light");
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && pnpm exec vitest run src/reader/readerThemes.test.ts`
Expected: FAIL — "Cannot find module './readerThemes'"

- [ ] **Step 3: Implement**

```typescript
// web/src/reader/readerThemes.ts

// Reader content palettes. Each theme is a light/dark pair; AMOLED is
// dark-only (its light slot aliases the dark palette so the variant toggle
// can be disabled without a special data shape).

export type ReaderThemeVariant = "light" | "dark";

export type ReaderThemeName =
  | "default"
  | "sepia"
  | "gray"
  | "dawnlight"
  | "ember"
  | "aurora"
  | "ocean"
  | "meadow"
  | "rosewood"
  | "amoled";

export type ReaderPalette = { background: string; foreground: string; link: string };

type ReaderThemeDefinition = {
  label: string;
  darkOnly?: true;
  light: ReaderPalette;
  dark: ReaderPalette;
};

const AMOLED_PALETTE: ReaderPalette = {
  background: "#000000",
  foreground: "#c9c9c9",
  link: "#7aa2f7",
};

export const READER_THEMES: Record<ReaderThemeName, ReaderThemeDefinition> = {
  default: {
    label: "Default",
    light: { background: "#ffffff", foreground: "#171717", link: "#2563eb" },
    dark: { background: "#111827", foreground: "#f8fafc", link: "#93c5fd" },
  },
  sepia: {
    label: "Sepia",
    light: { background: "#f4ecd8", foreground: "#2f261b", link: "#8b5a2b" },
    dark: { background: "#211b12", foreground: "#c8b697", link: "#c99a5b" },
  },
  gray: {
    label: "Gray",
    light: { background: "#eceef0", foreground: "#33383d", link: "#4a6fa5" },
    dark: { background: "#23262a", foreground: "#b9bfc6", link: "#8ab0dd" },
  },
  dawnlight: {
    label: "Dawnlight",
    light: { background: "#fdf6e3", foreground: "#586e75", link: "#268bd2" },
    dark: { background: "#002b36", foreground: "#93a1a1", link: "#268bd2" },
  },
  ember: {
    label: "Ember",
    light: { background: "#fbf1c7", foreground: "#3c3836", link: "#af3a03" },
    dark: { background: "#282828", foreground: "#ebdbb2", link: "#fe8019" },
  },
  aurora: {
    label: "Aurora",
    light: { background: "#eceff4", foreground: "#2e3440", link: "#5e81ac" },
    dark: { background: "#2e3440", foreground: "#d8dee9", link: "#88c0d0" },
  },
  ocean: {
    label: "Ocean",
    light: { background: "#e8f0f7", foreground: "#1e3a52", link: "#2471a3" },
    dark: { background: "#0d1b2a", foreground: "#a9c1d9", link: "#64a8dd" },
  },
  meadow: {
    label: "Meadow",
    light: { background: "#eef3e7", foreground: "#2f3b2a", link: "#3f7d44" },
    dark: { background: "#1a2318", foreground: "#b8c9a8", link: "#7fb069" },
  },
  rosewood: {
    label: "Rosewood",
    light: { background: "#f7edee", foreground: "#4a2f33", link: "#a4494f" },
    dark: { background: "#241a1c", foreground: "#d3b8bc", link: "#d98a90" },
  },
  amoled: {
    label: "AMOLED",
    darkOnly: true,
    light: AMOLED_PALETTE,
    dark: AMOLED_PALETTE,
  },
};

export function readerPalette(
  name: ReaderThemeName,
  variant: ReaderThemeVariant,
): ReaderPalette & { scheme: ReaderThemeVariant } {
  const theme = READER_THEMES[name] ?? READER_THEMES.default;
  const effective: ReaderThemeVariant = theme.darkOnly ? "dark" : variant;
  return { ...theme[effective], scheme: effective };
}

export function legacyThemeFor(
  name: ReaderThemeName,
  variant: ReaderThemeVariant,
): "light" | "sepia" | "dark" {
  if (readerPalette(name, variant).scheme === "dark") return "dark";
  if (name === "sepia") return "sepia";
  return "light";
}

export function themeFromLegacy(theme: string): {
  themeName: ReaderThemeName;
  themeVariant: ReaderThemeVariant;
} {
  switch (theme) {
    case "sepia":
      return { themeName: "sepia", themeVariant: "light" };
    case "dark":
      return { themeName: "default", themeVariant: "dark" };
    default:
      return { themeName: "default", themeVariant: "light" };
  }
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd web && pnpm exec vitest run src/reader/readerThemes.test.ts`
Expected: PASS (5 tests)

- [ ] **Step 5: Format, gate, commit**

```bash
cd web && pnpm exec prettier --write src/reader/readerThemes.ts src/reader/readerThemes.test.ts && pnpm exec tsc --noEmit -p tsconfig.app.json && cd ..
git add web/src/reader/readerThemes.ts web/src/reader/readerThemes.test.ts
git commit -m "feat(reader): paired-palette theme table

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Settings shape + normalization migration

**Files:**
- Modify: `web/src/reader/FoliateBookReader.tsx` (ReaderSettings type ~line 74-90, `DEFAULT_READER_SETTINGS` ~line 172, `normalizeReaderSettings`)
- Test: `web/src/reader/FoliateBookReader.test.ts` (extend — it already tests `normalizeReaderSettings`)

**Interfaces:**
- Consumes: `themeFromLegacy`, `legacyThemeFor`, types from Task 1.
- Produces (Tasks 3–4 rely on these exact fields): `ReaderSettings` gains
  `themeName: ReaderThemeName; themeVariant: ReaderThemeVariant; columns: "auto" | 1 | 2 | 3 | 4; columnGap: number; justify: boolean; customFontID: number | null;`
  and keeps `theme: ReaderTheme` (legacy, derived on write). `spread` field REMOVED from the type (normalization maps stored `spread: "none"` → `columns: 1`).

- [ ] **Step 1: Write failing tests** (add to the existing `normalizeReaderSettings` describe block in `FoliateBookReader.test.ts`; match its existing call style)

```typescript
it("migrates legacy theme and spread values", () => {
  const settings = normalizeReaderSettings({ theme: "sepia", spread: "none" });
  expect(settings.themeName).toBe("sepia");
  expect(settings.themeVariant).toBe("light");
  expect(settings.theme).toBe("sepia"); // legacy field still written
  expect(settings.columns).toBe(1);
});

it("prefers explicit themeName/themeVariant over the legacy field", () => {
  const settings = normalizeReaderSettings({ theme: "light", themeName: "ocean", themeVariant: "dark" });
  expect(settings.themeName).toBe("ocean");
  expect(settings.themeVariant).toBe("dark");
  expect(settings.theme).toBe("dark"); // legacy derived from the pair
});

it("clamps columns, columnGap, and defaults justify", () => {
  expect(normalizeReaderSettings({ columns: 4 }).columns).toBe(4);
  expect(normalizeReaderSettings({ columns: 7 }).columns).toBe("auto");
  expect(normalizeReaderSettings({ columnGap: 80 }).columnGap).toBe(50);
  expect(normalizeReaderSettings({ columnGap: -3 }).columnGap).toBe(0);
  expect(normalizeReaderSettings({}).justify).toBe(false);
  expect(normalizeReaderSettings({}).columns).toBe("auto");
  expect(normalizeReaderSettings({}).customFontID).toBeNull();
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && pnpm exec vitest run src/reader/FoliateBookReader.test.ts`
Expected: FAIL — new fields undefined

- [ ] **Step 3: Implement**

In `FoliateBookReader.tsx`:

```typescript
import {
  legacyThemeFor,
  themeFromLegacy,
  READER_THEMES,
  type ReaderThemeName,
  type ReaderThemeVariant,
} from "./readerThemes";

export type ReaderColumns = "auto" | 1 | 2 | 3 | 4;

// ReaderSettings: remove `spread: ReaderSpread` and add:
  themeName: ReaderThemeName;
  themeVariant: ReaderThemeVariant;
  columns: ReaderColumns;
  columnGap: number; // percent, 0-50
  justify: boolean;
  customFontID: number | null;
// keep `theme: ReaderTheme` in the type (legacy mirror, always rewritten on normalize)

// DEFAULT_READER_SETTINGS: remove `spread: "auto"`, add:
  themeName: "default",
  themeVariant: "light",
  columns: "auto",
  columnGap: 7,
  justify: false,
  customFontID: null,
```

In `normalizeReaderSettings` (which already clamps each field — follow its existing per-field style):

```typescript
const rawName = typeof raw.themeName === "string" && raw.themeName in READER_THEMES
  ? (raw.themeName as ReaderThemeName)
  : null;
const rawVariant = raw.themeVariant === "dark" || raw.themeVariant === "light"
  ? (raw.themeVariant as ReaderThemeVariant)
  : null;
const fromLegacy = themeFromLegacy(typeof raw.theme === "string" ? raw.theme : "light");
const themeName = rawName ?? fromLegacy.themeName;
const themeVariant = rawVariant ?? fromLegacy.themeVariant;
const theme = legacyThemeFor(themeName, themeVariant);

const columns: ReaderColumns =
  raw.columns === 1 || raw.columns === 2 || raw.columns === 3 || raw.columns === 4
    ? raw.columns
    : raw.spread === "none"
      ? 1
      : "auto";
const columnGap = Math.min(50, Math.max(0, Number.isFinite(Number(raw.columnGap)) ? Number(raw.columnGap) : 7));
const justify = raw.justify === true;
const customFontID =
  typeof raw.customFontID === "number" && Number.isInteger(raw.customFontID) && raw.customFontID > 0
    ? raw.customFontID
    : null;
```

(Adapt the exact `raw` access pattern to how the function currently reads unknown input; delete the `spread` normalization and the `ReaderSpread` export, fixing the one consumer found by tsc — the settings panel select replaced in Task 4.)

To keep this task compiling before Task 4 lands, update `readerRendererAttributes` minimally in this task: replace the `spread`-based `maxColumnCount` with

```typescript
maxColumnCount: scrolled ? "1" : settings.columns === "auto" ? "2" : String(settings.columns),
gap: `${settings.columnGap}%`,
```

and in `readerStyles` add (with the existing `!important` style):

```typescript
`text-align: ${settings.justify ? "justify" : "initial"} !important;`
```

in the `html, body` block, and switch `readerColors(theme)` calls to `readerPalette(settings.themeName, settings.themeVariant)` (delete `readerColors`; keep the returned `{background, foreground, link, scheme}` contract). Also update the temporary settings panel references: the Spread `<select>` in `web/src/pages/EbookReader.tsx` (~line 1414-1427) must be deleted in THIS task (its replacement lands in Task 4; removing it now keeps tsc green) and the Theme `<select>` (~line 1257) changes its `onChange` to `updateReaderSettings(themeFromLegacy(value))` — a stopgap Task 4 replaces. Check `READER_PROFILES` in `EbookReader.tsx` for `spread` references and drop them.

- [ ] **Step 4: Run suites**

Run: `cd web && pnpm exec vitest run src/reader/FoliateBookReader.test.ts src/pages/EbookReader.test.tsx src/reader/readerThemes.test.ts`
Expected: PASS (update any EbookReader test that asserted the Spread select — delete those assertions; keep intent by asserting the Columns control in Task 4)

- [ ] **Step 5: Gate + commit**

```bash
cd web && pnpm exec prettier --write src/reader/FoliateBookReader.tsx src/reader/FoliateBookReader.test.ts src/pages/EbookReader.tsx && pnpm exec tsc --noEmit -p tsconfig.app.json && pnpm run lint && cd ..
git add -A web/src
git commit -m "feat(reader): palette-pair settings with columns and justify

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: Fonts backend — migration, store, handlers, routes

**Files:**
- Create: `migrations/sql/20260720190000_reader_fonts.sql`
- Create: `internal/api/handlers/reader_fonts.go`
- Create: `internal/api/handlers/reader_fonts_test.go`
- Modify: `internal/api/router.go` (~line 2218 `/ebooks` group; handler wiring near `ebookConfigStore` ~line 514)
- Modify: `cmd/silo/main.go` (fonts dir resolution, beside `resolvePluginCacheDir` ~line 133)

**Interfaces:**
- Produces (Task 4's client calls these): routes inside the `/ebooks` group —
  `GET /reader-fonts` → `{"fonts": [{"id":1,"name":"Literata","filename":"literata.woff2","created_at":"…"}]}`;
  `POST /reader-fonts` (multipart field `font`) → 201 with one font object; errors: 400 `bad_request` (missing/invalid), 413 `too_large`, 409 `limit_reached`, 415 `unsupported_type`;
  `DELETE /reader-fonts/{font_id}` → 204; `GET /reader-fonts/{font_id}/file` → blob, `Cache-Control: private, max-age=31536000, immutable`.
- Go surface: `type ReaderFontsHandler struct { Store ReaderFontStore; Dir string }`, `NewPGReaderFontStore(pool)`.

- [ ] **Step 1: Migration**

```sql
-- migrations/sql/20260720190000_reader_fonts.sql
-- +goose Up
CREATE TABLE IF NOT EXISTS reader_fonts (
    id BIGINT GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    user_id INTEGER NOT NULL REFERENCES users(id) ON DELETE CASCADE,
    profile_id TEXT NOT NULL,
    name TEXT NOT NULL,
    filename TEXT NOT NULL,
    format TEXT NOT NULL,
    size_bytes BIGINT NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT NOW()
);
CREATE INDEX IF NOT EXISTS idx_reader_fonts_owner ON reader_fonts (user_id, profile_id);

-- +goose Down
DROP TABLE IF EXISTS reader_fonts;
```

Run: `make migrate-validate` — expected: passes.

- [ ] **Step 2: Write failing handler tests**

`internal/api/handlers/reader_fonts_test.go` — use a fake store (in-memory map implementing `ReaderFontStore`) and a `t.TempDir()` fonts dir; follow the package's existing handler-test style (httptest + chi context params). Cover, with real multipart bodies:

```go
func TestReaderFontUploadValidatesMagicBytes(t *testing.T)
// woff2 magic "wOF2" + padding accepted (201, metadata persisted, file on disk at <dir>/<user>/<profile>/<id>.woff2);
// a body starting "GIF8" rejected 415 unsupported_type; nothing on disk.

func TestReaderFontUploadEnforcesCaps(t *testing.T)
// store seeded with 10 fonts for the profile → 409 limit_reached;
// >5MB body → 413 too_large.

func TestReaderFontListScopedToProfile(t *testing.T)
// store holds fonts for (user 1, profile "a") and (user 1, profile "b");
// request as profile "a" lists only profile a's fonts.

func TestReaderFontServeAndDeleteAuthorize(t *testing.T)
// GET file for a font owned by another profile → 404;
// DELETE own font → 204, row gone, file gone;
// GET own font → 200, Content-Type font/woff2, immutable Cache-Control.
```

Write the four tests fully (assemble multipart with `mime/multipart.Writer`, magic bytes: sfnt `\x00\x01\x00\x00`, `OTTO`, `wOFF`, `wOF2`), asserting exact status codes and JSON error codes above.

- [ ] **Step 3: Run to verify failure**

Run: `go test ./internal/api/handlers -run TestReaderFont -count=1`
Expected: FAIL — undefined types/handlers

- [ ] **Step 4: Implement `reader_fonts.go`**

```go
// internal/api/handlers/reader_fonts.go
package handlers

// ReaderFont metadata row.
type ReaderFont struct {
    ID        int64     `json:"id"`
    UserID    int       `json:"-"`
    ProfileID string    `json:"-"`
    Name      string    `json:"name"`
    Filename  string    `json:"filename"`
    Format    string    `json:"format"`
    SizeBytes int64     `json:"-"`
    CreatedAt time.Time `json:"created_at"`
}

type ReaderFontStore interface {
    List(ctx context.Context, userID int, profileID string) ([]ReaderFont, error)
    Count(ctx context.Context, userID int, profileID string) (int, error)
    Insert(ctx context.Context, font ReaderFont) (ReaderFont, error)
    Get(ctx context.Context, userID int, profileID string, id int64) (*ReaderFont, error)
    Delete(ctx context.Context, userID int, profileID string, id int64) (bool, error)
}

const (
    readerFontMaxBytes   = 5 << 20
    readerFontMaxPerProfile = 10
)

type ReaderFontsHandler struct {
    Store ReaderFontStore
    Dir   string
}
```

Handlers: `HandleList`, `HandleUpload`, `HandleDelete`, `HandleServeFile`. Upload follows the branding pattern verbatim: `r.ParseMultipartForm(readerFontMaxBytes + (1 << 20))`, `r.FormFile("font")`, `io.ReadAll(io.LimitReader(file, readerFontMaxBytes+1))`, 413 on overflow. Format detection:

```go
func readerFontFormat(data []byte) (format, contentType string, ok bool) {
    switch {
    case len(data) >= 4 && string(data[:4]) == "wOF2":
        return "woff2", "font/woff2", true
    case len(data) >= 4 && string(data[:4]) == "wOFF":
        return "woff", "font/woff", true
    case len(data) >= 4 && (string(data[:4]) == "OTTO"):
        return "otf", "font/otf", true
    case len(data) >= 4 && bytes.Equal(data[:4], []byte{0x00, 0x01, 0x00, 0x00}):
        return "ttf", "font/ttf", true
    }
    return "", "", false
}
```

415 `unsupported_type` when !ok; 409 `limit_reached` when `Count >= readerFontMaxPerProfile`. Name: sanitized upload filename minus extension (font name-table parsing is NOT required — the spec allows filename fallback; keep it simple, note in the doc comment). Blob path: `filepath.Join(h.Dir, strconv.Itoa(userID), profileID, fmt.Sprintf("%d.%s", font.ID, format))` — insert row first (to get the ID), write file 0o644 with `os.MkdirAll` 0o755, delete the row on write failure. Serve: `Get` (404 when nil), open file (404 on missing), set `Content-Type` from format, `Cache-Control: private, max-age=31536000, immutable`, `X-Content-Type-Options: nosniff`, `http.ServeContent`. Delete: `Delete` row (404 when not found), then best-effort `os.Remove`.

PG store in the same file (mirror `PGEbookReaderConfigStore` style at `ebook_reader.go:715`): `PGReaderFontStore` with the five methods over the `reader_fonts` table, `ORDER BY created_at, id` for List.

- [ ] **Step 5: Wire routes and config**

`internal/api/router.go` inside the `/ebooks` group (after the annotations routes):

```go
if readerFontsHandler != nil {
    r.Get("/reader-fonts", readerFontsHandler.HandleList)
    r.Post("/reader-fonts", readerFontsHandler.HandleUpload)
    r.Delete("/reader-fonts/{font_id}", readerFontsHandler.HandleDelete)
    r.Get("/reader-fonts/{font_id}/file", readerFontsHandler.HandleServeFile)
}
```

Wire near `ebookConfigStore` (~line 514): `readerFontsHandler = &handlers.ReaderFontsHandler{Store: handlers.NewPGReaderFontStore(deps.DB), Dir: deps.ReaderFontsDir}` — add `ReaderFontsDir string` to the router deps struct, threaded from `cmd/silo/main.go`:

```go
func resolveReaderFontsDir() string {
    if v := strings.TrimSpace(os.Getenv("SILO_READER_FONTS_DIR")); v != "" {
        return v
    }
    return "/var/lib/silo/reader-fonts"
}
```

(match how `resolvePluginCacheDir` at main.go:133 is defined and where its value is consumed; pass through the same deps plumbing the other handlers use).

- [ ] **Step 6: Run tests + gates**

Run: `go test ./internal/api/handlers -run TestReaderFont -count=1 && go build ./... && go vet ./internal/api/... && gofmt -l internal/ cmd/ | (! grep .)`
Expected: PASS / clean

- [ ] **Step 7: Commit**

```bash
git add migrations/sql/20260720190000_reader_fonts.sql internal/api/handlers/reader_fonts.go internal/api/handlers/reader_fonts_test.go internal/api/router.go cmd/silo/main.go
git commit -m "feat(reader): per-profile custom font uploads

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Settings panel — theme grid, columns/gap/justify, font uploads

**Files:**
- Create: `web/src/reader/readerFontsApi.ts`
- Modify: `web/src/pages/EbookReader.tsx` (settings panel: Theme select region ~1257, font select ~1273; regroup into Theme/Typography/Layout)
- Modify: `web/src/reader/FoliateBookReader.tsx` (`readerStyles`: @font-face injection + custom font family)
- Test: `web/src/reader/readerFontsApi.test.ts`, extend `web/src/pages/EbookReader.test.tsx`

**Interfaces:**
- Consumes: Task 1 palettes, Task 2 settings fields, Task 3 endpoints.
- Produces:

```typescript
// readerFontsApi.ts
export type ReaderFontMeta = { id: number; name: string; filename: string; created_at: string };
export function fetchReaderFonts(): Promise<ReaderFontMeta[]>;
export function uploadReaderFont(file: File): Promise<ReaderFontMeta>;
export function deleteReaderFont(id: number): Promise<void>;
export function readerFontFileUrl(id: number): string; // "/api/v1/ebooks/reader-fonts/{id}/file"
```

- [ ] **Step 1: readerFontsApi + tests (TDD)**

Test first (`readerFontsApi.test.ts`): mock `fetch` (follow `ebookReaderApi.ts`'s transport — reuse its helper if one exists, else the same `apiFetch` the file uses); assert list unwraps `{fonts:[…]}`, upload posts `FormData` with field `font`, delete hits the id route, `readerFontFileUrl(3)` returns the exact path. Then implement mirroring `ebookReaderApi.ts`'s style. Run: `pnpm exec vitest run src/reader/readerFontsApi.test.ts` — RED then GREEN.

- [ ] **Step 2: Failing UI tests**

Add to `EbookReader.test.tsx` (mock `readerFontsApi` module like the other API mocks in the file):

```typescript
it("renders the theme grid and switches palette pairs", async () => {
  await renderReader();
  const ocean = document.querySelector('[data-theme-choice="ocean"]') as HTMLElement;
  act(() => ocean.click());
  // settings saved with themeName ocean + legacy theme mirror
  // (assert via the captured saveEbookReaderConfig payload like existing settings tests)
});

it("disables the variant toggle for AMOLED", async () => {
  await renderReader();
  act(() => (document.querySelector('[data-theme-choice="amoled"]') as HTMLElement).click());
  const toggle = document.querySelector('[aria-label="Reader dark mode"]') as HTMLButtonElement;
  expect(toggle.disabled).toBe(true);
});

it("offers uploaded fonts in the font picker and falls back on delete", async () => {
  mocks.fetchReaderFonts.mockResolvedValue([{ id: 3, name: "Literata", filename: "l.woff2", created_at: "" }]);
  await renderReader();
  // font select contains an "Uploaded" optgroup with Literata (value "custom:3")
  // deleting via mocked deleteReaderFont + selecting a missing font resets to inherit
});
```

(Adapt selectors/assertions to the harness's existing settings-panel test patterns; the assertions above are the required behaviors.)

- [ ] **Step 3: Implement panel + font-face**

- Theme section: a grid of 10 swatch buttons (`data-theme-choice={name}`, background from `READER_THEMES[name][variant].background`, title = label) + a "Reader dark mode" toggle button (`aria-label="Reader dark mode"`, disabled when `READER_THEMES[settings.themeName].darkOnly`), writing `updateReaderSettings({ themeName, themeVariant })` (normalization keeps the legacy mirror).
- Typography: existing font select gains an `<optgroup label="Uploaded">` from `fetchReaderFonts()` (loaded once when the panel opens; value format `custom:<id>` → `updateReaderSettings({ customFontID: id, fontFamily: "custom" })`; built-ins clear `customFontID`). Below it an upload input (accept `.ttf,.otf,.woff,.woff2`) calling `uploadReaderFont`, and per-font delete buttons calling `deleteReaderFont`; upload errors render inline (`too_large`/`limit_reached`/`unsupported_type` message text from the error response). Justify toggle beside hyphenation.
- Layout: Columns select (`auto|1|2|3|4`, aria-label "Columns") + Column gap slider (0–50, aria-label "Column gap") replacing the removed Spread select.
- `FoliateBookReader.tsx` `readerStyles`: when `settings.customFontID != null`, prepend

```typescript
`@font-face { font-family: "silo-custom-font"; src: url("${readerFontFileUrl(settings.customFontID)}"); font-display: swap; }`
```

and use `font-family: "silo-custom-font"` when `settings.fontFamily === "custom"`. (Import `readerFontFileUrl` from `./readerFontsApi`; the URL is same-origin so the existing CSP `font-src 'self'` allows it.)
- Delete-referenced-font fallback: when the fonts list loads and `customFontID` isn't in it, `updateReaderSettings({ customFontID: null, fontFamily: "inherit" })`.

- [ ] **Step 4: Run suites + gates**

Run: `cd web && pnpm exec vitest run src/pages/EbookReader.test.tsx src/reader && pnpm exec tsc --noEmit -p tsconfig.app.json && pnpm run lint && pnpm run format:check`
Expected: all green, 0 lint errors

- [ ] **Step 5: Commit**

```bash
git add -A web/src
git commit -m "feat(reader): theme grid, font uploads, columns and justify controls

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Full verification pass

**Files:** none expected (fix what gates surface)

- [ ] **Step 1:** `cd web && pnpm exec vitest run` — green except the 7 pre-existing upstream failures (SeasonContent/CompatibilityProxiesSettings/ServerStorageStep — QueryClientProvider/ResizeObserver, documented on the navigation branch).
- [ ] **Step 2:** `cd web && pnpm run lint && pnpm run format:check && pnpm build` — 0 errors, build succeeds.
- [ ] **Step 3:** `go test ./internal/api/handlers -count=1 && go build ./...` — green.
- [ ] **Step 4:** `make migrate-validate && make verify-local-paths` — pass.
- [ ] **Step 5:** Note for reviewer: manual pass — pick each theme in both variants, upload a real .woff2, select it, read a page; set 3 columns on a wide window; verify comics unaffected.
- [ ] **Step 6:** Commit any gate fixes as `test(reader): stabilize appearance suites` (skip if clean).
