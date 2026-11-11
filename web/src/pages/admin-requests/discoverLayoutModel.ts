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
import { createAppIntl, t, type MessageKey } from "@/i18n";
import { formatWith } from "@/i18n/format";
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
import { sectionTitleKey } from "@/lib/sectionTitle";

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

/**
 * Each row type as the picker, the row list and the dialog show it. `label` and
 * `sentence` are message ids; `rowKindLabel` and `rowKindSentence` give the text.
 */
export const ROW_KINDS: Readonly<
  Record<
    DiscoverRowType,
    { label: MessageKey; sentence: MessageKey; icon: LucideIcon; group: RowGroup }
  >
> = {
  tmdb_chart: {
    label: "web.admin.requests.discover.kind.tmdbChart.label",
    sentence: "web.admin.requests.discover.kind.tmdbChart.sentence",
    icon: Flame,
    group: "popular",
  },
  tmdb_discover: {
    label: "web.admin.requests.discover.kind.tmdbFilter.label",
    sentence: "web.admin.requests.discover.kind.tmdbFilter.sentence",
    icon: ListFilter,
    group: "picked",
  },
  mdblist: {
    label: "web.admin.requests.discover.kind.mdblist.label",
    sentence: "web.admin.requests.discover.kind.mdblist.sentence",
    icon: ListVideo,
    group: "new",
  },
  tmdb_list: {
    label: "web.admin.requests.discover.kind.tmdbList.label",
    sentence: "web.admin.requests.discover.kind.tmdbList.sentence",
    icon: Link2,
    group: "new",
  },
  studios: {
    label: "web.admin.requests.discover.kind.studios.label",
    sentence: "web.admin.requests.discover.kind.studios.sentence",
    icon: Clapperboard,
    group: "moods",
  },
  networks: {
    label: "web.admin.requests.discover.kind.networks.label",
    sentence: "web.admin.requests.discover.kind.networks.sentence",
    icon: Tv,
    group: "moods",
  },
  genres: {
    label: "web.admin.requests.discover.kind.genres.label",
    sentence: "web.admin.requests.discover.kind.genres.sentence",
    icon: Shapes,
    group: "moods",
  },
};

/** A row type's name: "TMDB chart", "Studios". */
export function rowKindLabel(type: DiscoverRowType): string {
  return t(ROW_KINDS[type].label);
}

let englishIntl: ReturnType<typeof createAppIntl> | undefined;

/** A row type's English name, for requests the server gets: sent text stays English (D44). */
export function rowKindEnglishLabel(type: DiscoverRowType): string {
  englishIntl ??= createAppIntl("en", {});
  return formatWith(englishIntl, ROW_KINDS[type].label);
}

/** A row type in a sentence: what its rows show. */
export function rowKindSentence(type: DiscoverRowType): string {
  return t(ROW_KINDS[type].sentence);
}

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
  // The server's own row names carry a catalog key (see `useHomeRowTitle`); a name an admin typed has none.
  const { title_key, title_args } = sectionTitleKey(row);
  return {
    id: row.id ?? "",
    title: row.title,
    sectionType: row.type,
    config: {},
    itemLimit: isBrandRow(row) ? 0 : (row.limit ?? DEFAULT_ROW_LIMIT),
    hero: false,
    shown: row.enabled,
    own: false,
    titleKey: title_key ? { title: row.title, key: title_key, args: title_args } : undefined,
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

/** The name a row shows when none is typed, in the interface language; empty when it needs one. */
export function defaultTitle(row: DiscoverRowBody): string {
  if (row.chart) return chartName(toTMDBChart(row.chart), t);
  if (isBrandRow(row)) return rowKindLabel(row.type);
  return "";
}

/**
 * The name the row is saved with. It is stored data, so an untyped chart row keeps its
 * English chart name and an untyped brand row the server's English default ("Studios")
 * in every interface language.
 */
export function savedTitle(row: DiscoverRowBody): string {
  const typed = row.title.trim();
  if (typed) return typed;
  if (row.chart) return chartName(toTMDBChart(row.chart));
  if (isBrandRow(row)) return BRAND_ROWS.find((brand) => brand.type === row.type)?.title ?? "";
  return defaultTitle(row);
}

/** A filter row's sorts: the picker's label and the summary's "… first" phrase, as message ids. */
const FILTER_SORTS: ReadonlyArray<{
  value: string;
  label: MessageKey;
  summary: MessageKey;
  media?: RequestMediaType;
}> = [
  {
    value: "popularity.desc",
    label: "web.admin.requests.discover.sort.mostPopular",
    summary: "web.admin.requests.discover.sort.mostPopularFirst",
  },
  {
    value: "vote_average.desc",
    label: "web.admin.requests.discover.sort.highestRated",
    summary: "web.admin.requests.discover.sort.highestRatedFirst",
  },
  {
    value: "vote_count.desc",
    label: "web.admin.requests.discover.sort.mostVoted",
    summary: "web.admin.requests.discover.sort.mostVotedFirst",
  },
  {
    value: "primary_release_date.desc",
    label: "web.admin.requests.discover.sort.newest",
    summary: "web.admin.requests.discover.sort.newestFirst",
    media: "movie",
  },
  {
    value: "primary_release_date.asc",
    label: "web.admin.requests.discover.sort.oldest",
    summary: "web.admin.requests.discover.sort.oldestFirst",
    media: "movie",
  },
  {
    value: "first_air_date.desc",
    label: "web.admin.requests.discover.sort.newest",
    summary: "web.admin.requests.discover.sort.newestFirst",
    media: "series",
  },
  {
    value: "first_air_date.asc",
    label: "web.admin.requests.discover.sort.oldest",
    summary: "web.admin.requests.discover.sort.oldestFirst",
    media: "series",
  },
  {
    value: "revenue.desc",
    label: "web.admin.requests.discover.sort.highestGrossing",
    summary: "web.admin.requests.discover.sort.highestGrossingFirst",
    media: "movie",
  },
];

function sortEntriesFor(mediaType: RequestMediaType) {
  return FILTER_SORTS.filter((sort) => !sort.media || sort.media === mediaType);
}

/** The sorts a filter row offers for a media type, labelled for its picker. */
export function sortsFor(mediaType: RequestMediaType) {
  return sortEntriesFor(mediaType).map((sort) => ({ value: sort.value, label: t(sort.label) }));
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
  const parts = [
    filter.media_type === "series"
      ? t("web.admin.requests.discover.summary.series")
      : t("web.admin.requests.discover.summary.movies"),
  ];
  const sort = sortEntriesFor(filter.media_type).find((s) => s.value === filter.sort_by);
  if (sort) parts.push(t(sort.summary));
  // A filter row holds at most 20 IDs of each kind.
  const counts: Array<[number | undefined, MessageKey]> = [
    [filter.genre_ids?.length, "web.admin.requests.discover.summary.genres"],
    [filter.keyword_ids?.length, "web.admin.requests.discover.summary.keywords"],
    [filter.company_ids?.length, "web.admin.requests.discover.summary.studios"],
    [filter.network_ids?.length, "web.admin.requests.discover.summary.networks"],
    [filter.watch_provider_ids?.length, "web.admin.requests.discover.summary.streamingServices"],
  ];
  for (const [count, key] of counts) {
    if (count) parts.push(t(key, { count }));
  }
  if (filter.year_from || filter.year_to) {
    parts.push(`${filter.year_from ?? "…"}–${filter.year_to ?? "…"}`);
  }
  if (filter.certification_lte) {
    parts.push(
      t("web.admin.requests.discover.summary.ratingLimit", { rating: filter.certification_lte }),
    );
  }
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
      return [
        ` · ${row.enabled ? t("web.admin.requests.discover.list.fetching") : t("web.admin.requests.discover.list.fetchedWhenOn")}`,
      ];
    case "ready": {
      // A stored list holds at most 200 entries.
      const updated = source.refreshed_at ? formatRelativeTime(source.refreshed_at) : "";
      return [
        ` · ${
          updated
            ? t("web.admin.requests.discover.list.entriesUpdated", {
                count: source.entries,
                updated,
              })
            : t("web.admin.requests.discover.list.entries", { count: source.entries })
        }`,
      ];
    }
    case "empty":
      return [" · ", { warning: t("web.admin.requests.discover.list.empty") }];
    default:
      return [" · ", { warning: listFailure(source.error, source.entries) }];
  }
}

/** Why a list row's last fetch failed, and what it shows meanwhile. */
function listFailure(error: string | undefined, entries: number | undefined): string {
  if (error && entries) {
    return t("web.admin.requests.discover.list.failedWithErrorKept", { error, count: entries });
  }
  if (error) return t("web.admin.requests.discover.list.failedWithError", { error });
  if (entries) return t("web.admin.requests.discover.list.failedKept", { count: entries });
  return t("web.admin.requests.discover.list.failed");
}

/** The orders an MDBList row can show its list in; list order is stored absent. */
export const MDBLIST_ORDERS = [
  { value: "list", label: "web.admin.requests.discover.order.list" },
  { value: "newest_added", label: "web.admin.requests.discover.order.newestAdded" },
  { value: "newest_released", label: "web.admin.requests.discover.order.newestReleased" },
] as const satisfies ReadonlyArray<{ value: string; label: MessageKey }>;

export type MDBListOrder = (typeof MDBLIST_ORDERS)[number]["value"];

/** An MDBList row's order, list order when it has none. */
export function mdblistOrder(row: DiscoverRowBody): MDBListOrder {
  return row.list?.order ?? "list";
}

/** A non-default order, as the row list's description line names it. */
function orderPart(order: MDBListOrder): DescriptionPart[] {
  if (order === "list") return [];
  const label = MDBLIST_ORDERS.find((option) => option.value === order)?.label;
  return [` · ${label ? t(label) : ""}`];
}

/** What a row shows, in the line under its name in the row list. */
export function describeDiscoverRow(row: DiscoverRow): DescriptionPart[] {
  const parts: DescriptionPart[] = [];
  if (row.chart) {
    parts.push(
      t("web.admin.requests.discover.describe.chart", {
        summary: chartSummary(toTMDBChart(row.chart)),
      }),
    );
  } else if (row.discover) {
    parts.push(
      t("web.admin.requests.discover.describe.filter", { summary: filterSummary(row.discover) }),
    );
  } else if (row.list)
    parts.push(
      `${t("web.admin.requests.discover.describe.listKind", { kind: rowKindLabel(row.type) })} `,
      { strong: shownLink(row.list.url) },
      ...orderPart(mdblistOrder(row)),
      ...listState(row),
    );
  else parts.push(rowKindSentence(row.type).replace(/\.$/, ""));
  if (row.hide_available) parts.push(` · ${t("web.admin.requests.discover.missingTitlesOnly")}`);
  return parts;
}

/** The server's field errors for one row, worded for the dialog. */
export function rowErrors(fields: Readonly<Record<string, string>>, index: number): string[] {
  const prefix = `rows[${index}]`;
  return Object.entries(fields)
    .filter(([path]) => path === prefix || path.startsWith(`${prefix}.`))
    .map(([path, detail]) => {
      const field = path.slice(prefix.length + 1);
      return field
        ? t("web.admin.requests.discover.fieldError", { field: field.replaceAll("_", " "), detail })
        : detail;
    });
}

/** Why a row can't be saved as it stands, or null: what the editor checks before the server does. */
export function rowProblem(row: DiscoverRowBody): string | null {
  if (row.list && !row.list.url.trim()) return t("web.admin.requests.discover.problem.noList");
  if (row.discover?.watch_provider_ids?.length && !row.discover.watch_region?.trim()) {
    return t("web.admin.requests.discover.problem.noRegion");
  }
  if (!savedTitle(row)) return t("web.admin.requests.discover.problem.noName");
  return null;
}
