/**
 * The Discover page editor's model: the layout's rows, how each reads in the
 * row list, and the rules a row must meet before it is sent. The rules mirror
 * internal/requests/discoverrows (NormalizeLayout); the server has the final
 * say and answers a refused row with field errors.
 */
import {
  Clapperboard,
  Flame,
  Link2,
  ListFilter,
  ListVideo,
  Shapes,
  Tv,
  type LucideIcon,
} from "lucide-react";
import type { RequestMediaType } from "@/api/types";
import type {
  DiscoverChart,
  DiscoverFilter,
  DiscoverLayout,
  DiscoverRow,
  DiscoverRowBody,
  DiscoverRowType,
} from "@/api/v2/adminRequests";
import { shownLink } from "@/lib/collections/synced";
import { chartName, chartSummary, type TMDBChart } from "@/lib/collections/tmdbSources";
import { formatRelativeTime } from "@/lib/date";
import type { RowGroup } from "@/lib/homeRows/catalog";
import type { DescriptionPart } from "@/lib/homeRows/describe";
import type { HomeRow } from "@/lib/homeRows/types";

/**
 * The six rows the page had before it could be changed. Shipped apps merge
 * them by ID, so each keeps its chart; its title, size and switch can change.
 */
export const BUILT_IN_ROWS: readonly DiscoverRowBody[] = [
  builtIn("trending_movies", "Trending Movies", {
    preset: "trending",
    media_type: "movie",
    time_window: "week",
  }),
  builtIn("trending_series", "Trending Series", {
    preset: "trending",
    media_type: "series",
    time_window: "week",
  }),
  builtIn("popular_movies", "Popular Movies", { preset: "popular", media_type: "movie" }),
  builtIn("popular_series", "Popular Series", { preset: "popular", media_type: "series" }),
  builtIn("upcoming_movies", "Upcoming Movies", { preset: "upcoming", media_type: "movie" }),
  builtIn("on_air_series", "On Air Series", { preset: "on_the_air", media_type: "series" }),
];

export const BRAND_ROWS: readonly DiscoverRowBody[] = [
  { id: "studios", type: "studios", title: "Studios", enabled: true },
  { id: "networks", type: "networks", title: "Networks", enabled: true },
  { id: "genres", type: "genres", title: "Genres", enabled: true },
];

function builtIn(id: string, title: string, chart: DiscoverChart): DiscoverRowBody {
  return { id, type: "tmdb_chart", title, enabled: true, limit: 20, chart };
}

/** Each row type as the picker, the row list and the dialog show it. */
export const ROW_KINDS: Readonly<
  Record<DiscoverRowType, { label: string; sentence: string; icon: LucideIcon; group: RowGroup }>
> = {
  tmdb_chart: {
    label: "TMDB chart",
    sentence: "Trending, popular, top rated or upcoming titles, as TMDB ranks them.",
    icon: Flame,
    group: "popular",
  },
  tmdb_discover: {
    label: "TMDB filter",
    sentence: "Titles that match genres, keywords, studios, streaming services, years or scores.",
    icon: ListFilter,
    group: "picked",
  },
  mdblist: {
    label: "MDBList list",
    sentence: "A public list from mdblist.com, in list order.",
    icon: ListVideo,
    group: "new",
  },
  tmdb_list: {
    label: "TMDB list",
    sentence: "A public list from themoviedb.org, in list order.",
    icon: Link2,
    group: "new",
  },
  studios: {
    label: "Studios",
    sentence: "Studio cards, each opening the studio's titles.",
    icon: Clapperboard,
    group: "moods",
  },
  networks: {
    label: "Networks",
    sentence: "Network cards, each opening the network's series.",
    icon: Tv,
    group: "moods",
  },
  genres: {
    label: "Genres",
    sentence: "Genre cards, each opening the genre's titles.",
    icon: Shapes,
    group: "moods",
  },
};

/** The types Add row starts a new row from; brand rows come back from "Bring back". */
export const NEW_ROW_TYPES: readonly DiscoverRowType[] = [
  "tmdb_chart",
  "tmdb_discover",
  "mdblist",
  "tmdb_list",
];

export const DEFAULT_ROW_LIMIT = 20;
export const MAX_ROW_LIMIT = 100;

export function isBrandRow(row: Pick<DiscoverRowBody, "type">): boolean {
  return row.type === "studios" || row.type === "networks" || row.type === "genres";
}

export function isBuiltInRow(row: Pick<DiscoverRowBody, "id">): boolean {
  return BUILT_IN_ROWS.some((builtInRow) => builtInRow.id === row.id);
}

/** A stored row as the server takes it back. */
export function rowBody({ source: _source, ...body }: DiscoverRow): DiscoverRowBody {
  return body;
}

/** The layout's rows as the server takes them back. */
export function layoutBodies(layout: DiscoverLayout): DiscoverRowBody[] {
  return layout.rows.map(rowBody);
}

/** A stored row as the shared row list shows it. Brand rows hold no titles. */
export function toHomeRow(row: DiscoverRow): HomeRow {
  return {
    id: row.id ?? "",
    title: row.title,
    sectionType: row.type,
    config: {},
    itemLimit: isBrandRow(row) ? 0 : (row.limit ?? DEFAULT_ROW_LIMIT),
    hero: false,
    shown: row.enabled,
    own: false,
  };
}

/** `rows` with `row` in place of the stored row of its id, or at the end when it has none. */
export function withRow(rows: readonly DiscoverRowBody[], row: DiscoverRowBody): DiscoverRowBody[] {
  const index = row.id ? rows.findIndex((entry) => entry.id === row.id) : -1;
  if (index === -1) return [...rows, row];
  return rows.map((entry, i) => (i === index ? row : entry));
}

/** A new row of a type, with defaults the server accepts once it has a name. */
export function newRow(type: DiscoverRowType): DiscoverRowBody {
  const base = { type, title: "", enabled: true, limit: DEFAULT_ROW_LIMIT };
  switch (type) {
    case "tmdb_chart":
      return { ...base, chart: { preset: "trending", media_type: "movie", time_window: "week" } };
    case "tmdb_discover":
      return { ...base, discover: { media_type: "movie", sort_by: "popularity.desc" } };
    case "tmdb_list":
    case "mdblist":
      return { ...base, list: { url: "" } };
    default:
      return { ...BRAND_ROWS.find((row) => row.type === type)! };
  }
}

/** The built-in and brand rows the layout no longer has, which Add row can bring back. */
export function missingStockRows(rows: readonly Pick<DiscoverRowBody, "id">[]): DiscoverRowBody[] {
  const ids = new Set(rows.map((row) => row.id));
  return [...BUILT_IN_ROWS, ...BRAND_ROWS].filter((row) => !ids.has(row.id));
}

// The collection charts name series "tv"; the request API says series.
export function toTMDBChart(chart: DiscoverChart): TMDBChart {
  return {
    preset: chart.preset as TMDBChart["preset"],
    mediaType: chart.media_type === "series" ? "tv" : chart.media_type,
    timeWindow: chart.time_window,
  };
}

export function fromTMDBChart(chart: TMDBChart): DiscoverChart {
  return {
    preset: chart.preset,
    media_type: chart.mediaType === "tv" ? "series" : chart.mediaType,
    ...(chart.timeWindow ? { time_window: chart.timeWindow } : {}),
  };
}

/** The name a row gets when none is typed; empty when it needs one. */
export function defaultTitle(row: DiscoverRowBody): string {
  if (row.chart) return chartName(toTMDBChart(row.chart));
  if (isBrandRow(row)) return ROW_KINDS[row.type].label;
  return "";
}

/** The name the row is saved with. */
export function savedTitle(row: DiscoverRowBody): string {
  return row.title.trim() || defaultTitle(row);
}

const FILTER_SORTS: ReadonlyArray<{ value: string; label: string; media?: RequestMediaType }> = [
  { value: "popularity.desc", label: "Most popular" },
  { value: "vote_average.desc", label: "Highest rated" },
  { value: "vote_count.desc", label: "Most voted" },
  { value: "primary_release_date.desc", label: "Newest", media: "movie" },
  { value: "primary_release_date.asc", label: "Oldest", media: "movie" },
  { value: "first_air_date.desc", label: "Newest", media: "series" },
  { value: "first_air_date.asc", label: "Oldest", media: "series" },
  { value: "revenue.desc", label: "Highest grossing", media: "movie" },
];

/** The sorts a filter row offers for a media type. */
export function sortsFor(mediaType: RequestMediaType) {
  return FILTER_SORTS.filter((sort) => !sort.media || sort.media === mediaType);
}

/** A sort that fits the other media type, for when a filter switches between them. */
export function sortForMediaType(sortBy: string, mediaType: RequestMediaType): string {
  const [field, direction] = sortBy.split(".");
  if (mediaType === "series" && field === "primary_release_date")
    return `first_air_date.${direction}`;
  if (mediaType === "movie" && field === "first_air_date")
    return `primary_release_date.${direction}`;
  if (mediaType === "series" && field === "revenue") return "popularity.desc";
  return sortBy;
}

export const RATING_CEILINGS: Readonly<Record<RequestMediaType, readonly string[]>> = {
  movie: ["G", "PG", "PG-13", "R", "NC-17"],
  series: ["TV-Y", "TV-Y7", "TV-G", "TV-PG", "TV-14", "TV-MA"],
};

/** "Movies, most popular first, 2 genres, 1990–1999": what a filter row looks for. */
export function filterSummary(filter: DiscoverFilter): string {
  const parts = [filter.media_type === "series" ? "Series" : "Movies"];
  const sort = sortsFor(filter.media_type).find((s) => s.value === filter.sort_by);
  if (sort) parts.push(`${sort.label.toLowerCase()} first`);
  const counts: Array<[number | undefined, string]> = [
    [filter.genre_ids?.length, "genre"],
    [filter.keyword_ids?.length, "keyword"],
    [filter.company_ids?.length, "studio"],
    [filter.network_ids?.length, "network"],
    [filter.watch_provider_ids?.length, "streaming service"],
  ];
  for (const [count, noun] of counts) {
    if (count) parts.push(`${count} ${noun}${count === 1 ? "" : "s"}`);
  }
  if (filter.year_from || filter.year_to) {
    parts.push(`${filter.year_from ?? "…"}–${filter.year_to ?? "…"}`);
  }
  if (filter.certification_lte) parts.push(`up to ${filter.certification_lte}`);
  return parts.join(", ");
}

/** How many of the filters under More options a filter sets, for its summary line. */
export function moreFilterCount(filter: DiscoverFilter): number {
  return [
    filter.original_language,
    filter.origin_countries?.length,
    filter.year_from ?? filter.year_to,
    filter.vote_average_gte ?? filter.vote_count_gte,
    filter.runtime_gte ?? filter.runtime_lte,
    filter.certification_lte,
  ].filter(Boolean).length;
}

/** How a list row's stored copy stands, after what it shows. */
function listState(row: DiscoverRow): DescriptionPart[] {
  const source = row.source;
  if (!source) return [];
  switch (source.state) {
    case "pending":
      // Only rows that are on get fetched.
      return [row.enabled ? " · Fetching the list…" : " · Fetched once the row is on"];
    case "ready":
      return [
        ` · ${source.entries} in the list${source.refreshed_at ? `, updated ${formatRelativeTime(source.refreshed_at)}` : ""}`,
      ];
    case "empty":
      return [" · ", { warning: "The list has no movies or series" }];
    default:
      return [
        " · ",
        {
          warning: `The last fetch failed${source.error ? `: ${source.error}` : ""}${source.entries ? `; showing the ${source.entries} titles from before` : ""}`,
        },
      ];
  }
}

/** What a row shows, in the line under its name in the row list. */
export function describeDiscoverRow(row: DiscoverRow): DescriptionPart[] {
  const parts: DescriptionPart[] = [];
  if (row.chart) parts.push(`TMDB chart: ${chartSummary(toTMDBChart(row.chart))}`);
  else if (row.discover) parts.push(`TMDB filter: ${filterSummary(row.discover)}`);
  else if (row.list)
    parts.push(
      `${ROW_KINDS[row.type].label}: `,
      { strong: shownLink(row.list.url) },
      ...listState(row),
    );
  else parts.push(ROW_KINDS[row.type].sentence.replace(/\.$/, ""));
  if (row.hide_available) parts.push(" · Missing titles only");
  return parts;
}

/** The server's field errors for one row, worded for the dialog. */
export function rowErrors(fields: Readonly<Record<string, string>>, index: number): string[] {
  const prefix = `rows[${index}]`;
  return Object.entries(fields)
    .filter(([path]) => path === prefix || path.startsWith(`${prefix}.`))
    .map(([path, detail]) => {
      const field = path.slice(prefix.length + 1);
      return field ? `${field.replaceAll("_", " ")}: ${detail}` : detail;
    });
}

/** Why a row can't be saved as it stands, or null: what the editor checks before the server does. */
export function rowProblem(row: DiscoverRowBody): string | null {
  if (row.list && !row.list.url.trim()) return "Pick a list or paste its link.";
  if (row.discover?.watch_provider_ids?.length && !row.discover.watch_region?.trim()) {
    return "Choose the region the streaming services are in.";
  }
  if (!savedTitle(row)) return "Give the row a name.";
  return null;
}
