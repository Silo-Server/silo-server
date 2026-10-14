/**
 * Starter pack answers shaped like `/api/v2`: the bundle list with its
 * template summaries, and dry-run results for the Core pack.
 */
import type { components } from "@/api/v2/schema";
import type { Library } from "@/api/types";

type Summary = components["schemas"]["CollectionTemplateSummary"];
type Result = components["schemas"]["AdminTemplateResult"];
type Entry = components["schemas"]["AdminTemplateEntry"];

function summary(
  id: string,
  title: string,
  media_kind: "movie" | "tv" | "mixed",
  extra: Partial<Summary> = {},
): Summary {
  return { id, title, source: "tmdb", media_kind, featured: false, needs_setup: false, ...extra };
}

const core = [
  summary("tmdb_trending_movies_week", "Trending Movies This Week", "movie", { featured: true }),
  summary("tmdb_popular_movies", "Popular Movies", "movie"),
  summary("tmdb_top_rated_movies", "Top Rated Movies", "movie"),
  summary("tmdb_trending_tv_week", "Trending TV This Week", "tv", { featured: true }),
  summary("tmdb_popular_tv", "Popular TV", "tv"),
];

const genres = [
  summary("tmdb_discover_popular_horror_movies", "Popular Horror Movies", "movie", {
    source: "tmdb_discover",
  }),
  summary("tmdb_discover_popular_comedy_tv", "Popular Comedy Shows", "tv", {
    source: "tmdb_discover",
  }),
];

const franchises = [
  summary("tmdb_franchise_star_wars", "Star Wars", "movie", { source: "tmdb_collection" }),
  summary("tmdb_franchise_placeholder", "TMDB Franchise", "movie", {
    source: "tmdb_collection",
    needs_setup: true,
  }),
];

function bundle(id: string, title: string, description: string, templates: Summary[]) {
  return { id, title, description, template_ids: templates.map((entry) => entry.id), templates };
}

/** `GET /api/v2/admin/collections/template-bundles`, Everything listed first as the server does. */
export const starterPackBundles = {
  bundles: [
    bundle("all_defaults", "All Defaults", "Every starter list.", [
      ...core,
      ...genres,
      ...franchises,
    ]),
    bundle("core_defaults", "Core Defaults", "Trending, popular and top-rated lists.", core),
    bundle("popular_genres", "Popular Genres", "Popular titles by genre.", genres),
    bundle("franchise_collections", "Franchise Collections", "Movie sagas.", franchises),
  ],
};

export const starterPackLibraries = [
  { id: 1, name: "Movies", type: "movies", enabled: true },
  { id: 2, name: "TV Shows", type: "series", enabled: true },
  { id: 3, name: "Audiobooks", type: "audiobooks", enabled: true },
] as Library[];

function entry(templateId: string, libraryId: 1 | 2, reason: string): Entry {
  const template = [...core, ...genres, ...franchises].find((item) => item.id === templateId)!;
  return {
    template_id: templateId,
    template_title: template.title,
    library_id: String(libraryId),
    library_name: libraryId === 1 ? "Movies" : "TV Shows",
    reason,
  };
}

function result(partial: Partial<Result>): Result {
  return {
    bundle_id: "core_defaults",
    dry_run: true,
    delete_existing: false,
    deleted: [],
    delete_skipped: [],
    delete_failed: [],
    created: [],
    skipped: [],
    failed: [],
    sync_queued: [],
    featured: [],
    featured_failed: [],
    ...partial,
  };
}

/**
 * The Core pack's dry run for Movies and TV Shows: Movies gets two new lists
 * (one pinned first) and already has Top Rated Movies; TV Shows gets both TV
 * lists; each library leaves out the other kind's lists.
 */
export const coreDryRun = result({
  created: [
    entry("tmdb_trending_movies_week", 1, "would_create"),
    entry("tmdb_popular_movies", 1, "would_create"),
    entry("tmdb_trending_tv_week", 2, "would_create"),
    entry("tmdb_popular_tv", 2, "would_create"),
  ],
  skipped: [
    { ...entry("tmdb_top_rated_movies", 1, "already_exists"), collection_id: "c9" },
    entry("tmdb_trending_tv_week", 1, "ineligible_library"),
    entry("tmdb_popular_tv", 1, "ineligible_library"),
    entry("tmdb_trending_movies_week", 2, "ineligible_library"),
    entry("tmdb_popular_movies", 2, "ineligible_library"),
    entry("tmdb_top_rated_movies", 2, "ineligible_library"),
  ],
});

/** The same dry run with the default hero banners asked for. */
export const coreDryRunWithHeroes = result({
  ...coreDryRun,
  featured: [
    {
      surface: "home",
      library_id: "1",
      library_name: "Movies",
      template_id: "tmdb_trending_movies_week",
      template_title: "Trending Movies This Week",
      reason: "would_create",
    },
    {
      surface: "library",
      library_id: "1",
      library_name: "Movies",
      template_id: "tmdb_trending_movies_week",
      template_title: "Trending Movies This Week",
      reason: "would_create",
    },
    {
      surface: "library",
      library_id: "2",
      library_name: "TV Shows",
      template_id: "tmdb_trending_tv_week",
      template_title: "Trending TV This Week",
      reason: "would_create",
    },
  ],
});

/** A dry run after the Core pack was added to Movies: every eligible list is there. */
export const coreDryRunAllThere = result({
  skipped: [
    { ...entry("tmdb_trending_movies_week", 1, "already_exists"), collection_id: "c1" },
    { ...entry("tmdb_popular_movies", 1, "already_exists"), collection_id: "c2" },
    { ...entry("tmdb_top_rated_movies", 1, "already_exists"), collection_id: "c9" },
    entry("tmdb_trending_tv_week", 1, "ineligible_library"),
    entry("tmdb_popular_tv", 1, "ineligible_library"),
  ],
});

/** The finished apply job for the Core pack on Movies and TV Shows. */
export const coreApplied = result({
  dry_run: false,
  created: [
    { ...entry("tmdb_trending_movies_week", 1, ""), collection_id: "c1" },
    { ...entry("tmdb_popular_movies", 1, ""), collection_id: "c2" },
    { ...entry("tmdb_trending_tv_week", 2, ""), collection_id: "c3" },
  ],
  failed: [entry("tmdb_popular_tv", 2, "operation_failed")],
  skipped: coreDryRun.skipped,
});

/** A finished apply job where every new list failed, so nothing was added. */
export const coreAppliedAllFailed = result({
  dry_run: false,
  failed: [
    entry("tmdb_trending_movies_week", 1, "operation_failed"),
    entry("tmdb_popular_movies", 1, "operation_failed"),
  ],
  skipped: coreDryRun.skipped,
});
