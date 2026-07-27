# Reader Page Navigation Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Replace the header fraction slider with a chapter-aware footer bar, add tap-zone/auto-hide chrome and a discoverable keyboard map to the prose ebook reader.

**Architecture:** Pure decision helpers in a new `readerNavigation.ts` (unit-tested first), location plumbing lifted out of `FoliateBookReader` via a new `onLocationChange` callback + `getSectionFractions()` handle method, a presentational `ReaderFooter`, and integration in `EbookReader.tsx` (chrome visibility state, tap layer, extended key handler, shortcuts overlay). No server or schema changes.

**Tech Stack:** React 19 + TypeScript, foliate-js (vendored, already exposes `getSectionFractions()` at `web/vendor/foliate-js/view.js:539` and relocate `fraction`/`tocItem`), vitest + jsdom, Tailwind classes matching the existing reader chrome.

## Global Constraints

- Spec: `docs/superpowers/specs/2026-07-20-reader-page-navigation-design.md` — re-read before starting.
- Prose formats only: everything below is inert when `isComicFormat` is true (existing helper in `web/src/pages/EbookReader.tsx`).
- Single-letter shortcuts must not fire when a text input has focus — reuse the existing `isEditableTarget` guard (`EbookReader.tsx`, used at the ArrowLeft/ArrowRight handler ~line 464).
- Left/right tap zones are disabled in `flow: "scrolled"`; middle-tap chrome toggle works in both flows.
- A tap that ends a text-selection gesture must NOT page.
- No TOC → no chapter band, no chapter title; the slider still works on fractions.
- Time-remaining is OUT of scope (stats sub-project); the footer right slot shows percentage only.
- All commands run from `web/`: `pnpm exec vitest run <file>`; format with `pnpm exec prettier --write <files>`; commit from repo root.
- Commit messages: Conventional Commits, `Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>` trailer.

---

### Task 1: Pure navigation helpers

**Files:**
- Create: `web/src/reader/readerNavigation.ts`
- Test: `web/src/reader/readerNavigation.test.ts`

**Interfaces:**
- Consumes: nothing (pure module).
- Produces:
  - `chapterExtent(sectionFractions: number[], fraction: number): { start: number; end: number; index: number } | null`
  - `tapZoneAction(input: { xRatio: number; flow: "paginated" | "scrolled"; hasSelection: boolean }): "prev" | "next" | "toggle-chrome" | null`
  - `READER_SHORTCUTS: ReadonlyArray<{ key: string; description: string }>` (display data for the overlay)

- [ ] **Step 1: Write the failing tests**

```typescript
// web/src/reader/readerNavigation.test.ts
import { describe, expect, it } from "vitest";
import { chapterExtent, tapZoneAction, READER_SHORTCUTS } from "./readerNavigation";

describe("chapterExtent", () => {
  // foliate's getSectionFractions() returns cumulative fraction boundaries
  // starting at 0, e.g. [0, 0.25, 0.6, 1] for a 3-section book.
  it("maps a whole-book fraction to its section's extent", () => {
    expect(chapterExtent([0, 0.25, 0.6, 1], 0.3)).toEqual({ start: 0.25, end: 0.6, index: 1 });
  });

  it("clamps to the last section at fraction 1", () => {
    expect(chapterExtent([0, 0.25, 0.6, 1], 1)).toEqual({ start: 0.6, end: 1, index: 2 });
  });

  it("returns null without enough boundaries", () => {
    expect(chapterExtent([], 0.5)).toBeNull();
    expect(chapterExtent([0], 0.5)).toBeNull();
  });

  it("treats a trailing boundary below 1 as extending to the book end", () => {
    expect(chapterExtent([0, 0.5], 0.75)).toEqual({ start: 0.5, end: 1, index: 1 });
  });
});

describe("tapZoneAction", () => {
  it("pages back on the left third and forward on the right third", () => {
    expect(tapZoneAction({ xRatio: 0.1, flow: "paginated", hasSelection: false })).toBe("prev");
    expect(tapZoneAction({ xRatio: 0.9, flow: "paginated", hasSelection: false })).toBe("next");
  });

  it("toggles chrome on the middle third", () => {
    expect(tapZoneAction({ xRatio: 0.5, flow: "paginated", hasSelection: false })).toBe(
      "toggle-chrome",
    );
  });

  it("never pages while a selection is active", () => {
    expect(tapZoneAction({ xRatio: 0.1, flow: "paginated", hasSelection: true })).toBeNull();
    expect(tapZoneAction({ xRatio: 0.5, flow: "paginated", hasSelection: true })).toBeNull();
  });

  it("disables edge zones in scrolled flow but keeps the chrome toggle", () => {
    expect(tapZoneAction({ xRatio: 0.1, flow: "scrolled", hasSelection: false })).toBeNull();
    expect(tapZoneAction({ xRatio: 0.5, flow: "scrolled", hasSelection: false })).toBe(
      "toggle-chrome",
    );
  });
});

describe("READER_SHORTCUTS", () => {
  it("documents every shortcut the reader binds", () => {
    const keys = READER_SHORTCUTS.map((s) => s.key);
    for (const key of ["←/→", "Home/End", "t", "s", "b", "f", "?", "Esc"]) {
      expect(keys).toContain(key);
    }
  });
});
```

- [ ] **Step 2: Run tests to verify they fail**

Run: `cd web && pnpm exec vitest run src/reader/readerNavigation.test.ts`
Expected: FAIL — "Cannot find module './readerNavigation'"

- [ ] **Step 3: Write the implementation**

```typescript
// web/src/reader/readerNavigation.ts

// Navigation decision helpers for the prose reader. Pure functions so the
// footer band, tap zones, and shortcuts overlay stay unit-testable apart
// from foliate and the DOM.

export type ChapterExtent = { start: number; end: number; index: number };

// sectionFractions are foliate's cumulative boundaries (view.getSectionFractions()),
// e.g. [0, 0.25, 0.6, 1]. A trailing boundary below 1 means the list omits the
// book-end marker; the final section then runs to 1.
export function chapterExtent(
  sectionFractions: number[],
  fraction: number,
): ChapterExtent | null {
  if (sectionFractions.length < 2) return null;
  const clamped = Math.min(1, Math.max(0, fraction));
  for (let i = sectionFractions.length - 1; i >= 0; i--) {
    if (clamped >= sectionFractions[i]) {
      const start = sectionFractions[i];
      const end = i + 1 < sectionFractions.length ? sectionFractions[i + 1] : 1;
      if (start >= end) continue; // zero-width section: attribute to the previous one
      return { start, end, index: i };
    }
  }
  return { start: 0, end: sectionFractions[1] ?? 1, index: 0 };
}

export type TapZoneInput = {
  xRatio: number; // pointer X within the reading surface, 0..1
  flow: "paginated" | "scrolled";
  hasSelection: boolean;
};

export type TapZoneResult = "prev" | "next" | "toggle-chrome" | null;

export function tapZoneAction({ xRatio, flow, hasSelection }: TapZoneInput): TapZoneResult {
  if (hasSelection) return null;
  if (xRatio < 1 / 3) return flow === "paginated" ? "prev" : null;
  if (xRatio > 2 / 3) return flow === "paginated" ? "next" : null;
  return "toggle-chrome";
}

export const READER_SHORTCUTS = [
  { key: "←/→", description: "Previous / next page" },
  { key: "Home/End", description: "Start / end of chapter" },
  { key: "t", description: "Contents panel" },
  { key: "s", description: "Search panel" },
  { key: "b", description: "Toggle bookmark" },
  { key: "f", description: "Fullscreen" },
  { key: "?", description: "This shortcut list" },
  { key: "Esc", description: "Close overlay, or hide/show controls" },
] as const;
```

- [ ] **Step 4: Run tests to verify they pass**

Run: `cd web && pnpm exec vitest run src/reader/readerNavigation.test.ts`
Expected: PASS (9 tests)

- [ ] **Step 5: Format and commit**

```bash
cd web && pnpm exec prettier --write src/reader/readerNavigation.ts src/reader/readerNavigation.test.ts && cd ..
git add web/src/reader/readerNavigation.ts web/src/reader/readerNavigation.test.ts
git commit -m "feat(reader): add navigation decision helpers

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 2: Surface location data from FoliateBookReader

**Files:**
- Modify: `web/src/reader/FoliateBookReader.tsx` (RelocateDetail type ~line 131, handle assembly ~line 649, relocate listener ~line 839, props interface)
- Test: `web/src/reader/FoliateBookReader.component.test.tsx` (extend)

**Interfaces:**
- Consumes: foliate view events; `RelocateDetail` currently `{ cfi?, location? }`.
- Produces (used by Task 4):
  - New prop `onLocationChange?: (info: ReaderLocationInfo) => void` where
    `export type ReaderLocationInfo = { fraction: number; sectionIndex: number | null; tocLabel: string | null }`
  - New handle method `getSectionFractions: () => number[]` on `FoliateBookReaderHandle`.

- [ ] **Step 1: Write the failing test**

Add to `web/src/reader/FoliateBookReader.component.test.tsx` (follow the file's existing pattern for mounting the component and dispatching a `relocate` CustomEvent on the fake `<foliate-view>` element; reuse its existing fixtures/mocks):

```typescript
it("reports location info on relocate", async () => {
  const onLocationChange = vi.fn();
  // mount with the same fixture the surrounding tests use, passing onLocationChange
  // ... (mount helper from this file)
  fakeView.dispatchEvent(
    new CustomEvent("relocate", {
      detail: {
        cfi: "epubcfi(/6/8!/4/2)",
        fraction: 0.3,
        index: 1,
        tocItem: { label: "Chapter 2" },
        location: { current: 29, total: 100 },
      },
    }),
  );
  expect(onLocationChange).toHaveBeenCalledWith({
    fraction: 0.3,
    sectionIndex: 1,
    tocLabel: "Chapter 2",
  });
});

it("exposes section fractions through the handle", async () => {
  fakeView.getSectionFractions = () => [0, 0.25, 1];
  // handleRef obtained from the mount helper
  expect(handleRef.current?.getSectionFractions()).toEqual([0, 0.25, 1]);
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && pnpm exec vitest run src/reader/FoliateBookReader.component.test.tsx`
Expected: FAIL — `onLocationChange` never called / `getSectionFractions` is not a function

- [ ] **Step 3: Implement**

In `web/src/reader/FoliateBookReader.tsx`:

Widen the relocate type (~line 131):

```typescript
type RelocateDetail = {
  cfi?: string;
  fraction?: number;
  index?: number;
  tocItem?: { label?: string };
  location?: {
    current?: number;
    total?: number;
  };
};

export type ReaderLocationInfo = {
  fraction: number;
  sectionIndex: number | null;
  tocLabel: string | null;
};
```

Add to the props interface (next to `onProgressChange`):

```typescript
onLocationChange?: (info: ReaderLocationInfo) => void;
```

Add to `FoliateBookReaderHandle` (type ~line 56) and the `useImperativeHandle` block (~line 649):

```typescript
// type:
getSectionFractions: () => number[];

// handle assembly (viewRef.current is the foliate view; its getSectionFractions
// exists at web/vendor/foliate-js/view.js:539):
getSectionFractions: () =>
  (viewRef.current as { getSectionFractions?: () => number[] } | null)
    ?.getSectionFractions?.() ?? [],
```

In the relocate listener (~line 839), after the existing `onProgressChange` call:

```typescript
const detail = (event as CustomEvent<RelocateDetail>).detail;
if (typeof detail.fraction === "number") {
  onLocationChange?.({
    fraction: Math.min(1, Math.max(0, detail.fraction)),
    sectionIndex: typeof detail.index === "number" ? detail.index : null,
    tocLabel: detail.tocItem?.label?.trim() || null,
  });
}
```

(Keep the existing `progressFromRelocate` flow untouched; `onLocationChange` is additive.)

- [ ] **Step 4: Run the component suite**

Run: `cd web && pnpm exec vitest run src/reader/FoliateBookReader.component.test.tsx src/reader/FoliateBookReader.test.ts`
Expected: PASS, including the two new tests

- [ ] **Step 5: Format and commit**

```bash
cd web && pnpm exec prettier --write src/reader/FoliateBookReader.tsx src/reader/FoliateBookReader.component.test.tsx && cd ..
git add web/src/reader/FoliateBookReader.tsx web/src/reader/FoliateBookReader.component.test.tsx
git commit -m "feat(reader): surface relocate location info and section fractions

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 3: ReaderFooter component

**Files:**
- Create: `web/src/reader/ReaderFooter.tsx`
- Test: `web/src/reader/ReaderFooter.test.tsx`

**Interfaces:**
- Consumes: `ChapterExtent` from Task 1.
- Produces (used by Task 4):

```typescript
export type ReaderFooterProps = {
  fraction: number; // 0..1 current position
  extent: ChapterExtent | null; // null hides band + title
  chapterLabel: string | null;
  onScrub: (fraction: number) => void;
  onShowShortcuts: () => void;
};
export default function ReaderFooter(props: ReaderFooterProps): JSX.Element;
```

- [ ] **Step 1: Write the failing test**

```typescript
// web/src/reader/ReaderFooter.test.tsx
// @vitest-environment jsdom
import { act } from "react";
import { createRoot, type Root } from "react-dom/client";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import ReaderFooter from "./ReaderFooter";

let container: HTMLDivElement;
let root: Root;

beforeEach(() => {
  container = document.createElement("div");
  document.body.appendChild(container);
  root = createRoot(container);
});

afterEach(() => {
  act(() => root.unmount());
  container.remove();
});

function render(props: Partial<React.ComponentProps<typeof ReaderFooter>> = {}) {
  const defaults = {
    fraction: 0.34,
    extent: { start: 0.28, end: 0.42, index: 6 },
    chapterLabel: "The Duke",
    onScrub: vi.fn(),
    onShowShortcuts: vi.fn(),
  };
  const merged = { ...defaults, ...props };
  act(() => root.render(<ReaderFooter {...merged} />));
  return merged;
}

describe("ReaderFooter", () => {
  it("shows chapter label, percentage, and the chapter band", () => {
    render();
    expect(container.textContent).toContain("The Duke");
    expect(container.textContent).toContain("34%");
    const band = container.querySelector("[data-chapter-band]") as HTMLElement;
    expect(band).not.toBeNull();
    expect(band.style.left).toBe("28%");
    expect(band.style.width).toBe("14.000000000000002%");
  });

  it("hides band and label without an extent", () => {
    render({ extent: null, chapterLabel: null });
    expect(container.querySelector("[data-chapter-band]")).toBeNull();
  });

  it("scrubs the whole book from the slider", () => {
    const { onScrub } = render();
    const slider = container.querySelector('input[type="range"]') as HTMLInputElement;
    slider.value = "62";
    act(() => {
      slider.dispatchEvent(new Event("input", { bubbles: true }));
    });
    expect(onScrub).toHaveBeenCalledWith(0.62);
  });

  it("opens the shortcuts overlay from the ? affordance", () => {
    const { onShowShortcuts } = render();
    const btn = container.querySelector('[aria-label="Keyboard shortcuts"]') as HTMLButtonElement;
    act(() => btn.click());
    expect(onShowShortcuts).toHaveBeenCalled();
  });
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && pnpm exec vitest run src/reader/ReaderFooter.test.tsx`
Expected: FAIL — "Cannot find module './ReaderFooter'"

- [ ] **Step 3: Implement**

```tsx
// web/src/reader/ReaderFooter.tsx
import { Keyboard } from "lucide-react";
import { Button } from "@/components/ui/button";
import type { ChapterExtent } from "./readerNavigation";

export type ReaderFooterProps = {
  fraction: number;
  extent: ChapterExtent | null;
  chapterLabel: string | null;
  onScrub: (fraction: number) => void;
  onShowShortcuts: () => void;
};

export default function ReaderFooter({
  fraction,
  extent,
  chapterLabel,
  onScrub,
  onShowShortcuts,
}: ReaderFooterProps) {
  const percent = Math.round(Math.min(1, Math.max(0, fraction)) * 100);
  return (
    <footer className="border-border/70 bg-background/95 sticky bottom-0 z-20 border-t backdrop-blur">
      <div className="flex flex-col gap-1 px-4 py-2">
        <div className="relative h-2">
          {extent && (
            <div
              data-chapter-band
              title={chapterLabel ?? undefined}
              className="bg-primary/25 pointer-events-none absolute top-0 h-2 rounded"
              style={{ left: `${extent.start * 100}%`, width: `${(extent.end - extent.start) * 100}%` }}
            />
          )}
          <input
            aria-label="Reading progress"
            type="range"
            min="0"
            max="100"
            step="1"
            value={percent}
            onInput={(event) => onScrub(Number((event.target as HTMLInputElement).value) / 100)}
            className="accent-primary absolute inset-0 h-2 w-full min-w-0"
          />
        </div>
        <div className="text-muted-foreground flex items-center justify-between text-xs">
          <span className="min-w-0 truncate">{chapterLabel ?? ""}</span>
          <span className="tabular-nums">{percent}%</span>
          <Button
            variant="ghost"
            size="icon-sm"
            aria-label="Keyboard shortcuts"
            title="Keyboard shortcuts"
            onClick={onShowShortcuts}
          >
            <Keyboard className="size-4" />
          </Button>
        </div>
      </div>
    </footer>
  );
}
```

- [ ] **Step 4: Run to verify pass**

Run: `cd web && pnpm exec vitest run src/reader/ReaderFooter.test.tsx`
Expected: PASS (4 tests). If the band-width assertion fails on float formatting, assert with `expect(parseFloat(band.style.width)).toBeCloseTo(14)` instead — adjust the test, not the component.

- [ ] **Step 5: Format and commit**

```bash
cd web && pnpm exec prettier --write src/reader/ReaderFooter.tsx src/reader/ReaderFooter.test.tsx && cd ..
git add web/src/reader/ReaderFooter.tsx web/src/reader/ReaderFooter.test.tsx
git commit -m "feat(reader): chapter-aware footer bar

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 4: Wire footer into EbookReader (slider moves out of header)

**Files:**
- Modify: `web/src/pages/EbookReader.tsx` (header slider row ~lines 718–731 removed; footer rendered after `<main>`; new location state; FoliateBookReader gets `onLocationChange`)
- Test: `web/src/pages/EbookReader.test.tsx` (extend the FoliateBookReader mock + new tests)

**Interfaces:**
- Consumes: `ReaderFooter` (Task 3), `chapterExtent` (Task 1), `onLocationChange` + `getSectionFractions` (Task 2), existing `handleProgressScrub` (~line 359) and `readerProgress` state.
- Produces: `shortcutsOpen` boolean state + `setShortcutsOpen` (Task 6 renders the overlay from it).

- [ ] **Step 1: Extend the mock and write failing tests**

In `web/src/pages/EbookReader.test.tsx`, extend the `vi.mock("@/reader/FoliateBookReader", …)` stub: accept and store `onLocationChange` in a module-level `mocks.lastOnLocationChange`, add `getSectionFractions: () => [0, 0.25, 0.6, 1]` to the imperative handle, and invoke `onLocationChange` from a test via `act`. Then add:

```typescript
it("renders the chapter-aware footer instead of a header slider", async () => {
  await renderReader(); // existing helper in this file
  act(() => {
    mocks.lastOnLocationChange?.({ fraction: 0.3, sectionIndex: 1, tocLabel: "Chapter 2" });
  });
  const header = document.querySelector("header")!;
  expect(header.querySelector('input[type="range"]')).toBeNull();
  const footer = document.querySelector("footer")!;
  expect(footer.textContent).toContain("Chapter 2");
  expect(footer.textContent).toContain("30%");
  expect(footer.querySelector("[data-chapter-band]")).not.toBeNull();
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && pnpm exec vitest run src/pages/EbookReader.test.tsx -t "chapter-aware footer"`
Expected: FAIL — header still contains the range input / no footer

- [ ] **Step 3: Implement in EbookReader.tsx**

1. New state near the other reader state hooks:

```typescript
const [locationInfo, setLocationInfo] = useState<ReaderLocationInfo | null>(null);
const [shortcutsOpen, setShortcutsOpen] = useState(false);
```

2. Pass to `<FoliateBookReader …>`:

```tsx
onLocationChange={setLocationInfo}
```

3. Compute the extent (memoized, near other derived values):

```typescript
const chapterBand = useMemo(() => {
  if (!locationInfo || isComic) return null;
  const fractions = readerRef.current?.getSectionFractions() ?? [];
  return chapterExtent(fractions, locationInfo.fraction);
}, [locationInfo, isComic]);
```

4. Delete the header's slider row (the `div` with the `BookOpen` icon + range input + `progressLabel`, ~lines 718–731).

5. Render after `</main>` (prose only):

```tsx
{!isComic && (
  <ReaderFooter
    fraction={locationInfo?.fraction ?? readerProgress ?? 0}
    extent={chapterBand}
    chapterLabel={locationInfo?.tocLabel ?? null}
    onScrub={(fraction) => void readerRef.current?.goToFraction(fraction)}
    onShowShortcuts={() => setShortcutsOpen(true)}
  />
)}
```

Imports: `ReaderFooter` from `@/reader/ReaderFooter`, `chapterExtent` from `@/reader/readerNavigation`, `type ReaderLocationInfo` from `@/reader/FoliateBookReader`. Use the file's actual comic flag name (`isComicFormat`-derived variable) — match what the surrounding code calls it.

- [ ] **Step 4: Run the page suite**

Run: `cd web && pnpm exec vitest run src/pages/EbookReader.test.tsx`
Expected: PASS — new test green; any existing test that asserted the header slider must be updated to target the footer instead (update assertions, keep their intent).

- [ ] **Step 5: Format and commit**

```bash
cd web && pnpm exec prettier --write src/pages/EbookReader.tsx src/pages/EbookReader.test.tsx && cd ..
git add web/src/pages/EbookReader.tsx web/src/pages/EbookReader.test.tsx
git commit -m "feat(reader): move progress into chapter-aware footer

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 5: Tap zones and chrome auto-hide

**Files:**
- Modify: `web/src/pages/EbookReader.tsx` (chrome visibility state; pointer handler on the reading surface wrapper; header/footer conditional rendering)
- Test: `web/src/pages/EbookReader.test.tsx`

**Interfaces:**
- Consumes: `tapZoneAction` (Task 1); existing `readerRef.current?.prev()/next()`; reader settings `flow`; existing selection state (the page already tracks the current selection for the highlight button — reuse that state variable as `hasSelection`).
- Produces: `chromeVisible` boolean state (Task 6's Esc handling flips it).

- [ ] **Step 1: Write failing tests**

```typescript
it("pages on edge taps and toggles chrome on middle tap", async () => {
  await renderReader();
  const surface = document.querySelector("[data-reader-surface]") as HTMLElement;
  surface.getBoundingClientRect = () =>
    ({ left: 0, width: 300, top: 0, height: 500 }) as DOMRect;

  act(() => {
    surface.dispatchEvent(new PointerEvent("pointerup", { clientX: 30, bubbles: true }));
  });
  expect(mocks.readerPrev).toHaveBeenCalled();

  act(() => {
    surface.dispatchEvent(new PointerEvent("pointerup", { clientX: 270, bubbles: true }));
  });
  expect(mocks.readerNext).toHaveBeenCalled();

  expect(document.querySelector("header")).not.toBeNull();
  act(() => {
    surface.dispatchEvent(new PointerEvent("pointerup", { clientX: 150, bubbles: true }));
  });
  expect(document.querySelector("header")).toBeNull();
  expect(document.querySelector("footer")).toBeNull();
  act(() => {
    surface.dispatchEvent(new PointerEvent("pointerup", { clientX: 150, bubbles: true }));
  });
  expect(document.querySelector("header")).not.toBeNull();
});
```

(jsdom lacks `PointerEvent` — if the suite errors on it, construct with `new MouseEvent("pointerup", …)` in the test; the handler reads only `clientX`.)

- [ ] **Step 2: Run to verify failure**

Run: `cd web && pnpm exec vitest run src/pages/EbookReader.test.tsx -t "pages on edge taps"`
Expected: FAIL — no `[data-reader-surface]` element / prev not called

- [ ] **Step 3: Implement**

1. State: `const [chromeVisible, setChromeVisible] = useState(true);`
2. Wrap the reading surface (the container around `<FoliateBookReader>` inside `<main>`) with `data-reader-surface` and a `onPointerUp` handler:

```tsx
const handleSurfacePointerUp = useCallback(
  (event: React.PointerEvent<HTMLDivElement>) => {
    if (isComic) return;
    const rect = event.currentTarget.getBoundingClientRect();
    if (rect.width <= 0) return;
    const action = tapZoneAction({
      xRatio: (event.clientX - rect.left) / rect.width,
      flow: readerSettings.flow,
      hasSelection: Boolean(activeSelection),
    });
    if (action === "prev") readerRef.current?.prev();
    else if (action === "next") readerRef.current?.next();
    else if (action === "toggle-chrome") setChromeVisible((v) => !v);
  },
  [isComic, readerSettings.flow, activeSelection],
);
```

(`activeSelection` = the page's existing selection state variable; use its real name.)

3. Gate chrome rendering: wrap the existing `<header>` and the Task-4 `<ReaderFooter>` in `{chromeVisible && (…)}`. Comics always render chrome (`chromeVisible || isComic`).
4. Reappear on mouse-move-to-edge (pointer devices):

```typescript
useEffect(() => {
  if (chromeVisible) return;
  const onMove = (event: MouseEvent) => {
    if (event.clientY < 24 || window.innerHeight - event.clientY < 24) {
      setChromeVisible(true);
    }
  };
  window.addEventListener("mousemove", onMove);
  return () => window.removeEventListener("mousemove", onMove);
}, [chromeVisible]);
```

- [ ] **Step 4: Run the page suite**

Run: `cd web && pnpm exec vitest run src/pages/EbookReader.test.tsx`
Expected: PASS

- [ ] **Step 5: Format and commit**

```bash
cd web && pnpm exec prettier --write src/pages/EbookReader.tsx src/pages/EbookReader.test.tsx && cd ..
git add web/src/pages/EbookReader.tsx web/src/pages/EbookReader.test.tsx
git commit -m "feat(reader): tap zones with auto-hiding chrome

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 6: Keyboard map and shortcuts overlay

**Files:**
- Create: `web/src/reader/ReaderShortcutsOverlay.tsx`
- Modify: `web/src/pages/EbookReader.tsx` (extend the keydown effect ~line 462; render overlay)
- Test: `web/src/pages/EbookReader.test.tsx`

**Interfaces:**
- Consumes: `READER_SHORTCUTS` (Task 1), `shortcutsOpen`/`setShortcutsOpen` (Task 4), `chromeVisible`/`setChromeVisible` (Task 5), existing `isEditableTarget`, panel/bookmark/fullscreen handlers already in the file (reuse the exact handlers the existing header buttons call).
- Produces: nothing further.

- [ ] **Step 1: Write failing tests**

```typescript
it("binds the reader keyboard map with an input guard", async () => {
  await renderReader();
  const press = (key: string, target?: EventTarget) =>
    act(() => {
      window.dispatchEvent(new KeyboardEvent("keydown", { key, bubbles: true }));
    });

  press("?");
  expect(document.body.textContent).toContain("Keyboard shortcuts");
  press("Escape"); // closes overlay first
  expect(document.body.textContent).not.toContain("Previous / next page");

  press("Escape"); // then toggles chrome
  expect(document.querySelector("header")).toBeNull();

  // single letters do nothing from inputs
  const input = document.createElement("input");
  document.body.appendChild(input);
  input.focus();
  press("t");
  // panel did not open — assert via the panel's test id/text used elsewhere in this file
});

it("Home and End jump to chapter bounds", async () => {
  await renderReader();
  act(() => {
    mocks.lastOnLocationChange?.({ fraction: 0.3, sectionIndex: 1, tocLabel: "Ch 2" });
  });
  act(() => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "Home" }));
  });
  expect(mocks.readerGoToFraction).toHaveBeenCalledWith(0.25);
  act(() => {
    window.dispatchEvent(new KeyboardEvent("keydown", { key: "End" }));
  });
  expect(mocks.readerGoToFraction).toHaveBeenCalledWith(0.6);
});
```

- [ ] **Step 2: Run to verify failure**

Run: `cd web && pnpm exec vitest run src/pages/EbookReader.test.tsx -t "keyboard map"`
Expected: FAIL — overlay text absent, goToFraction not called

- [ ] **Step 3: Implement**

`web/src/reader/ReaderShortcutsOverlay.tsx`:

```tsx
import { READER_SHORTCUTS } from "./readerNavigation";
import { Button } from "@/components/ui/button";

export default function ReaderShortcutsOverlay({ onClose }: { onClose: () => void }) {
  return (
    <div
      role="dialog"
      aria-modal="true"
      aria-label="Keyboard shortcuts"
      className="fixed inset-0 z-40 flex items-center justify-center bg-black/50"
      onClick={onClose}
    >
      <div
        className="bg-background border-border w-80 rounded-lg border p-4 shadow-lg"
        onClick={(event) => event.stopPropagation()}
      >
        <h2 className="mb-3 text-sm font-semibold">Keyboard shortcuts</h2>
        <dl className="space-y-1.5 text-sm">
          {READER_SHORTCUTS.map((shortcut) => (
            <div key={shortcut.key} className="flex justify-between gap-4">
              <dt className="text-muted-foreground">{shortcut.description}</dt>
              <dd className="font-mono">{shortcut.key}</dd>
            </div>
          ))}
        </dl>
        <Button className="mt-4 w-full" variant="outline" size="sm" onClick={onClose} autoFocus>
          Close
        </Button>
      </div>
    </div>
  );
}
```

In `EbookReader.tsx`, replace the arrow-key effect (~line 462) with the full map. Reuse the exact existing handlers the header buttons call for TOC/search panel toggles, bookmark, and fullscreen (find them where the header buttons are defined; do not invent new ones):

```typescript
useEffect(() => {
  const handleKeyDown = (event: KeyboardEvent) => {
    if (event.defaultPrevented || isEditableTarget(event.target)) return;
    switch (event.key) {
      case "ArrowLeft":
        readerRef.current?.prev();
        break;
      case "ArrowRight":
        readerRef.current?.next();
        break;
      case "Home":
      case "End": {
        if (isComic) return;
        const fractions = readerRef.current?.getSectionFractions() ?? [];
        const extent = chapterExtent(fractions, locationInfoRef.current?.fraction ?? 0);
        if (!extent) return;
        event.preventDefault();
        void readerRef.current?.goToFraction(event.key === "Home" ? extent.start : extent.end);
        break;
      }
      case "t":
        toggleContentsPanel(); // existing handler name — use the real one
        break;
      case "s":
        toggleSearchPanel(); // existing handler name — use the real one
        break;
      case "b":
        toggleBookmark(); // existing handler name — use the real one
        break;
      case "f":
        toggleFullscreen(); // existing handler name — use the real one
        break;
      case "?":
        setShortcutsOpen(true);
        break;
      case "Escape":
        if (shortcutsOpen) setShortcutsOpen(false);
        else setChromeVisible((v) => !v);
        break;
    }
  };
  window.addEventListener("keydown", handleKeyDown);
  return () => window.removeEventListener("keydown", handleKeyDown);
}, [isComic, shortcutsOpen /*, the real handler deps */]);
```

Keep `locationInfo` mirrored in a ref (`locationInfoRef`) updated alongside `setLocationInfo` so the handler avoids re-binding per relocate. Render the overlay next to the footer: `{shortcutsOpen && <ReaderShortcutsOverlay onClose={() => setShortcutsOpen(false)} />}`.

- [ ] **Step 4: Run the page suite**

Run: `cd web && pnpm exec vitest run src/pages/EbookReader.test.tsx`
Expected: PASS

- [ ] **Step 5: Format and commit**

```bash
cd web && pnpm exec prettier --write src/pages/EbookReader.tsx src/reader/ReaderShortcutsOverlay.tsx src/pages/EbookReader.test.tsx && cd ..
git add web/src/pages/EbookReader.tsx web/src/reader/ReaderShortcutsOverlay.tsx web/src/pages/EbookReader.test.tsx
git commit -m "feat(reader): full keyboard map with shortcuts overlay

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

---

### Task 7: Full verification pass

**Files:**
- Modify: none expected (fix what the gates surface)

- [ ] **Step 1: Full web test suite**

Run: `cd web && pnpm exec vitest run`
Expected: all green (fix regressions before proceeding)

- [ ] **Step 2: Lint + format + build**

Run: `cd web && pnpm run lint && pnpm run format:check && pnpm build`
Expected: 0 errors (pre-existing warnings acceptable), build succeeds

- [ ] **Step 3: Local-path check (spec/plan hygiene)**

Run: `make verify-local-paths` (repo root)
Expected: passes

- [ ] **Step 4: Manual smoke (deferred to review)**

Note for the reviewer: run `make dev-frontend` against a backend with an ebook library and verify on a phone-width viewport: tap zones page correctly, middle-tap hides all chrome, selection near an edge does not page, footer band tracks chapters, `?` overlay opens/closes.

- [ ] **Step 5: Commit any gate fixes**

```bash
git add -A && git commit -m "test(reader): stabilize navigation suites

Co-Authored-By: Claude Fable 5 <noreply@anthropic.com>"
```

(Skip if nothing changed.)
