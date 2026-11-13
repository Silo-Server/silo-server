/**
 * Plain names and picker groups for Home row kinds. The stored `section_type`
 * values never change; this is display only.
 */
import { t, type MessageKey } from "@/i18n";
import {
  presetLabel,
  presetShortText,
  type RecipeCatalogResponse,
  type RecipeDefinition,
} from "@/lib/recipes";

export type RowGroup = "keep" | "new" | "popular" | "picked" | "moods" | "collections";

export const ROW_GROUP_ORDER: readonly RowGroup[] = [
  "keep",
  "new",
  "popular",
  "picked",
  "moods",
  "collections",
];

export const ROW_GROUP_LABELS: Record<RowGroup, MessageKey> = {
  keep: "web.home.rows.groups.keep",
  new: "web.home.rows.groups.new",
  popular: "web.home.rows.groups.popular",
  picked: "web.home.rows.groups.picked",
  moods: "web.home.rows.groups.moods",
  collections: "web.home.rows.groups.collections",
};

/** The line after each group's name in the picker. */
export const ROW_GROUP_BLURBS: Record<RowGroup, MessageKey> = {
  keep: "web.home.rows.groups.keepBlurb",
  new: "web.home.rows.groups.newBlurb",
  popular: "web.home.rows.groups.popularBlurb",
  picked: "web.home.rows.groups.pickedBlurb",
  moods: "web.home.rows.groups.moodsBlurb",
  collections: "web.home.rows.groups.collectionsBlurb",
};

interface RowKind {
  group: RowGroup;
  /** The English name a new row is saved with when it gets no other: stored data, never translated. */
  name: string;
  label: MessageKey;
  /** The picker card's sentence: what the row shows. */
  sentence: MessageKey;
  /** Each viewer sees their own titles. */
  personal?: boolean;
}

// Insertion order is the picker's card order within each group.
const ROW_KINDS: Record<string, RowKind> = {
  continue_watching: {
    group: "keep",
    name: "Continue watching",
    label: "web.home.rows.kinds.continueWatching",
    sentence: "web.home.rows.kindSentences.continueWatching",
    personal: true,
  },
  next_up: {
    group: "keep",
    name: "Next up",
    label: "web.home.rows.kinds.nextUp",
    sentence: "web.home.rows.kindSentences.nextUp",
    personal: true,
  },
  next_in_series: {
    group: "keep",
    name: "Next in series",
    label: "web.home.rows.kinds.nextInSeries",
    sentence: "web.home.rows.kindSentences.nextInSeries",
    personal: true,
  },
  watchlist: {
    group: "keep",
    name: "Watchlist",
    label: "web.home.rows.kinds.watchlist",
    sentence: "web.home.rows.kindSentences.watchlist",
    personal: true,
  },
  favorites: {
    group: "keep",
    name: "Favorites",
    label: "web.home.rows.kinds.favorites",
    sentence: "web.home.rows.kindSentences.favorites",
    personal: true,
  },
  recently_added: {
    group: "new",
    name: "Recently added",
    label: "web.home.rows.kinds.recentlyAdded",
    sentence: "web.home.rows.kindSentences.recentlyAdded",
  },
  recently_released: {
    group: "new",
    name: "New releases",
    label: "web.home.rows.kinds.recentlyReleased",
    sentence: "web.home.rows.kindSentences.recentlyReleased",
  },
  new_to_library: {
    group: "new",
    name: "New this month",
    label: "web.home.rows.kinds.newToLibrary",
    sentence: "web.home.rows.kindSentences.newToLibrary",
  },
  returning_shows: {
    group: "new",
    name: "Returning shows",
    label: "web.home.rows.kinds.returningShows",
    sentence: "web.home.rows.kindSentences.returningShows",
    personal: true,
  },
  trending_on_server: {
    group: "popular",
    name: "Trending on this server",
    label: "web.home.rows.kinds.trendingOnServer",
    sentence: "web.home.rows.kindSentences.trendingOnServer",
  },
  most_watched: {
    group: "popular",
    name: "Most watched",
    label: "web.home.rows.kinds.mostWatched",
    sentence: "web.home.rows.kindSentences.mostWatched",
  },
  trending_discover: {
    group: "popular",
    name: "Trending worldwide",
    label: "web.home.rows.kinds.trendingDiscover",
    sentence: "web.home.rows.kindSentences.trendingDiscover",
  },
  profile_activity_feed: {
    group: "popular",
    name: "What others just watched",
    label: "web.home.rows.kinds.profileActivityFeed",
    sentence: "web.home.rows.kindSentences.profileActivityFeed",
  },
  recommended_for_you: {
    group: "picked",
    name: "Recommended for you",
    label: "web.home.rows.kinds.recommendedForYou",
    sentence: "web.home.rows.kindSentences.recommendedForYou",
    personal: true,
  },
  because_you_watched: {
    group: "picked",
    name: "Because you watched",
    label: "web.home.rows.kinds.becauseYouWatched",
    sentence: "web.home.rows.kindSentences.becauseYouWatched",
    personal: true,
  },
  similar_users_liked: {
    group: "picked",
    name: "Profiles like you enjoyed",
    label: "web.home.rows.kinds.similarUsersLiked",
    sentence: "web.home.rows.kindSentences.similarUsersLiked",
    personal: true,
  },
  taste_match: {
    group: "picked",
    name: "Top picks today",
    label: "web.home.rows.kinds.tasteMatch",
    sentence: "web.home.rows.kindSentences.tasteMatch",
    personal: true,
  },
  mood_collection: {
    group: "moods",
    name: "Mood picks",
    label: "web.home.rows.kinds.moodCollection",
    sentence: "web.home.rows.kindSentences.moodCollection",
  },
  seasonal_themed: {
    group: "moods",
    name: "Seasonal picks",
    label: "web.home.rows.kinds.seasonalThemed",
    sentence: "web.home.rows.kindSentences.seasonalThemed",
  },
  editorial_spotlight: {
    group: "moods",
    name: "Spotlight",
    label: "web.home.rows.kinds.editorialSpotlight",
    sentence: "web.home.rows.kindSentences.editorialSpotlight",
  },
  format_showcase: {
    group: "moods",
    name: "4K & HDR showcase",
    label: "web.home.rows.kinds.formatShowcase",
    sentence: "web.home.rows.kindSentences.formatShowcase",
  },
  hidden_gems: {
    group: "moods",
    name: "Hidden gems",
    label: "web.home.rows.kinds.hiddenGems",
    sentence: "web.home.rows.kindSentences.hiddenGems",
    personal: true,
  },
  critically_acclaimed: {
    group: "moods",
    name: "Critically acclaimed",
    label: "web.home.rows.kinds.criticallyAcclaimed",
    sentence: "web.home.rows.kindSentences.criticallyAcclaimed",
  },
  forgotten_favorites: {
    group: "moods",
    name: "Forgotten favorites",
    label: "web.home.rows.kinds.forgottenFavorites",
    sentence: "web.home.rows.kindSentences.forgottenFavorites",
    personal: true,
  },
  genre_roulette: {
    group: "moods",
    name: "Genre roulette",
    label: "web.home.rows.kinds.genreRoulette",
    sentence: "web.home.rows.kindSentences.genreRoulette",
  },
  random: {
    group: "moods",
    name: "Surprise me",
    label: "web.home.rows.kinds.random",
    sentence: "web.home.rows.kindSentences.random",
  },
  short_watches: {
    group: "moods",
    name: "Short & sweet",
    label: "web.home.rows.kinds.shortWatches",
    sentence: "web.home.rows.kindSentences.shortWatches",
  },
  anniversaries: {
    group: "moods",
    name: "Anniversaries",
    label: "web.home.rows.kinds.anniversaries",
    sentence: "web.home.rows.kindSentences.anniversaries",
  },
  award_winners: {
    group: "moods",
    name: "Award winners (no longer offered)",
    label: "web.home.rows.kinds.awardWinners",
    sentence: "web.home.rows.kindSentences.awardWinners",
  },
  collection: {
    group: "collections",
    name: "A collection",
    label: "web.home.rows.kinds.collection",
    sentence: "web.home.rows.kindSentences.collection",
  },
  custom_filter: {
    group: "collections",
    name: "Titles matching rules",
    label: "web.home.rows.kinds.customFilter",
    sentence: "web.home.rows.kindSentences.customFilter",
  },
  genre: {
    group: "collections",
    name: "Genre (no longer offered)",
    label: "web.home.rows.kinds.genre",
    sentence: "web.home.rows.kindSentences.genre",
  },
  admin_curated_list: {
    group: "collections",
    name: "Editor's picks",
    label: "web.home.rows.kinds.adminCuratedList",
    sentence: "web.home.rows.kindSentences.adminCuratedList",
  },
};

/**
 * Kinds an existing row may still have, but that no picker offers for a new
 * row. The server adds the only Next up row itself, when a profile's Next up
 * episodes setting is Separate row, and refuses one added by hand.
 */
export const NOT_OFFERED_FOR_NEW_ROWS: ReadonlySet<string> = new Set([
  "admin_curated_list",
  "genre",
  "award_winners",
  "next_up",
]);

/** The plain name of a row kind; unknown kinds read "Row", never a raw type key. */
export function rowKindLabel(sectionType: string): string {
  return t(ROW_KINDS[sectionType]?.label ?? "web.home.rows.kinds.unknown");
}

/**
 * The English name a row of this kind is saved with when nothing else names
 * it. A row's title is stored data, so this is never translated.
 */
export function rowKindName(sectionType: string): string {
  return ROW_KINDS[sectionType]?.name ?? "Row";
}

export function rowKindGroup(sectionType: string): RowGroup {
  return ROW_KINDS[sectionType]?.group ?? "collections";
}

/** What the row shows, as one sentence ending in a full stop. */
export function rowKindSentence(sectionType: string): string {
  return t(ROW_KINDS[sectionType]?.sentence ?? "web.home.rows.kindSentences.unknown");
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
    const award = typeof config.award_type === "string" ? config.award_type : "";
    return t("web.home.rows.kinds.retiredAward", { award });
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
  if (kind) {
    return {
      type: def.type,
      group: kind.group,
      label: t(kind.label),
      sentence: t(kind.sentence),
      def,
    };
  }
  // A kind this web build does not know yet still shows, under its own name.
  const preset = def.presets[0];
  return {
    type: def.type,
    group: "moods",
    label: preset ? presetLabel(preset) : t("web.home.rows.kinds.unknown"),
    sentence: preset ? presetShortText(preset) : rowKindSentence(def.type),
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
    label: t(ROW_GROUP_LABELS[group]),
    blurb: t(ROW_GROUP_BLURBS[group]),
    cards: cards.filter((card) => card.group === group),
  })).filter((group) => group.cards.length > 0);
}
