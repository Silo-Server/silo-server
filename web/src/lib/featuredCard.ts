/*
 * How a row's cards are treated: which rows draw a ranked reel, and which open
 * into a landscape tile when the pointer rests on them.
 *
 * Both sets live here, together, because a card can only be treated one way and
 * two lists in two files is how they would quietly start to overlap.
 *
 * The timings are PCF's own, so the two products feel the same: a pointer that
 * merely crosses the row does nothing, and the tile has settled before anything
 * starts playing over it.
 *
 * Data and numbers only, so the parts worth pinning down are testable without a
 * browser. SectionRow applies the treatment; SectionItemCard draws both.
 */

import type { PosterSize } from "@/lib/uiCustomization";

/**
 * The row types that draw as a ranked reel, numbered beside each poster.
 *
 * Interim, and kept in one place on purpose. The plan is for a row to carry an
 * explicit display style of its own — server-side, on /api/v2 only — at which
 * point these sets become a single field read and an administrator picks the
 * treatment. Until that lands a row is treated by its type.
 *
 * Each type here has to be a list whose order means something, or numbering it
 * claims a ranking the row does not have:
 *
 *   - `trending_discover` — TMDB's trending list matched to the library, already
 *     held in trending order. This is the "top titles today" row.
 *   - `critically_acclaimed` — "Top Rated", ordered by rating.
 *
 * A dev library can leave `trending_discover` legitimately empty: current TMDB
 * trending is new releases, and a small catalogue of older titles matches none
 * of them. An empty row renders nothing at all, so an empty trending row shows
 * neither a heading nor a reel.
 */
export const RANKED_SECTION_TYPES: ReadonlySet<string> = new Set<string>([
  "trending_discover",
  "critically_acclaimed",
]);

/**
 * How long the pointer must rest on a card before it opens. A card that opened
 * on contact would make the row unusable to cross with the mouse.
 */
export const FEATURED_HOVER_DELAY_MS = 400;

/**
 * How long the open tile waits before a clip fades in over the backdrop, so the
 * tile itself settles first.
 */
export const FEATURED_TRAILER_DELAY_MS = 1000;

/** The tile's width as a multiple of the card's resting width. */
export const FEATURED_CARD_SCALE = 2.6;

/**
 * The row types whose cards open into a tile.
 *
 * For now this is the Featured row, which is a `favorites` row: the titles each
 * viewer keeps. A row whose order carries a meaning of its own is a ranked row
 * instead — see {@link RANKED_SECTION_TYPES}.
 *
 * Adding a row type here is the whole of what it takes to give that kind of row
 * the treatment; the two lists must stay disjoint, because one card cannot be
 * both numbered and opening.
 */
export const EXPANDING_SECTION_TYPES: ReadonlySet<string> = new Set<string>(["favorites"]);

/**
 * The open tile's width classes: {@link FEATURED_CARD_SCALE} times the resting
 * card, which the poster sizes in uiCustomization own. Literal strings for the
 * same reason the rest of the card widths are — Tailwind resolves classes by
 * scanning source text, so a computed `w-[NNpx]` would never reach the
 * stylesheet.
 *
 * The tile keeps the card's height, so scaling the width is what makes it read
 * as landscape; nothing grows taller than the row it sits in.
 */
export function expandedCardWidthClasses(size: PosterSize): string {
  switch (size) {
    case "compact":
      return "w-[312px] sm:w-[364px] lg:w-[429px] xl:w-[546px] 2xl:w-[715px]";
    case "large":
      return "w-[442px] sm:w-[507px] lg:w-[611px] xl:w-[780px] 2xl:w-[1014px]";
    default:
      return "w-[364px] sm:w-[416px] lg:w-[520px] xl:w-[676px] 2xl:w-[884px]";
  }
}
