import type { HomeSectionItemsResponse } from "@/api/types";
import { sectionFromV2 } from "@/api/v2/catalog";
import type { components } from "@/api/v2/schema";
import { visibleCarouselSlideCount } from "@/lib/carouselWindow";

type SectionV2 = components["schemas"]["Section"];

/** The viewport width, in CSS pixels, a Home row's first read is sized for. */
const HOME_ROW_REFERENCE_VIEWPORT_PX = 1920;

/**
 * The cards a Home row reads first: two screens of standard posters at a
 * 1920-pixel viewport, the cards in view plus the page one arrow press
 * scrolls to. That is 20, the size the Android and Apple apps ask for, so the
 * server's capped-row cache serves all three. A row whose carousel nears the
 * last of these cards reads the rest (Home's completeSection), at once on a
 * wider screen or with smaller posters.
 */
export const HOME_ROW_ITEM_LIMIT =
  2 * visibleCarouselSlideCount("standard", HOME_ROW_REFERENCE_VIEWPORT_PX);

/**
 * The cached form of a Home row read. A read with item_limit is partial when
 * the server reports the row holds more cards than it answered (has_more);
 * Home's completeSection then reads the rest. The server sends has_more only
 * to a read with item_limit, so a row read whole is never partial.
 */
export function homeRowResponse(section: SectionV2): HomeSectionItemsResponse {
  const row = { section: sectionFromV2(section) };
  return section.has_more ? { ...row, partial: true } : row;
}

/**
 * The item_limit to read a Home row with. A featured row is read whole: the
 * hero counts and cycles through every card. So is a row already completed or
 * read whole past the cap, so a refresh never shrinks a row the viewer has
 * scrolled.
 */
export function homeRowItemLimit(
  featured: boolean,
  cached: HomeSectionItemsResponse | undefined,
): number | undefined {
  if (featured) return undefined;
  if (
    cached &&
    !cached.partial &&
    (cached.shown !== undefined || cached.section.items.length > HOME_ROW_ITEM_LIMIT)
  ) {
    return undefined;
  }
  return HOME_ROW_ITEM_LIMIT;
}

/**
 * A partial row completed by the whole row: the cards already shown, in
 * place, then the whole row's cards they lack, up to the row's own limit.
 * Rows that draw a subset sized by their limit (hidden gems, random picks)
 * can draw different cards at the larger size, and keeping the shown ones in
 * place stops a carousel the viewer is scrolling from shifting.
 *
 * `current` is the row as cached when the whole row arrives, without any
 * card the viewer removed (Not interested, a dismissal) since `shown` was
 * read. Such a card stays out even when the whole row, read before the
 * server heard of the removal, still holds it.
 */
export function completeHomeRow(
  shown: HomeSectionItemsResponse,
  whole: HomeSectionItemsResponse,
  current: HomeSectionItemsResponse = shown,
): HomeSectionItemsResponse {
  const seenIds = new Set(
    [...shown.section.items, ...current.section.items].map((item) => item.content_id),
  );
  const rest = whole.section.items.filter((item) => !seenIds.has(item.content_id));
  const length = Math.max(whole.section.item_limit, whole.section.items.length);
  return {
    section: { ...whole.section, items: [...current.section.items, ...rest].slice(0, length) },
    shown: current.section.items.length,
    shownAt: nextShownAt(),
  };
}

let lastShownAt = 0;

/**
 * A completion time later than every earlier one, even within the same
 * millisecond: Home restores its rows' duplicate-filter order by it, and a
 * tie would fall back to layout order.
 */
function nextShownAt(): number {
  lastShownAt = Math.max(Date.now(), lastShownAt + 1);
  return lastShownAt;
}

/**
 * How many of a row's first cards Home's duplicate filter compares before
 * any row's later cards (see dropCrossSectionDuplicates), or undefined for
 * all of them. A completed row keeps the count it showed before; any other
 * row read whole past the cap counts its first read's worth.
 */
export function homeRowShownCount(
  featured: boolean,
  row: HomeSectionItemsResponse | undefined,
): number | undefined {
  if (featured || !row) return undefined;
  if (row.shown !== undefined) return row.shown;
  return homeRowItemLimit(false, row) === undefined ? HOME_ROW_ITEM_LIMIT : undefined;
}
