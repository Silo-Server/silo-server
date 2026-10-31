// @vitest-environment node

import { describe, expect, it } from "vitest";

import {
  DEFAULT_CARD_PRESENTATION,
  carouselCardWidthPx,
  carouselCardWidthClasses,
  featuredCarouselCardWidthClasses,
  defaultWebPrimaryMenu,
  menuItemKey,
  moveMenuItem,
  parseCardPresentation,
  parsePrimaryMenu,
  parseShortcuts,
  rankedCarouselCardWidthClasses,
  rankedCarouselGutterClasses,
  rankedCarouselNumeralClasses,
  rankedCarouselOneCardWidthClasses,
  rankedCarouselOneGutterClasses,
  rankedCarouselOneNumeralClasses,
} from "./uiCustomization";
import { RANK_ONE_INK_RIGHT_UNITS } from "./rankNumerals";

/** The pixel values a set of `prefix-[NNpx]` utility classes carries, in order. */
function pxValues(classes: string, prefix: string): number[] {
  return [...classes.matchAll(new RegExp(`${prefix}-\\[(\\d+)px\\]`, "g"))].map((match) =>
    Number(match[1]),
  );
}

describe("UI customization contract helpers", () => {
  it("falls back when card presentation is incomplete or unknown", () => {
    expect(parseCardPresentation(null)).toEqual(DEFAULT_CARD_PRESENTATION);
    expect(parseCardPresentation({ caption: "title_metadata" })).toEqual(DEFAULT_CARD_PRESENTATION);
    expect(parseCardPresentation({ poster_size: "huge", caption: "title_metadata" })).toEqual(
      DEFAULT_CARD_PRESENTATION,
    );
  });

  it("keeps the poster size and ignores the retired caption", () => {
    expect(parseCardPresentation({ poster_size: "large", caption: "artwork" })).toEqual({
      poster_size: "large",
    });
    expect(parseCardPresentation({ poster_size: "compact" })).toEqual({ poster_size: "compact" });
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

  it("keeps a ranked row's three width tables in step", () => {
    // A ranked card's numeral gutter is written down three times: as the card
    // width classes, as the padding classes that hold the poster clear of the
    // numeral, and as the pixel table the carousel window measures its first
    // commit with. The three drift silently — a mismatch leaves a wide screen
    // short of slides while a laptop still looks right — so they are compared
    // here rather than trusted.
    const viewports = [500, 800, 1440];

    for (const size of ["compact", "standard", "large"] as const) {
      const cardWidths = pxValues(rankedCarouselCardWidthClasses(size), "w");
      const gutters = pxValues(rankedCarouselGutterClasses(size), "pl");
      const numerals = pxValues(rankedCarouselNumeralClasses(size), "left");

      expect(cardWidths).toHaveLength(3);
      expect(gutters).toHaveLength(3);
      expect(numerals).toHaveLength(3);

      for (const [index, viewportWidth] of viewports.entries()) {
        const cardWidth = cardWidths[index];
        const gutter = gutters[index];
        const numeral = numerals[index];
        if (cardWidth === undefined || gutter === undefined || numeral === undefined) {
          throw new Error(`ranked ${size} card has no width for a ${viewportWidth}px viewport`);
        }

        expect(carouselCardWidthPx(size, viewportWidth, "ranked")).toBe(cardWidth);
        // The gutter is the difference between the ranked card and its poster.
        expect(cardWidth).toBe(carouselCardWidthPx(size, viewportWidth) + gutter);
        // The numeral is pulled left by that same gutter, or it sits on the
        // poster instead of in the gutter beside it.
        expect(numeral).toBe(gutter);
      }
    }
  });

  it("puts the poster over the narrow 1 instead of leaving it a gap", () => {
    // The "1" is the only digit whose ink stops short of the poster's left edge.
    // On the shared gutter it sits in a gap where every other digit is cropped
    // by the poster, and the tables above cannot see that: they are consistent
    // either way. So hold rank 1's own gutter to the glyph. The poster's left
    // edge has to land inside the ink, and by roughly the share of the poster
    // the wide digits lose to the crop.
    const viewports = [500, 800, 1440];
    /** A poster is 2:3, and the numeral is drawn to the poster's own height. */
    const posterHeightPerWidth = 1.5;
    /** Rank 1's viewBox height, the divisor that scales the glyph. */
    const glyphHeight = 154;

    for (const size of ["compact", "standard", "large"] as const) {
      const gutters = pxValues(rankedCarouselOneGutterClasses(size), "pl");
      const cardWidths = pxValues(rankedCarouselOneCardWidthClasses(size), "w");
      const numerals = pxValues(rankedCarouselOneNumeralClasses(size), "left");

      expect(gutters).toHaveLength(3);
      expect(cardWidths).toHaveLength(3);
      expect(numerals).toHaveLength(3);

      for (const [index, viewportWidth] of viewports.entries()) {
        const gutter = gutters[index];
        const cardWidth = cardWidths[index];
        const numeral = numerals[index];
        if (gutter === undefined || cardWidth === undefined || numeral === undefined) {
          throw new Error(`rank 1 has no ${size} width for a ${viewportWidth}px viewport`);
        }
        const posterWidth = carouselCardWidthPx(size, viewportWidth);

        // Rank 1 is narrower than its neighbours, not wider.
        expect(cardWidth).toBe(posterWidth + gutter);
        expect(numeral).toBe(gutter);

        // The ink's right edge, in pixels: the glyph sits in a box as tall as
        // the poster, so it scales with the poster's height.
        const inkRight =
          (RANK_ONE_INK_RIGHT_UNITS * posterWidth * posterHeightPerWidth) / glyphHeight;
        // The wide digits lose about a sixth of the poster to it (measured:
        // 0.17 of the width at every size). A hairline meet is not an overlap,
        // and a gap is what this is here to catch.
        expect(inkRight - gutter).toBeGreaterThan(0.13 * posterWidth);
      }
    }
  });

  it("keeps the Featured row on an ordinary row's sizes until lg, then larger", () => {
    // A Featured card on a phone must be the card every other row draws: there is
    // no hover there to open it, and two tiles to a screen is not a row. From lg
    // up it is the page's showcase, and the only row that keeps growing.
    const viewports = [500, 800, 1200, 1440, 1700];

    for (const size of ["compact", "standard", "large"] as const) {
      const featured = pxValues(featuredCarouselCardWidthClasses(size), "w");
      expect(featured).toHaveLength(5);

      for (const [index, viewportWidth] of viewports.entries()) {
        const featuredWidth = featured[index];
        if (featuredWidth === undefined) {
          throw new Error(`Featured ${size} card has no width for a ${viewportWidth}px viewport`);
        }

        expect(featuredWidth).toBe(carouselCardWidthPx(size, viewportWidth, "featured"));

        // Below lg the two agree; from lg the Featured row is the larger one.
        const plainWidth = carouselCardWidthPx(size, viewportWidth);
        if (viewportWidth < 1024) {
          expect(featuredWidth).toBe(plainWidth);
        } else {
          expect(featuredWidth).toBeGreaterThan(plainWidth);
        }
      }
    }
  });

  it("keeps an ordinary row's class widths and its pixel table in step", () => {
    // The pixel table is what carousel windowing measures its first commit with;
    // the classes are what the cards actually take. Drift here mounts the wrong
    // number of slides and the row scrolls short.
    const viewports = [500, 800, 1440];

    for (const size of ["compact", "standard", "large"] as const) {
      const widths = pxValues(carouselCardWidthClasses(size), "w");
      expect(widths).toHaveLength(3);

      for (const [index, viewportWidth] of viewports.entries()) {
        expect(widths[index]).toBe(carouselCardWidthPx(size, viewportWidth));
      }
    }
  });
});
