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

  it("describes the b shortcut as a toggle, since it now un-bookmarks a matching location", () => {
    expect(READER_SHORTCUTS.find((s) => s.key === "b")?.description).toBe("Toggle bookmark");
  });
});
