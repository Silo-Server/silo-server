import { queryDefinitionFromSectionConfig, type LibraryCollection } from "@/api/types";
import { COLLECTION_KIND_LABEL, collectionKindOf } from "@/lib/collections/types";
import { rowKindLabel } from "./catalog";
import type { HomeRow, Surface } from "./types";

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
  return collectionType ? COLLECTION_KIND_LABEL[collectionKindOf(collectionType)] : undefined;
}

const SERVER_WINDOWS: Record<string, string> = {
  "24h": "24 hours",
  "7d": "7 days",
  "30d": "30 days",
};

const SCOPE_NOUNS: Record<string, string> = {
  movie: "movies",
  series: "shows",
  episode: "episodes",
  audiobook: "audiobooks",
  ebook: "books",
  manga: "manga",
  video: "movies and shows",
};

/** How a rule row with no rules is ordered, keyed "field:order". The default (newest added) says nothing. */
const SORT_PHRASES: Record<string, string> = {
  "rating_imdb:desc": "highest rated first",
  "rating_tmdb:desc": "highest rated first",
  "rating_rt_critic:desc": "highest rated first",
  "rating_rt_audience:desc": "highest rated first",
  "release_date:desc": "newest released first",
  "year:desc": "newest released first",
  "plays:desc": "most played first",
  "title:asc": "A to Z",
};

const FORMAT_NAMES: Record<string, string> = {
  "4k": "4K",
  dolby_vision: "Dolby Vision",
  hdr: "HDR",
};

const CADENCE_NOUNS: Record<string, string> = {
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
const MIN_VOTES = 100;
const ACCLAIMED_MIN_VOTES = 500;

/** A setting the server reads as its default unless it is above zero. */
function positive(value: unknown, fallback: number): number {
  const n = num(value);
  return n !== undefined && n > 0 ? n : fallback;
}

/** A TMDB rating floor as the presets write it: 7.5, 8.0. */
function rating(value: unknown, fallback: number): string {
  return (num(value) || fallback).toFixed(1);
}

function capitalize(value: string): string {
  return value.charAt(0).toUpperCase() + value.slice(1);
}

function plural(count: number, noun: string): string {
  return `${count} ${noun}${count === 1 ? "" : "s"}`;
}

function cadence(config: Record<string, unknown>): string {
  return CADENCE_NOUNS[str(config.rotation_cadence) ?? "weekly"] ?? "week";
}

function describeCollection(
  config: Record<string, unknown>,
  context: DescribeContext,
): DescriptionPart[] {
  const id = str(config.library_collection_id) ?? str(config.user_collection_id);
  const collection = id ? context.collection?.(id) : undefined;
  if (collection === null) return [{ warning: "Collection no longer available" }];
  if (!collection) return ["A collection"];
  const kind = collection.kind ? ` (${collection.kind})` : "";
  return [collection.yours ? "Your " : "The ", { strong: collection.title }, ` collection${kind}`];
}

/**
 * How a rule row is ordered, for its More options summary ("Highest rated
 * first"). Orders without a plain phrase read "Custom order".
 */
export function ruleSortSummary(config: Record<string, unknown>): string {
  const { field, order } = queryDefinitionFromSectionConfig(config).sort;
  if (field === "added_at" && order === "desc") return "Newest added first";
  const phrase = SORT_PHRASES[`${field}:${order}`];
  return phrase ? capitalize(phrase) : "Custom order";
}

function describeRules(config: Record<string, unknown>): string {
  const query = queryDefinitionFromSectionConfig(config);
  const noun = SCOPE_NOUNS[query.media_scope ?? ""] ?? "titles";
  const rules = query.groups.reduce((count, group) => count + group.rules.length, 0);
  if (rules > 0) return `${capitalize(noun)} matching ${plural(rules, "rule")}`;
  const { field, order } = query.sort;
  if (field === "added_at" && order === "desc") return `All ${noun}`;
  const phrase = SORT_PHRASES[`${field}:${order}`];
  return phrase ? `All ${noun}, ${phrase}` : `${capitalize(noun)} in a chosen order`;
}

function scopeNoun(config: Record<string, unknown>): string | undefined {
  return SCOPE_NOUNS[queryDefinitionFromSectionConfig(config).media_scope ?? ""];
}

function describeSpotlight(config: Record<string, unknown>): string {
  const subjectType = str(config.subject_type);
  const subject = str(config.subject);
  if (subjectType === "era") return subject ? `Titles from the ${subject}` : "Titles from one era";
  if (subjectType === "director" || subjectType === "actor" || subjectType === "studio") {
    if (config.auto_rotate === false && subject) return `Titles from ${subject}`;
    return `A different ${subjectType} every ${cadence(config)}`;
  }
  return "A changing spotlight";
}

function describeFormat(config: Record<string, unknown>): string {
  const format = FORMAT_NAMES[str(config.format) ?? ""];
  if (!format) return "Your best-looking titles";
  return config.sort === "recent" ? `The latest ${format} additions` : `Titles in ${format}`;
}

/** Personalized rows as a profile reads them about itself, keyed by kind. */
const PROFILE_SENTENCES: Record<string, string> = {
  continue_watching: "What you're partway through",
  next_up: "The next episode of every show you follow",
  next_in_series: "The next audiobook in each series you finished",
  watchlist: "What you saved to watch",
  favorites: "Your favorites",
  returning_shows: "Shows you watched that have a new season",
  recommended_for_you: "Picked from your watch history",
  because_you_watched: "More like what you watched last",
  taste_match: "The best matches for your taste today",
};

function profileSentence(row: HomeRow): string | undefined {
  if (row.sectionType === "continue_watching") {
    if (row.config.continue_type === "listening") return "Audiobooks you're partway through";
    if (row.config.continue_type === "reading") return "Books you're partway through";
  }
  return PROFILE_SENTENCES[row.sectionType];
}

/**
 * One plain sentence saying what a row shows. Personalized rows say "each
 * viewer" on the admin surface, because its preview shows the admin's own
 * picks, not everyone's; a profile reads them about itself. A row the profile
 * renamed says what it was called instead.
 */
export function describeRow(row: HomeRow, context: DescribeContext): DescriptionPart[] {
  if (row.renamedFrom) return ["Renamed from ", { strong: row.renamedFrom }];
  const own = context.surface === "profile" ? profileSentence(row) : undefined;
  if (own) return [own];
  const { config } = row;
  const onHome = context.pageKind === "home";
  switch (row.sectionType) {
    case "continue_watching":
      if (config.continue_type === "listening")
        return ["Audiobooks each viewer is partway through"];
      if (config.continue_type === "reading") return ["Books each viewer is partway through"];
      return ["What each viewer is partway through"];
    case "next_up":
      return ["The next episode of every show each viewer follows"];
    case "next_in_series":
      return ["The next audiobook in each series a viewer finished"];
    case "watchlist":
      return ["What each viewer saved to watch"];
    case "favorites":
      return ["Each viewer's favorites"];
    case "recently_added": {
      if (!onHome) {
        const noun = scopeNoun(config);
        return [noun ? `Newest ${noun} in this library` : "Newest additions to this library"];
      }
      const libraries = queryDefinitionFromSectionConfig(config).library_ids.length;
      if (libraries === 0) return ["Newest movies and episodes from all libraries"];
      return [`Newest additions from ${libraries === 1 ? "1 library" : `${libraries} libraries`}`];
    }
    case "recently_released":
      return ["Newest by release date"];
    case "new_to_library":
      return [`Added in the last ${num(config.lookback_days) ?? 30} days`];
    case "returning_shows":
      return ["Shows each viewer watched that have a new season"];
    case "trending_on_server": {
      const window = SERVER_WINDOWS[str(config.window) ?? ""];
      const where = onHome ? "on this server" : "here";
      return [
        window ? `Most played ${where} in the last ${window}` : `Most played ${where} lately`,
      ];
    }
    case "most_watched":
      return [`Watched most often here this ${config.window === "month" ? "month" : "week"}`];
    case "trending_discover":
      return [
        `Trending on TMDB ${config.window === "day" ? "today" : "this week"}, from what you have`,
      ];
    case "profile_activity_feed":
      return ["What other profiles just watched"];
    case "recommended_for_you":
      return ["Picked for each viewer from their own watch history"];
    case "because_you_watched":
      return ["More like what each viewer watched last"];
    case "similar_users_liked":
      return ["What profiles with similar taste enjoyed"];
    case "taste_match":
      return ["The best matches for each viewer's taste today"];
    case "mood_collection":
      return ["Mood pick"];
    case "seasonal_themed": {
      const themes = config.enabled_themes;
      const family =
        (Array.isArray(themes) && themes.length === 1 && themes[0] === "family_movie_night") ||
        (themes === undefined && config.theme === "family_movie_night");
      return [family ? "Family picks on Friday and Saturday evenings" : "Changes with the season"];
    }
    case "editorial_spotlight":
      return [describeSpotlight(config)];
    case "format_showcase":
      return [describeFormat(config)];
    case "hidden_gems": {
      const plays = num(config.max_play_count) ?? 0;
      const times = plays === 1 ? "once" : plays === 2 ? "twice" : `${plays} times`;
      const watched = plays > 0 ? `watched ${times} or less` : "never watched";
      return [
        `Rated ${rating(config.min_rating, 7.5)}+ on TMDB with ${MIN_VOTES}+ votes, and ${watched}`,
      ];
    }
    case "critically_acclaimed":
      return [`Rated ${rating(config.min_score, 8)}+ on TMDB with ${ACCLAIMED_MIN_VOTES}+ votes`];
    case "forgotten_favorites": {
      const days = positive(config.lookback_days, 365);
      return [
        `Rated 7.0+ on TMDB with ${MIN_VOTES}+ votes, and not watched in the past ${days === 365 ? "year" : plural(days, "day")}`,
      ];
    }
    case "genre_roulette":
      return [
        `A different genre every ${cadence(config)}, rated ${rating(config.min_rating, 6)}+ on TMDB with ${MIN_VOTES}+ votes`,
      ];
    case "random":
      return ["A random mix"];
    case "short_watches":
      return [
        `Movies of ${positive(config.max_minutes, 95)} minutes or less, rated ${rating(config.min_rating, 6)}+ on TMDB with ${MIN_VOTES}+ votes`,
      ];
    case "anniversaries":
      return ["Titles marking a release anniversary this month"];
    case "collection":
      return describeCollection(config, context);
    case "custom_filter":
      return [describeRules(config)];
    case "genre":
      return ["Titles from chosen genres"];
    case "admin_curated_list":
      return ["Hand-picked titles"];
    case "award_winners":
      return ["Award winners (no longer offered)"];
    default:
      return [rowKindLabel(row.sectionType)];
  }
}

export function titleCount(itemLimit: number): string {
  return plural(itemLimit, "title");
}

/** "Home" or "Movies page", after "my" or "your": profile copy names the page the row is on. */
export function profilePageName(pageLabel: string): string {
  return pageLabel === "Home" ? "Home" : `${pageLabel} page`;
}

/** The switch's accessible name says what it does, not just "Enabled". */
export function rowSwitchLabel(surface: Surface, row: HomeRow, pageLabel: string): string {
  if (surface === "profile") return `Show ${row.title} on my ${profilePageName(pageLabel)}`;
  return `${row.title} is ${row.shown ? "on" : "off"} for everyone`;
}

/** The line after the bold title on a collapsed (off or hidden) row. */
export function collapsedRowText(surface: Surface, pageLabel: string): string {
  return surface === "profile"
    ? ` is hidden on your ${profilePageName(pageLabel)}.`
    : " is off. Nobody sees this row.";
}
