/**
 * TMDB charts a Synced list can follow, and the choices TMDB accepts for each.
 * Mirrors `validateTMDB` in internal/collections/templates/validate.go, so the
 * editor never offers a combination the server refuses.
 */
import type { ImportTMDBCollectionRequest } from "@/api/types";
import { parseTMDBListID } from "@/lib/tmdbList";

export type TMDBChartPreset = ImportTMDBCollectionRequest["preset"];
export type TMDBChartMediaType = ImportTMDBCollectionRequest["media_type"];
export type TMDBChartTimeWindow = NonNullable<ImportTMDBCollectionRequest["time_window"]>;

export interface TMDBChart {
  preset: TMDBChartPreset;
  mediaType: TMDBChartMediaType;
  /** Trending only. */
  timeWindow?: TMDBChartTimeWindow;
}

export const TMDB_CHARTS: ReadonlyArray<{ preset: TMDBChartPreset; label: string; hint: string }> =
  [
    { preset: "trending", label: "Trending", hint: "What's hot now" },
    { preset: "popular", label: "Popular", hint: "Most viewed" },
    { preset: "top_rated", label: "Top rated", hint: "Best scores" },
    { preset: "now_playing", label: "Now playing", hint: "Movies in cinemas" },
    { preset: "upcoming", label: "Upcoming", hint: "Movies coming soon" },
    { preset: "airing_today", label: "Airing today", hint: "TV, today" },
    { preset: "on_the_air", label: "On the air", hint: "TV, this week" },
  ];

export const CHART_MEDIA_LABEL: Readonly<Record<TMDBChartMediaType, string>> = {
  movie: "Movies",
  tv: "TV shows",
  all: "Both",
};

export const CHART_WINDOW_LABEL: Readonly<Record<TMDBChartTimeWindow, string>> = {
  day: "Today",
  week: "This week",
};

/** What Show offers for a chart; Both only for Trending. */
export function chartMediaTypes(preset: TMDBChartPreset): TMDBChartMediaType[] {
  switch (preset) {
    case "trending":
      return ["movie", "tv", "all"];
    case "popular":
    case "top_rated":
      return ["movie", "tv"];
    case "now_playing":
    case "upcoming":
      return ["movie"];
    case "airing_today":
    case "on_the_air":
      return ["tv"];
  }
}

/** "Trending over" applies to Trending only. */
export function chartHasTimeWindow(preset: TMDBChartPreset): boolean {
  return preset === "trending";
}

/** Why Show is fixed for a chart TMDB has for one kind only; null when it isn't. */
export function chartLockReason(preset: TMDBChartPreset): string | null {
  const [only, ...more] = chartMediaTypes(preset);
  if (more.length > 0) return null;
  return `TMDB has this chart for ${only === "movie" ? "movies" : "TV shows"} only`;
}

/**
 * The chart with choices TMDB accepts: a Show it doesn't offer falls back to
 * the first it does, and "Trending over" is kept for Trending only.
 */
export function normalizeChart(chart: TMDBChart): TMDBChart {
  const allowed = chartMediaTypes(chart.preset);
  const mediaType = allowed.includes(chart.mediaType) ? chart.mediaType : allowed[0]!;
  if (!chartHasTimeWindow(chart.preset)) return { preset: chart.preset, mediaType };
  return { preset: chart.preset, mediaType, timeWindow: chart.timeWindow ?? "day" };
}

/** A chart picked without choices yet: Trending starts on Both, today. */
export function defaultChart(preset: TMDBChartPreset): TMDBChart {
  return normalizeChart({ preset, mediaType: "all" });
}

export function sameChart(a: TMDBChart, b: TMDBChart): boolean {
  const left = normalizeChart(a);
  const right = normalizeChart(b);
  return (
    left.preset === right.preset &&
    left.mediaType === right.mediaType &&
    left.timeWindow === right.timeWindow
  );
}

/** The name a chart gets when no ready-made pick matches it: "Trending Movies Today". */
export function chartName(chart: TMDBChart): string {
  const { preset, mediaType, timeWindow } = normalizeChart(chart);
  const label = TMDB_CHARTS.find((entry) => entry.preset === preset)!.label;
  const title = label.replace(/\b\w/g, (letter) => letter.toUpperCase());
  const media = { movie: "Movies", tv: "TV Shows", all: "" }[mediaType];
  const when = timeWindow === "week" ? "This Week" : timeWindow === "day" ? "Today" : "";
  return [title, media, when].filter(Boolean).join(" ");
}

/** The kind of titles a chart holds, for which libraries it can match into. */
export function chartMediaKind(chart: TMDBChart): "movie" | "tv" | "mixed" {
  const { mediaType } = normalizeChart(chart);
  return mediaType === "all" ? "mixed" : mediaType;
}

// --- Stored sources -----------------------------------------------------------

/** What the PATCH of a saved synced list sends for the list it follows. */
export interface StoredSource {
  source_url: string;
  source_config: Record<string, unknown>;
}

/** `config` with `limit` set, or as it is when there is none. */
export function withLimit(config: Record<string, unknown>, limit: number | undefined) {
  return limit === undefined ? config : { ...config, limit };
}

function isChartPreset(value: unknown): value is TMDBChartPreset {
  return TMDB_CHARTS.some((entry) => entry.preset === value);
}

/** The chart a stored `tmdb_preset` config names; one naming no known chart reads as Trending. */
export function chartOfSourceConfig(config: Record<string, unknown> | undefined): TMDBChart {
  const preset = config?.preset;
  if (!isChartPreset(preset)) return defaultChart("trending");
  const mediaType = config?.media_type;
  const timeWindow = config?.time_window;
  return normalizeChart({
    preset,
    mediaType:
      mediaType === "movie" || mediaType === "tv" || mediaType === "all" ? mediaType : "all",
    timeWindow: timeWindow === "day" || timeWindow === "week" ? timeWindow : undefined,
  });
}

/** A chart as the server stores it: `tmdb://preset/media[/window]`. */
export function chartSource(chart: TMDBChart, limit?: number): StoredSource {
  const { preset, mediaType, timeWindow } = normalizeChart(chart);
  const config: Record<string, unknown> = { mode: "tmdb_preset", preset, media_type: mediaType };
  if (timeWindow) config.time_window = timeWindow;
  return {
    source_url: `tmdb://${[preset, mediaType, timeWindow].filter(Boolean).join("/")}`,
    source_config: withLimit(config, limit),
  };
}

/** A TMDB list link, stored as the list's canonical URL. */
export function tmdbListSource(link: string, limit?: number): StoredSource {
  const id = parseTMDBListID(link);
  const url = id === null ? link.trim() : `https://www.themoviedb.org/list/${id}`;
  return { source_url: url, source_config: withLimit({ mode: "tmdb_list", url }, limit) };
}

/** A franchise list: the TMDB collection it follows. */
export function franchiseSource(collectionId: number, limit?: number): StoredSource {
  return {
    source_url: `tmdb://collection/${collectionId}`,
    source_config: withLimit({ mode: "tmdb_collection", collection_id: collectionId }, limit),
  };
}

const CHART_MEDIA_WORDS: Readonly<Record<TMDBChartMediaType, string>> = {
  movie: "movies",
  tv: "TV shows",
  all: "movies and TV shows",
};

/** "Trending movies, this week", "Top rated TV shows": what a chart follows, in words. */
export function chartSummary(chart: TMDBChart): string {
  const { preset, mediaType, timeWindow } = normalizeChart(chart);
  const label = TMDB_CHARTS.find((entry) => entry.preset === preset)!.label;
  const when = timeWindow ? `, ${CHART_WINDOW_LABEL[timeWindow].toLowerCase()}` : "";
  return `${label} ${CHART_MEDIA_WORDS[mediaType]}${when}`;
}
