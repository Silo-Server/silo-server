// @vitest-environment node

import { describe, expect, it } from "vitest";

import {
  EXPANDING_SECTION_TYPES,
  FEATURED_CARD_SCALE,
  RANKED_SECTION_TYPES,
  expandedCardWidthClasses,
} from "./featuredCard";
import { carouselCardWidthPx } from "./uiCustomization";

/** The pixel values a set of `prefix-[NNpx]` utility classes carries, in order. */
function pxValues(classes: string, prefix: string): number[] {
  return [...classes.matchAll(new RegExp(`${prefix}-\\[(\\d+)px\\]`, "g"))].map((match) =>
    Number(match[1]),
  );
}

describe("row card treatment", () => {
  it("treats a row one way at most", () => {
    // A card cannot be both numbered and opening. The two lists sit in one file
    // for exactly this reason, and this is what stops them drifting apart.
    const both = [...RANKED_SECTION_TYPES].filter((type) => EXPANDING_SECTION_TYPES.has(type));
    expect(both).toEqual([]);
  });

  it("opens the tile to FEATURED_CARD_SCALE times the Featured card", () => {
    // The tile keeps the card's height, so its width is the whole of what makes
    // it read as landscape. Kept in step with the Featured row's own resting
    // width, or an open tile is the wrong size on one breakpoint only.
    const viewports = [500, 800, 1200, 1440, 1700];

    for (const size of ["compact", "standard", "large"] as const) {
      const widths = pxValues(expandedCardWidthClasses(size), "w");
      expect(widths).toHaveLength(5);

      for (const [index, viewportWidth] of viewports.entries()) {
        const width = widths[index];
        if (width === undefined) {
          throw new Error(`expanded ${size} card has no width for a ${viewportWidth}px viewport`);
        }

        expect(width).toBe(
          Math.round(carouselCardWidthPx(size, viewportWidth, "featured") * FEATURED_CARD_SCALE),
        );
      }
    }
  });
});
