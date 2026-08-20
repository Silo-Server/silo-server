export type PosterSize = "compact" | "standard" | "large";
export type CardCaption = "title_metadata" | "title" | "artwork";

export interface CardPresentation {
  poster_size: PosterSize;
  caption: CardCaption;
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

export const DEFAULT_CARD_PRESENTATION: CardPresentation = {
  poster_size: "standard",
  caption: "title_metadata",
};

export const CARD_PRESENTATION_PRESETS = [
  {
    id: "balanced",
    label: "Balanced",
    description: "Standard posters with titles and metadata.",
    value: DEFAULT_CARD_PRESENTATION,
  },
  {
    id: "compact",
    label: "Compact",
    description: "More posters on screen with titles only.",
    value: { poster_size: "compact", caption: "title" },
  },
  {
    id: "cinema",
    label: "Cinema",
    description: "Large posters with titles only.",
    value: { poster_size: "large", caption: "title" },
  },
  {
    id: "artwork",
    label: "Artwork only",
    description: "Large artwork without caption rows.",
    value: { poster_size: "large", caption: "artwork" },
  },
] as const satisfies ReadonlyArray<{
  id: string;
  label: string;
  description: string;
  value: CardPresentation;
}>;

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
const CARD_CAPTIONS = new Set<CardCaption>(["title_metadata", "title", "artwork"]);

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
  const caption = value.caption;
  if (
    typeof posterSize !== "string" ||
    !POSTER_SIZES.has(posterSize as PosterSize) ||
    typeof caption !== "string" ||
    !CARD_CAPTIONS.has(caption as CardCaption)
  ) {
    return DEFAULT_CARD_PRESENTATION;
  }
  return { poster_size: posterSize as PosterSize, caption: caption as CardCaption };
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

export function cardTextAreaHeight(caption: CardCaption): number {
  if (caption === "artwork") return 0;
  return caption === "title" ? 28 : 44;
}
