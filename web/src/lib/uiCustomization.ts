export type PosterSize = "compact" | "standard" | "large";
export interface CardPresentation {
  poster_size: PosterSize;
}

export type PrimaryMenuBuiltin =
  | "home"
  | "movies"
  | "series"
  | "music"
  | "audiobooks"
  | "for_you"
  | "calendar";

export interface BuiltinMenuItem {
  type: "builtin";
  destination: PrimaryMenuBuiltin;
}

export interface LibraryMenuItem {
  type: "library";
  library_id: number;
  label: string;
}

export interface SectionMenuItem {
  type: "section";
  library_id: number;
  section_id: string;
  label: string;
}

export interface CollectionMenuItem {
  type: "collection";
  collection_id: string;
  label: string;
  library_id?: number;
}

export type ShortcutTarget = LibraryMenuItem | SectionMenuItem | CollectionMenuItem;
export type PrimaryMenuItem = BuiltinMenuItem | ShortcutTarget;

export interface PrimaryMenuDocument {
  items: PrimaryMenuItem[];
}

export interface ShortcutDocument {
  items: ShortcutTarget[];
}

export const DEFAULT_CARD_PRESENTATION: CardPresentation = { poster_size: "standard" };

/**
 * The stored ui.card_presentation document. caption is retired and every card
 * shows its title and details, but the schema still requires the field.
 */
export function cardPresentationValue(presentation: CardPresentation) {
  return { poster_size: presentation.poster_size, caption: "title_metadata" } as const;
}

const BUILTIN_DESTINATIONS = new Set<PrimaryMenuBuiltin>([
  "home",
  "movies",
  "series",
  "music",
  "audiobooks",
  "for_you",
  "calendar",
]);
const POSTER_SIZES = new Set<PosterSize>(["compact", "standard", "large"]);

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null && !Array.isArray(value);
}

function isPositiveInteger(value: unknown): value is number {
  return typeof value === "number" && Number.isInteger(value) && value > 0;
}

function isLabel(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0 && [...value].length <= 256;
}

function isTargetId(value: unknown): value is string {
  return typeof value === "string" && value.trim().length > 0 && [...value].length <= 128;
}

function parseMenuItem(value: unknown, allowBuiltin: boolean): PrimaryMenuItem | null {
  if (!isObject(value) || typeof value.type !== "string") return null;

  if (value.type === "builtin" && allowBuiltin) {
    if (
      typeof value.destination === "string" &&
      BUILTIN_DESTINATIONS.has(value.destination as PrimaryMenuBuiltin)
    ) {
      return { type: "builtin", destination: value.destination as PrimaryMenuBuiltin };
    }
    return null;
  }

  if (value.type === "library") {
    return isPositiveInteger(value.library_id) && isLabel(value.label)
      ? { type: "library", library_id: value.library_id, label: value.label.trim() }
      : null;
  }

  if (value.type === "section") {
    return isPositiveInteger(value.library_id) &&
      isTargetId(value.section_id) &&
      isLabel(value.label)
      ? {
          type: "section",
          library_id: value.library_id,
          section_id: value.section_id,
          label: value.label.trim(),
        }
      : null;
  }

  if (value.type === "collection") {
    if (
      !isTargetId(value.collection_id) ||
      !isLabel(value.label) ||
      (value.library_id !== undefined && !isPositiveInteger(value.library_id))
    ) {
      return null;
    }
    return {
      type: "collection",
      collection_id: value.collection_id,
      label: value.label.trim(),
      ...(value.library_id === undefined ? {} : { library_id: value.library_id }),
    };
  }

  return null;
}

export function parseCardPresentation(value: unknown): CardPresentation {
  if (!isObject(value)) return DEFAULT_CARD_PRESENTATION;
  const posterSize = value.poster_size;
  if (typeof posterSize !== "string" || !POSTER_SIZES.has(posterSize as PosterSize)) {
    return DEFAULT_CARD_PRESENTATION;
  }
  return { poster_size: posterSize as PosterSize };
}

export function menuItemKey(item: PrimaryMenuItem): string {
  switch (item.type) {
    case "builtin":
      return `builtin:${item.destination}`;
    case "library":
      return `library:${item.library_id}`;
    case "section":
      return `section:${item.library_id}:${item.section_id}`;
    case "collection":
      return JSON.stringify(["collection", item.library_id ?? null, item.collection_id]);
  }
}

export function parsePrimaryMenu(value: unknown): PrimaryMenuDocument | null {
  if (value == null) return null;
  if (!isObject(value) || !Array.isArray(value.items)) return null;

  const items: PrimaryMenuItem[] = [];
  const seen = new Set<string>();
  for (const candidate of value.items.slice(0, 64)) {
    const item = parseMenuItem(candidate, true);
    if (!item) continue;
    const key = menuItemKey(item);
    if (seen.has(key)) continue;
    seen.add(key);
    items.push(item);
  }

  const homeCount = items.filter(
    (item) => item.type === "builtin" && item.destination === "home",
  ).length;
  return items.length > 0 && homeCount === 1 ? { items } : null;
}

export function parseShortcuts(value: unknown): ShortcutDocument {
  if (!isObject(value) || !Array.isArray(value.items)) return { items: [] };
  const items: ShortcutTarget[] = [];
  const seen = new Set<string>();
  for (const candidate of value.items.slice(0, 256)) {
    const item = parseMenuItem(candidate, false);
    if (!item || item.type === "builtin") continue;
    const key = menuItemKey(item);
    if (seen.has(key)) continue;
    seen.add(key);
    items.push(item);
  }
  return { items };
}

export function defaultWebPrimaryMenu(): PrimaryMenuDocument {
  return {
    items: [
      { type: "builtin", destination: "home" },
      { type: "builtin", destination: "for_you" },
      { type: "builtin", destination: "calendar" },
    ],
  };
}

export function moveMenuItem(
  items: readonly PrimaryMenuItem[],
  index: number,
  direction: -1 | 1,
): PrimaryMenuItem[] {
  const target = index + direction;
  if (index < 0 || index >= items.length || target < 0 || target >= items.length) {
    return [...items];
  }
  const next = [...items];
  [next[index], next[target]] = [next[target]!, next[index]!];
  return next;
}

export function cardGridClasses(size: PosterSize): string {
  switch (size) {
    case "compact":
      return "grid grid-cols-3 sm:grid-cols-5 md:grid-cols-6 lg:grid-cols-8 xl:grid-cols-10 gap-3";
    case "large":
      return "grid grid-cols-2 sm:grid-cols-3 md:grid-cols-4 lg:grid-cols-5 xl:grid-cols-6 gap-4";
    default:
      return "grid grid-cols-3 sm:grid-cols-4 md:grid-cols-5 lg:grid-cols-7 xl:grid-cols-8 gap-3";
  }
}

export function carouselCardWidthClasses(size: PosterSize): string {
  switch (size) {
    case "compact":
      return "w-[120px] shrink-0 sm:w-[140px] lg:w-[160px]";
    case "large":
      return "w-[170px] shrink-0 sm:w-[195px] lg:w-[220px]";
    default:
      return "w-[140px] shrink-0 sm:w-[160px] lg:w-[185px]";
  }
}

/**
 * A Featured row's cards are larger than an ordinary row's from the lg step up,
 * and identical to an ordinary row's below it: this is the row whose cards open
 * into a tile, and a big card on a phone is a bad card — there is no hover there
 * to open it, and two tiles to a screen is not a row. It is also the only row
 * that keeps growing past lg.
 *
 * Keep in step with FEATURED_CAROUSEL_WIDTH_PX below.
 */
export function featuredCarouselCardWidthClasses(size: PosterSize): string {
  switch (size) {
    case "compact":
      return "w-[120px] shrink-0 sm:w-[140px] lg:w-[165px] xl:w-[210px] 2xl:w-[275px]";
    case "large":
      return "w-[170px] shrink-0 sm:w-[195px] lg:w-[235px] xl:w-[300px] 2xl:w-[390px]";
    default:
      return "w-[140px] shrink-0 sm:w-[160px] lg:w-[200px] xl:w-[260px] 2xl:w-[340px]";
  }
}

/** Width classes of wide (16:9) carousel cards such as Continue Watching episodes. */
export const WIDE_CAROUSEL_CARD_WIDTH_CLASSES = "w-[260px] shrink-0 sm:w-[315px]";

/**
 * A ranked row's card is wider than its poster: the extra space to the left is
 * the gutter the numeral is drawn in, and the poster is cropped over the
 * numeral's right edge. These are literal class strings for the same reason the
 * poster widths above are — Tailwind resolves classes by scanning source text,
 * so a computed `w-[NNpx]` would never reach the stylesheet.
 *
 * Keep in step with RANKED_CAROUSEL_WIDTH_PX below.
 */
export function rankedCarouselCardWidthClasses(size: PosterSize): string {
  switch (size) {
    case "compact":
      return "w-[232px] shrink-0 sm:w-[270px] lg:w-[309px]";
    case "large":
      return "w-[328px] shrink-0 sm:w-[376px] lg:w-[425px]";
    default:
      return "w-[270px] shrink-0 sm:w-[309px] lg:w-[357px]";
  }
}

/**
 * The ranked gutter as padding, for the card that holds the numeral. It sits on
 * the card rather than inside the poster box so the poster box keeps exactly the
 * poster's width — the hover play button centres on that box, and padding inside
 * it would centre the button on the numeral instead.
 *
 * Keep in step with rankedCarouselCardWidthClasses above.
 */
export function rankedCarouselGutterClasses(size: PosterSize): string {
  switch (size) {
    case "compact":
      return "pl-[112px] sm:pl-[130px] lg:pl-[149px]";
    case "large":
      return "pl-[158px] sm:pl-[181px] lg:pl-[205px]";
    default:
      return "pl-[130px] sm:pl-[149px] lg:pl-[172px]";
  }
}

/**
 * The numeral's own offset: the same gutter, negated. A numeral is anchored
 * inside the poster box, so it has to be pulled left by exactly the gutter the
 * card pads with to reach the card's own left edge.
 *
 * Kept derived from rankedCarouselGutterClasses by the test beside this file,
 * because the two drifting apart is silent: the numeral would sit over the
 * poster instead of beside it.
 */
export function rankedCarouselNumeralClasses(size: PosterSize): string {
  switch (size) {
    case "compact":
      return "-left-[112px] sm:-left-[130px] lg:-left-[149px]";
    case "large":
      return "-left-[158px] sm:-left-[181px] lg:-left-[205px]";
    default:
      return "-left-[130px] sm:-left-[149px] lg:-left-[172px]";
  }
}

/**
 * The ranked width table, as [base, sm, lg] pixels. This is the drawer the
 * class strings above are written out from: the numeral takes roughly the
 * poster's own width, which is what makes the digits read as backdrop rather
 * than as a label.
 */
const RANKED_CAROUSEL_WIDTH_PX: Record<PosterSize, readonly number[]> = {
  compact: [232, 270, 309],
  standard: [270, 309, 357],
  large: [328, 376, 425],
};

/** The ordinary poster widths, as [base, sm, lg]. Every row but Featured uses these. */
const CAROUSEL_CARD_WIDTH_PX: Record<PosterSize, readonly number[]> = {
  compact: [120, 140, 160],
  standard: [140, 160, 185],
  large: [170, 195, 220],
};

/** Wide (16:9) cards, which are already the largest thing in a row. */
const WIDE_CAROUSEL_CARD_WIDTH_PX = [260, 315, 315];

/**
 * The Featured row's own widths, one per entry of CARD_BREAKPOINT_PX. It
 * matches an ordinary row up to lg and leaves it behind from there, so a phone
 * or a tablet draws the Featured row exactly as it draws every other row, and
 * a big screen turns it into the page's showcase.
 */
const FEATURED_CAROUSEL_WIDTH_PX: Record<PosterSize, readonly number[]> = {
  compact: [120, 140, 165, 210, 275],
  standard: [140, 160, 200, 260, 340],
  large: [170, 195, 235, 300, 390],
};

/** The viewport width each entry of a width table applies from, in order. */
const CARD_BREAKPOINT_PX = [0, 640, 1024, 1280, 1536];

/**
 * The entry a table picks for a viewport: the last one whose breakpoint it
 * reaches. A table shorter than CARD_BREAKPOINT_PX simply stops growing, which
 * is how an ordinary card keeps its lg width on any larger screen.
 */
function widthAt(table: readonly number[], viewportWidth: number): number {
  let width = table[0] ?? 0;
  CARD_BREAKPOINT_PX.forEach((minimum, index) => {
    const candidate = table[index];
    if (candidate !== undefined && viewportWidth >= minimum) {
      width = candidate;
    }
  });
  return width;
}

/** Which width table a carousel slide is measured against. */
export type CarouselSlideWidth = "poster" | "ranked" | "featured";

/**
 * The width in pixels that a carousel card of this size resolves to at a
 * viewport width. Keep each table in step with the class string written out
 * from it: carousel windowing sizes its first commit from this, and a mismatch
 * mounts slides sized for a different card.
 *
 * slideWidth names the row it is for: a ranked row's card carries a numeral
 * gutter, and a Featured row's cards are larger from lg up.
 */
export function carouselCardWidthPx(
  size: PosterSize | "wide",
  viewportWidth: number,
  slideWidth: CarouselSlideWidth = "poster",
): number {
  const table =
    size === "wide"
      ? WIDE_CAROUSEL_CARD_WIDTH_PX
      : slideWidth === "ranked"
        ? RANKED_CAROUSEL_WIDTH_PX[size]
        : slideWidth === "featured"
          ? FEATURED_CAROUSEL_WIDTH_PX[size]
          : CAROUSEL_CARD_WIDTH_PX[size];
  return widthAt(table, viewportWidth);
}

/**
 * Placeholder height for an off-screen carousel row rendered under
 * content-visibility: the row header plus a 2:3 poster at this size's card
 * width plus the tallest caption. Kept beside carouselCardWidthClasses so a
 * card-size change updates both; underestimating makes the scrollbar jump as
 * rows render on approach.
 */
export function carouselIntrinsicHeight(size: PosterSize): string {
  switch (size) {
    case "compact":
      return "21rem";
    case "large":
      return "27rem";
    default:
      return "23rem";
  }
}

/** Height of the title and details lines under a grid poster. */
export const CARD_TEXT_AREA_HEIGHT = 44;
