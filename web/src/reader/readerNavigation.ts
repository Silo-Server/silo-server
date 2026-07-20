// Navigation decision helpers for the prose reader. Pure functions so the
// footer band, tap zones, and shortcuts overlay stay unit-testable apart
// from foliate and the DOM.

export type ChapterExtent = { start: number; end: number; index: number };

// sectionFractions are foliate's cumulative boundaries (view.getSectionFractions()),
// e.g. [0, 0.25, 0.6, 1]. A trailing boundary below 1 means the list omits the
// book-end marker; the final section then runs to 1.
export function chapterExtent(sectionFractions: number[], fraction: number): ChapterExtent | null {
  if (sectionFractions.length < 2) return null;
  const clamped = Math.min(1, Math.max(0, fraction));
  for (let i = sectionFractions.length - 1; i >= 0; i--) {
    const start = sectionFractions[i];
    if (start === undefined || clamped < start) continue;
    const end = i + 1 < sectionFractions.length ? (sectionFractions[i + 1] ?? 1) : 1;
    if (start >= end) continue; // zero-width section: attribute to the previous one
    return { start, end, index: i };
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
