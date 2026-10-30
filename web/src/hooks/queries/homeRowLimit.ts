import type { HomeSectionItemsResponse, ResolvedSection } from "@/api/types";
import { visibleCarouselSlideCount } from "@/lib/carouselWindow";

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
 * Whether a row read with itemLimit may hold more cards than it answered.
 * The server reports the smaller of the row's own limit and itemLimit, so a
 * row under the cap was answered whole. A row at the cap may hold more when
 * it filled the cap, or when Home's hide-watched filter cut it short while
 * its source counts more.
 */
export function isPartialHomeRow(section: ResolvedSection, itemLimit: number | undefined): boolean {
  if (itemLimit === undefined || section.item_limit < itemLimit) return false;
  return section.items.length >= itemLimit || section.total_count > section.items.length;
}

/** The cached form of a row read with itemLimit, or read whole without one. */
export function homeRowResponse(
  section: ResolvedSection,
  itemLimit?: number,
): HomeSectionItemsResponse {
  return isPartialHomeRow(section, itemLimit) ? { section, partial: true } : { section };
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
 */
export function completeHomeRow(
  shown: HomeSectionItemsResponse,
  whole: HomeSectionItemsResponse,
): HomeSectionItemsResponse {
  const shownIds = new Set(shown.section.items.map((item) => item.content_id));
  const rest = whole.section.items.filter((item) => !shownIds.has(item.content_id));
  const length = Math.max(whole.section.item_limit, whole.section.items.length);
  return {
    section: { ...whole.section, items: [...shown.section.items, ...rest].slice(0, length) },
    shown: shown.section.items.length,
  };
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
