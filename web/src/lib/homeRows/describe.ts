import { queryDefinitionFromSectionConfig, type LibraryCollection } from "@/api/types";
import { t, type MessageKey } from "@/i18n";
import { collectionKindLabel, collectionKindOf } from "@/lib/collections/types";
import { rowKindLabel } from "./catalog";
import type { HomeRow, PageRef, Surface } from "./types";

/**
 * A description is plain text with optional bold names ("The **Ghibli**
 * collection") and warnings ("Collection no longer available").
 */
export type DescriptionPart = string | { strong: string } | { warning: string };

/** A collection a row shows, as the surface knows it. */
export interface CollectionSummary {
  title: string;
  /** Manual, Smart or Synced list. */
  kind?: string;
  /** One of the viewer's own collections: "Your X collection". */
  yours?: boolean;
}

export interface DescribeContext {
  pageKind: "home" | "library";
  /** The profile surface speaks to the viewer ("What you're partway through"). */
  surface?: Surface;
  /**
   * Looks up a collection row's collection: its summary, null when the
   * surface knows it is gone, or undefined when it can't tell (yet).
   */
  collection?: (id: string) => CollectionSummary | null | undefined;
}

/** The plain name of a collection type: Manual, Smart or Synced list. */
export function collectionKind(
  collectionType: LibraryCollection["collection_type"] | undefined,
): string | undefined {
  return collectionType ? collectionKindLabel(collectionKindOf(collectionType)) : undefined;
}

/** The `window` select of the trending messages, by the stored window. */
const SERVER_WINDOWS: Record<string, string> = {
  "24h": "hours24",
  "7d": "days7",
  "30d": "days30",
};

/** The `order` select of rulesAllSorted, keyed "field:order". The default (newest added) says nothing. */
const SORT_ORDERS: Record<string, string> = {
  "rating_imdb:desc": "rating",
  "rating_tmdb:desc": "rating",
  "rating_rt_critic:desc": "rating",
  "rating_rt_audience:desc": "rating",
  "release_date:desc": "released",
  "year:desc": "released",
  "plays:desc": "plays",
  "title:asc": "title",
};

/** How a rule row is ordered, as its More options summary names it. */
const SORT_SUMMARIES: Record<string, MessageKey> = {
  rating: "web.home.rows.describe.sortHighestRated",
  released: "web.home.addRow.params.orderReleaseDesc",
  plays: "web.home.rows.describe.sortMostPlayed",
  title: "web.home.addRow.params.orderTitleAsc",
};

/** The media scopes the rule-row messages name; any other reads "titles". */
const SCOPES = new Set(["movie", "series", "episode", "audiobook", "ebook", "manga", "video"]);

const FORMAT_NAMES: Record<string, string> = {
  "4k": "4K",
  dolby_vision: "Dolby Vision",
  hdr: "HDR",
};

/** The `cadence` select of the rotating messages. */
const CADENCES: Record<string, string> = {
  daily: "day",
  weekly: "week",
  monthly: "month",
};

function str(value: unknown): string | undefined {
  return typeof value === "string" && value !== "" ? value : undefined;
}

function num(value: unknown): number | undefined {
  return typeof value === "number" && Number.isFinite(value) ? value : undefined;
}

/**
 * The TMDB vote minimums rating-led rows require, as the server's
 * recipes.DiscoveryMinVotes and recipes.AcclaimedMinVotes set them.
 */
const MIN_VOTES = "100";
const ACCLAIMED_MIN_VOTES = "500";

/** A setting the server reads as its default unless it is above zero. */
function positive(value: unknown, fallback: number): number {
  const n = num(value);
  return n !== undefined && n > 0 ? n : fallback;
}

/** A TMDB rating floor as the presets write it: 7.5, 8.0. */
function rating(value: unknown, fallback: number): string {
  return (num(value) || fallback).toFixed(1);
}

function cadence(config: Record<string, unknown>): string {
  return CADENCES[str(config.rotation_cadence) ?? "weekly"] ?? "week";
}

/** Stands in for the bold name while a message is formatted, then splits it out. */
const STRONG = "\uE000";

function withStrong(text: string, strong: string): DescriptionPart[] {
  const [before = "", after = ""] = text.split(STRONG);
  return [before, { strong }, after].filter((part) => part !== "");
}

function describeCollection(
  config: Record<string, unknown>,
  context: DescribeContext,
): DescriptionPart[] {
  const id = str(config.library_collection_id) ?? str(config.user_collection_id);
  const collection = id ? context.collection?.(id) : undefined;
  if (collection === null) return [{ warning: t("web.home.rows.describe.collectionGone") }];
  if (!collection) return [t("web.home.rows.describe.collectionUnknown")];
  const text = t("web.home.rows.describe.collection", {
    owner: collection.yours ? "yours" : "other",
    title: STRONG,
    has_kind: collection.kind ? "yes" : "no",
    kind: collection.kind ?? "",
  });
  return withStrong(text, collection.title);
}

function sortKey(config: Record<string, unknown>): { key: string; newestAdded: boolean } {
  const { field, order } = queryDefinitionFromSectionConfig(config).sort;
  return { key: `${field}:${order}`, newestAdded: field === "added_at" && order === "desc" };
}

/**
 * How a rule row is ordered, for its More options summary ("Highest rated
 * first"). Orders without a plain phrase read "Custom order".
 */
export function ruleSortSummary(config: Record<string, unknown>): string {
  const { key, newestAdded } = sortKey(config);
  if (newestAdded) return t("web.home.addRow.params.orderAddedDesc");
  const summary = SORT_SUMMARIES[SORT_ORDERS[key] ?? ""];
  return t(summary ?? "web.home.rows.describe.sortCustom");
}

function scopeOf(config: Record<string, unknown>): string {
  const scope = queryDefinitionFromSectionConfig(config).media_scope ?? "";
  return SCOPES.has(scope) ? scope : "other";
}

function describeRules(config: Record<string, unknown>): string {
  const query = queryDefinitionFromSectionConfig(config);
  const scope = scopeOf(config);
  const rules = query.groups.reduce((count, group) => count + group.rules.length, 0);
  if (rules > 0) return t("web.home.rows.describe.rulesMatching", { scope, count: rules });
  const { key, newestAdded } = sortKey(config);
  if (newestAdded) return t("web.home.rows.describe.rulesAll", { scope });
  const order = SORT_ORDERS[key];
  return order
    ? t("web.home.rows.describe.rulesAllSorted", { scope, order })
    : t("web.home.rows.describe.rulesChosenOrder", { scope });
}

function describeSpotlight(config: Record<string, unknown>): string {
  const subjectType = str(config.subject_type);
  const subject = str(config.subject);
  if (subjectType === "era") {
    return subject
      ? t("web.home.rows.describe.spotlightEra", { subject })
      : t("web.home.rows.describe.spotlightOneEra");
  }
  if (subjectType === "director" || subjectType === "actor" || subjectType === "studio") {
    if (config.auto_rotate === false && subject) {
      return t("web.home.rows.describe.spotlightPinned", { subject });
    }
    return t("web.home.rows.describe.spotlightRotating", {
      subject_type: subjectType,
      cadence: cadence(config),
    });
  }
  return t("web.home.rows.describe.spotlightChanging");
}

function describeFormat(config: Record<string, unknown>): string {
  const format = FORMAT_NAMES[str(config.format) ?? ""];
  if (!format) return t("web.home.rows.describe.formatAny");
  return config.sort === "recent"
    ? t("web.home.rows.describe.formatRecent", { format })
    : t("web.home.rows.describe.formatTitles", { format });
}

/** Personalized rows as a profile reads them about itself, keyed by kind. */
const PROFILE_SENTENCES: Record<string, MessageKey> = {
  continue_watching: "web.home.rows.describe.ownCwWatching",
  next_up: "web.home.rows.describe.ownNextUp",
  next_in_series: "web.home.rows.describe.ownNextInSeries",
  watchlist: "web.home.rows.describe.ownWatchlist",
  favorites: "web.home.rows.describe.ownFavorites",
  returning_shows: "web.home.rows.describe.ownReturningShows",
  recommended_for_you: "web.home.rows.describe.ownRecommended",
  because_you_watched: "web.home.rows.describe.ownBecauseYouWatched",
  taste_match: "web.home.rows.describe.ownTasteMatch",
};

function profileSentence(row: HomeRow): MessageKey | undefined {
  if (row.sectionType === "continue_watching") {
    if (row.config.continue_type === "listening") return "web.home.rows.describe.ownCwListening";
    if (row.config.continue_type === "reading") return "web.home.rows.describe.ownCwReading";
  }
  return PROFILE_SENTENCES[row.sectionType];
}

/** Personalized rows as the admin surface reads them ("each viewer"), keyed by kind. */
const ADMIN_SENTENCES: Record<string, MessageKey> = {
  next_up: "web.home.rows.describe.nextUp",
  next_in_series: "web.home.rows.describe.nextInSeries",
  watchlist: "web.home.rows.describe.watchlist",
  favorites: "web.home.rows.describe.favorites",
  returning_shows: "web.home.rows.describe.returningShows",
  recommended_for_you: "web.home.rows.describe.recommended",
  because_you_watched: "web.home.rows.describe.becauseYouWatched",
  taste_match: "web.home.rows.describe.tasteMatch",
};

/** Rows whose description never depends on their config, keyed by kind. */
const FIXED_SENTENCES: Record<string, MessageKey> = {
  recently_released: "web.home.rows.describe.recentlyReleased",
  profile_activity_feed: "web.home.rows.describe.activityFeed",
  similar_users_liked: "web.home.rows.describe.similarUsersLiked",
  mood_collection: "web.home.rows.describe.moodPick",
  random: "web.home.rows.describe.randomMix",
  anniversaries: "web.home.rows.describe.anniversaries",
  genre: "web.home.rows.describe.genre",
  admin_curated_list: "web.home.rows.describe.curated",
  award_winners: "web.home.rows.kinds.awardWinners",
};

function describeRecentlyAdded(config: Record<string, unknown>, onHome: boolean): string {
  if (!onHome) return t("web.home.rows.describe.recentlyAddedLibrary", { scope: scopeOf(config) });
  const libraries = queryDefinitionFromSectionConfig(config).library_ids.length;
  if (libraries === 0) return t("web.home.rows.describe.recentlyAddedAll");
  return t("web.home.rows.describe.recentlyAddedFrom", { count: libraries });
}

/**
 * One plain sentence saying what a row shows. Personalized rows say "each
 * viewer" on the admin surface, because its preview shows the admin's own
 * picks, not everyone's; a profile reads them about itself. A row the profile
 * renamed says what it was called instead.
 */
export function describeRow(row: HomeRow, context: DescribeContext): DescriptionPart[] {
  if (row.renamedFrom) {
    return withStrong(t("web.home.rows.describe.renamedFrom", { name: STRONG }), row.renamedFrom);
  }
  const own = context.surface === "profile" ? profileSentence(row) : undefined;
  if (own) return [t(own)];
  return describeKind(row, context);
}

function describeKind(row: HomeRow, context: DescribeContext): DescriptionPart[] {
  const { config } = row;
  const onHome = context.pageKind === "home";
  const fixed = ADMIN_SENTENCES[row.sectionType] ?? FIXED_SENTENCES[row.sectionType];
  if (fixed) return [t(fixed)];
  switch (row.sectionType) {
    case "continue_watching":
      if (config.continue_type === "listening") return [t("web.home.rows.describe.cwListening")];
      if (config.continue_type === "reading") return [t("web.home.rows.describe.cwReading")];
      return [t("web.home.rows.describe.cwWatching")];
    case "recently_added":
      return [describeRecentlyAdded(config, onHome)];
    case "new_to_library": {
      const days = num(config.lookback_days) ?? 30;
      return [t("web.home.rows.describe.newToLibrary", { count: days, days: String(days) })];
    }
    case "trending_on_server": {
      const window = SERVER_WINDOWS[str(config.window) ?? ""];
      if (window) {
        return [
          t(
            onHome ? "web.home.rows.describe.trendingHome" : "web.home.rows.describe.trendingHere",
            {
              window,
            },
          ),
        ];
      }
      return [
        t(
          onHome
            ? "web.home.rows.describe.trendingHomeLately"
            : "web.home.rows.describe.trendingHereLately",
        ),
      ];
    }
    case "most_watched":
      return [
        t("web.home.rows.describe.mostWatched", {
          window: config.window === "month" ? "month" : "week",
        }),
      ];
    case "trending_discover": {
      // The movies-only and TV-only wording is new; its catalog keys are a follow-up,
      // so those two stay English until the catalog has them.
      if (config.filter_type === "movie" || config.filter_type === "series") {
        const what = config.filter_type === "movie" ? "Movies trending" : "TV shows trending";
        return [
          `${what} on TMDB ${config.window === "day" ? "today" : "this week"}, from what you have`,
        ];
      }
      return [
        t("web.home.rows.describe.trendingDiscover", {
          window: config.window === "day" ? "day" : "week",
        }),
      ];
    }
    case "seasonal_themed": {
      const themes = config.enabled_themes;
      const family =
        (Array.isArray(themes) && themes.length === 1 && themes[0] === "family_movie_night") ||
        (themes === undefined && config.theme === "family_movie_night");
      return [
        t(family ? "web.home.rows.describe.seasonalFamily" : "web.home.rows.describe.seasonal"),
      ];
    }
    case "editorial_spotlight":
      return [describeSpotlight(config)];
    case "format_showcase":
      return [describeFormat(config)];
    case "hidden_gems": {
      const plays = Math.max(num(config.max_play_count) ?? 0, 0);
      return [
        t("web.home.rows.describe.hiddenGems", {
          rating: rating(config.min_rating, 7.5),
          votes: MIN_VOTES,
          plays,
          // The count as typed: `#` would group it (1,234).
          plays_text: String(plays),
        }),
      ];
    }
    case "critically_acclaimed":
      return [
        t("web.home.rows.describe.acclaimed", {
          rating: rating(config.min_score, 8),
          votes: ACCLAIMED_MIN_VOTES,
        }),
      ];
    case "forgotten_favorites": {
      const days = positive(config.lookback_days, 365);
      return [
        t("web.home.rows.describe.forgotten", {
          rating: "7.0",
          votes: MIN_VOTES,
          count: days,
          days: String(days),
        }),
      ];
    }
    case "genre_roulette":
      return [
        t("web.home.rows.describe.genreRoulette", {
          cadence: cadence(config),
          rating: rating(config.min_rating, 6),
          votes: MIN_VOTES,
        }),
      ];
    case "short_watches": {
      const minutes = positive(config.max_minutes, 95);
      return [
        t("web.home.rows.describe.shortWatches", {
          count: minutes,
          minutes: String(minutes),
          rating: rating(config.min_rating, 6),
          votes: MIN_VOTES,
        }),
      ];
    }
    case "collection":
      return describeCollection(config, context);
    case "custom_filter":
      return [describeRules(config)];
    default:
      return [rowKindLabel(row.sectionType)];
  }
}

export function titleCount(count: number): string {
  return t("web.home.rows.describe.titleCount", { count, count_text: String(count) });
}

/**
 * Which wording a profile page's name takes in a sentence: Home's, or a library
 * page's. It goes by the label as the English always has, so a library named
 * "Home" reads like Home; changing that is a wording fix of its own.
 */
export function profilePageKind(page: PageRef, label: string): PageRef["kind"] {
  return page.kind === "home" || label === "Home" ? "home" : "library";
}

/**
 * "Home" or "Movies page", after "my" or "your", for Settings > Home Screen's
 * menu items, which are not translated yet: English only, so they read whole.
 */
export function profilePageName(page: PageRef, label: string): string {
  return profilePageKind(page, label) === "home" ? "Home" : `${label} page`;
}

/** The switch's accessible name says what it does, not just "Enabled". */
export function rowSwitchLabel(
  surface: Surface,
  row: HomeRow,
  page: PageRef,
  pageLabel: string,
): string {
  if (surface === "profile") {
    return t("web.home.rows.describe.switchProfile", {
      row: row.title,
      page_kind: profilePageKind(page, pageLabel),
      page: pageLabel,
    });
  }
  return t("web.home.rows.describe.switchAdmin", {
    shown: row.shown ? "yes" : "no",
    row: row.title,
  });
}

/** The line after the bold title on a collapsed (off or hidden) row. */
export function collapsedRowText(surface: Surface, page: PageRef, pageLabel: string): string {
  const text =
    surface === "profile"
      ? t("web.home.rows.describe.collapsedProfile", {
          page_kind: profilePageKind(page, pageLabel),
          page: pageLabel,
        })
      : t("web.home.rows.describe.collapsedAdmin");
  return ` ${text}`;
}
