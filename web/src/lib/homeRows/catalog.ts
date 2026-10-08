/**
 * Plain names and picker groups for Home row kinds. The stored `section_type`
 * values never change; this is display only.
 */
import type { RecipeCatalogResponse, RecipeDefinition } from "@/lib/recipes";

export type RowGroup = "keep" | "new" | "popular" | "picked" | "moods" | "collections";

export const ROW_GROUP_ORDER: readonly RowGroup[] = [
  "keep",
  "new",
  "popular",
  "picked",
  "moods",
  "collections",
];

export const ROW_GROUP_LABELS: Record<RowGroup, string> = {
  keep: "Keep watching",
  new: "What's new",
  popular: "Popular",
  picked: "Picked for you",
  moods: "Moods & themes",
  collections: "Collections & rules",
};

/** The line after each group's name in the picker. */
export const ROW_GROUP_BLURBS: Record<RowGroup, string> = {
  keep: "Pick up where each viewer left off",
  new: "Fresh in your libraries",
  popular: "What people here, and elsewhere, are watching",
  picked: "Different for every viewer",
  moods: "Ready-made picks with a theme",
  collections: "Your collections, or rows built from rules",
};

interface RowKind {
  group: RowGroup;
  label: string;
  /** The picker card's sentence: what the row shows. */
  sentence: string;
  /** Each viewer sees their own titles. */
  personal?: boolean;
}

// Insertion order is the picker's card order within each group.
const ROW_KINDS: Record<string, RowKind> = {
  continue_watching: {
    group: "keep",
    label: "Continue watching",
    sentence: "Pick up where each viewer left off.",
    personal: true,
  },
  next_up: {
    group: "keep",
    label: "On deck",
    sentence: "The next episode of each show a viewer follows.",
    personal: true,
  },
  next_in_series: {
    group: "keep",
    label: "Next in series",
    sentence: "Audiobooks (beta): the next book in each series a viewer finished.",
    personal: true,
  },
  watchlist: {
    group: "keep",
    label: "Watchlist",
    sentence: "What each viewer saved to watch.",
    personal: true,
  },
  favorites: {
    group: "keep",
    label: "Favorites",
    sentence: "Each viewer's favorites.",
    personal: true,
  },
  recently_added: {
    group: "new",
    label: "Recently added",
    sentence: "The newest additions to your libraries.",
  },
  recently_released: {
    group: "new",
    label: "New releases",
    sentence: "Titles by release date, newest first.",
  },
  new_to_library: {
    group: "new",
    label: "New this month",
    sentence: "Everything added in the last 30 days.",
  },
  returning_shows: {
    group: "new",
    label: "Returning shows",
    sentence: "Shows each viewer watched that have a new season.",
    personal: true,
  },
  trending_on_server: {
    group: "popular",
    label: "Trending on this server",
    sentence: "What's been played most here lately.",
  },
  most_watched: {
    group: "popular",
    label: "Most watched",
    sentence: "Titles finished most often on this server.",
  },
  trending_discover: {
    group: "popular",
    label: "Trending worldwide",
    sentence: "Popular on TMDB, from titles you have.",
  },
  profile_activity_feed: {
    group: "popular",
    label: "What others just watched",
    sentence: "The latest plays from other profiles in the household.",
  },
  recommended_for_you: {
    group: "picked",
    label: "Recommended for you",
    sentence: "Picked from each viewer's own watch history.",
    personal: true,
  },
  because_you_watched: {
    group: "picked",
    label: "Because you watched",
    sentence: "More like the last thing each viewer finished.",
    personal: true,
  },
  similar_users_liked: {
    group: "picked",
    label: "Profiles like you enjoyed",
    sentence: "What viewers with similar taste loved.",
    personal: true,
  },
  taste_match: {
    group: "picked",
    label: "Top picks today",
    sentence: "A fresh daily set for each viewer.",
    personal: true,
  },
  mood_collection: {
    group: "moods",
    label: "Mood picks",
    sentence: "A row built around a feeling.",
  },
  seasonal_themed: {
    group: "moods",
    label: "Seasonal picks",
    sentence: "Holiday favorites that show up when the season comes.",
  },
  editorial_spotlight: {
    group: "moods",
    label: "Spotlight",
    sentence: "One director, actor, studio or decade at a time.",
  },
  format_showcase: {
    group: "moods",
    label: "4K & HDR showcase",
    sentence: "Your best-looking titles.",
  },
  hidden_gems: {
    group: "moods",
    label: "Hidden gems",
    sentence: "Well rated, but rarely watched.",
  },
  critically_acclaimed: {
    group: "moods",
    label: "Critically acclaimed",
    sentence: "Titles rated highest by critics.",
  },
  forgotten_favorites: {
    group: "moods",
    label: "Forgotten favorites",
    sentence: "Titles nobody has watched in a year.",
  },
  genre_roulette: {
    group: "moods",
    label: "Genre roulette",
    sentence: "A different genre every week.",
  },
  random: { group: "moods", label: "Surprise me", sentence: "A random mix from your libraries." },
  short_watches: {
    group: "moods",
    label: "Short & sweet",
    sentence: "Well-rated movies under 95 minutes.",
  },
  anniversaries: {
    group: "moods",
    label: "Anniversaries",
    sentence: "Titles marking a release milestone this month.",
  },
  award_winners: {
    group: "moods",
    label: "Award winners (no longer offered)",
    sentence: "Award winners.",
  },
  collection: {
    group: "collections",
    label: "A collection",
    sentence: "Show one of your collections.",
  },
  custom_filter: {
    group: "collections",
    label: "Titles matching rules",
    sentence: "Build a row from rules, like genre and decade.",
  },
  genre: {
    group: "collections",
    label: "Genre (no longer offered)",
    sentence: "Titles from chosen genres.",
  },
  admin_curated_list: {
    group: "collections",
    label: "Editor's picks",
    sentence: "Hand-picked titles.",
  },
};

/** Kinds an existing row may still have, but that no picker offers for a new row. */
export const NOT_OFFERED_FOR_NEW_ROWS: ReadonlySet<string> = new Set([
  "admin_curated_list",
  "genre",
  "award_winners",
]);

const AWARD_NAMES: Record<string, string> = {
  oscar: "Oscar Winners",
  emmy: "Emmy Winners",
  cannes: "Cannes Selections",
};

/** The plain name of a row kind; unknown kinds read "Row", never a raw type key. */
export function rowKindLabel(sectionType: string): string {
  return ROW_KINDS[sectionType]?.label ?? "Row";
}

export function rowKindGroup(sectionType: string): RowGroup {
  return ROW_KINDS[sectionType]?.group ?? "collections";
}

/** What the row shows, as one sentence ending in a full stop. */
export function rowKindSentence(sectionType: string): string {
  return ROW_KINDS[sectionType]?.sentence ?? "A row of titles.";
}

/** Each viewer sees their own titles in this kind of row. */
export function isPersonalRowKind(sectionType: string): boolean {
  return ROW_KINDS[sectionType]?.personal === true;
}

/**
 * The "Shows:" name of a kind the picker no longer offers. Award rows keep
 * their award in the name ("Oscar Winners (no longer offered)").
 */
export function retiredKindLabel(sectionType: string, config: Record<string, unknown>): string {
  if (sectionType === "award_winners") {
    const award = typeof config.award_type === "string" ? AWARD_NAMES[config.award_type] : "";
    return `${award || "Award winners"} (no longer offered)`;
  }
  return rowKindLabel(sectionType);
}

export interface PickerCard {
  type: string;
  group: RowGroup;
  label: string;
  sentence: string;
  def: RecipeDefinition;
}

export interface PickerGroup {
  group: RowGroup;
  label: string;
  blurb: string;
  cards: PickerCard[];
}

const KIND_ORDER = Object.keys(ROW_KINDS);

function cardFor(def: RecipeDefinition): PickerCard {
  const kind = ROW_KINDS[def.type];
  if (kind) return { type: def.type, ...kind, def };
  // A kind this web build does not know yet still shows, under its own name.
  const preset = def.presets[0];
  return {
    type: def.type,
    group: "moods",
    label: preset?.display_name ?? "Row",
    sentence: preset?.description_short ?? rowKindSentence(def.type),
    def,
  };
}

/**
 * The picker's groups and cards: one card per row kind the catalog offers,
 * minus the kinds no longer offered for new rows. Every surface offers rule
 * rows; the one admin-only kind, Editor's picks, is no longer offered.
 */
export function pickerGroups(catalog: RecipeCatalogResponse | undefined): PickerGroup[] {
  const defs = Object.values(catalog?.categories ?? {})
    .flatMap((list) => list ?? [])
    .filter(
      (def) =>
        !NOT_OFFERED_FOR_NEW_ROWS.has(def.type) &&
        (def.presets.length > 0 || def.type === "custom_filter"),
    );
  const rank = (type: string) => {
    const index = KIND_ORDER.indexOf(type);
    return index === -1 ? KIND_ORDER.length : index;
  };
  const cards = defs.map(cardFor).sort((a, b) => rank(a.type) - rank(b.type));
  return ROW_GROUP_ORDER.map((group) => ({
    group,
    label: ROW_GROUP_LABELS[group],
    blurb: ROW_GROUP_BLURBS[group],
    cards: cards.filter((card) => card.group === group),
  })).filter((group) => group.cards.length > 0);
}
