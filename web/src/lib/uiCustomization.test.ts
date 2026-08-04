import { describe, expect, it } from "vitest";

import {
  CARD_PRESENTATION_PRESETS,
  DEFAULT_CARD_PRESENTATION,
  defaultWebPrimaryMenu,
  menuItemKey,
  moveMenuItem,
  parseCardPresentation,
  parsePrimaryMenu,
  parseShortcuts,
} from "./uiCustomization";

describe("UI customization contract helpers", () => {
  it("falls back when card presentation is incomplete or unknown", () => {
    expect(parseCardPresentation(null)).toEqual(DEFAULT_CARD_PRESENTATION);
    expect(parseCardPresentation({ poster_size: "large" })).toEqual(DEFAULT_CARD_PRESENTATION);
    expect(parseCardPresentation({ poster_size: "huge", caption: "artwork" })).toEqual(
      DEFAULT_CARD_PRESENTATION,
    );
  });

  it("accepts every supported card dimension", () => {
    expect(parseCardPresentation({ poster_size: "large", caption: "artwork" })).toEqual({
      poster_size: "large",
      caption: "artwork",
    });
    expect(parseCardPresentation({ poster_size: "compact", caption: "title" })).toEqual({
      poster_size: "compact",
      caption: "title",
    });
  });

  it("keeps the named presets aligned with the cross-client contract", () => {
    expect(CARD_PRESENTATION_PRESETS.map(({ id, value }) => ({ id, value }))).toEqual([
      { id: "balanced", value: { poster_size: "standard", caption: "title_metadata" } },
      { id: "compact", value: { poster_size: "compact", caption: "title" } },
      { id: "cinema", value: { poster_size: "large", caption: "title" } },
      { id: "artwork", value: { poster_size: "large", caption: "artwork" } },
    ]);
  });

  it("rejects a custom menu without exactly one home destination", () => {
    expect(parsePrimaryMenu({ items: [{ type: "builtin", destination: "calendar" }] })).toBeNull();
    expect(
      parsePrimaryMenu({
        items: [
          { type: "builtin", destination: "home" },
          { type: "builtin", destination: "home" },
        ],
      }),
    ).toEqual({ items: [{ type: "builtin", destination: "home" }] });
  });

  it("sanitizes unknown and duplicate menu entries", () => {
    expect(
      parsePrimaryMenu({
        items: [
          { type: "builtin", destination: "home" },
          { type: "library", library_id: 7, label: "Movies" },
          { type: "library", library_id: 7, label: "Renamed" },
          { type: "builtin", destination: "unsupported" },
        ],
      }),
    ).toEqual({
      items: [
        { type: "builtin", destination: "home" },
        { type: "library", library_id: 7, label: "Movies" },
      ],
    });
  });

  it("parses only shortcut targets from the profile shortcut catalog", () => {
    expect(
      parseShortcuts({
        items: [
          { type: "builtin", destination: "home" },
          { type: "section", library_id: 7, section_id: "recent", label: "Recent" },
          { type: "collection", collection_id: "favorites", label: "Favorites" },
        ],
      }),
    ).toEqual({
      items: [
        { type: "section", library_id: 7, section_id: "recent", label: "Recent" },
        { type: "collection", collection_id: "favorites", label: "Favorites" },
      ],
    });
  });

  it("keeps global and library-scoped collection identities unambiguous", () => {
    const globalCollection = {
      type: "collection" as const,
      collection_id: "7:favorites",
      label: "Global favorites",
    };
    const libraryCollection = {
      type: "collection" as const,
      library_id: 7,
      collection_id: "favorites",
      label: "Library favorites",
    };

    expect(menuItemKey(globalCollection)).not.toBe(menuItemKey(libraryCollection));
    expect(parseShortcuts({ items: [globalCollection, libraryCollection] })).toEqual({
      items: [globalCollection, libraryCollection],
    });
  });

  it("rejects whitespace-only section and collection target IDs", () => {
    expect(
      parseShortcuts({
        items: [
          { type: "section", library_id: 7, section_id: "   ", label: "Recent" },
          { type: "collection", collection_id: "\t", label: "Favorites" },
        ],
      }),
    ).toEqual({ items: [] });
  });

  it("builds the native web primary-menu baseline", () => {
    const menu = defaultWebPrimaryMenu();
    expect(menu.items.map(menuItemKey)).toEqual([
      "builtin:home",
      "builtin:for_you",
      "builtin:calendar",
    ]);
  });

  it("moves items without mutating the source and ignores an out-of-range move", () => {
    const original = defaultWebPrimaryMenu().items;
    const moved = moveMenuItem(original, 1, -1);
    expect(moved.map(menuItemKey)).toEqual(["builtin:for_you", "builtin:home", "builtin:calendar"]);
    expect(original.map(menuItemKey)).toEqual([
      "builtin:home",
      "builtin:for_you",
      "builtin:calendar",
    ]);
    expect(moveMenuItem(original, 0, -1)).toEqual(original);
  });
});
