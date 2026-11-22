import { codeNumber, t } from "@/i18n";

import { formatDate, parseCalendarDate } from "@/lib/datetime";

interface EpisodeCardLabelItem {
  type?: string;
  title?: string;
  series_title?: string;
  season_number?: number | null;
  episode_number?: number | null;
  /** The series' episode labels: "air_date" when its numbers are synthetic. */
  episode_labels?: string;
  air_date?: string | null;
}

export interface EpisodeCardLabels {
  seriesTitle: string;
  episodeTitle?: string;
  episodeCode: string;
}

/** "S01E02", zero-padded like the Apple and Android episode cards. */
export function formatEpisodeCode(seasonNumber: number, episodeNumber: number): string {
  return t("web.detail.episodeCode", {
    season: codeNumber(seasonNumber, 2),
    episode: codeNumber(episodeNumber, 2),
  });
}

/**
 * Whether a series labels its episodes by air date instead of season and
 * episode numbers: a sports league Sportarr numbers S2026E296, for one.
 */
export function labelsEpisodesByAirDate(episodeLabels: string | undefined): boolean {
  return episodeLabels === "air_date";
}

/**
 * The separator between an air date and a title. The space after the bullet
 * doesn't break, so it survives before a title rendered as its own box.
 */
export const AIR_DATE_TITLE_SEPARATOR = " •\u00a0";

/**
 * The air date ("Nov 20, 2026") that labels an episode of a date-labeled
 * series, or null when the series uses numbers or the episode is undated.
 */
export function episodeAirDateLabel(
  episodeLabels: string | undefined,
  airDate: string | null | undefined,
): string | null {
  if (!labelsEpisodesByAirDate(episodeLabels) || !airDate) return null;
  return formatDate(airDate, "medium") || null;
}

/**
 * An episode's label: "S01E02", or the air date ("Nov 20, 2026") when the
 * series is labeled by date. An undated episode keeps its code.
 */
export function formatEpisodeLabel(item: {
  season_number: number;
  episode_number: number;
  episode_labels?: string;
  air_date?: string | null;
}): string {
  return (
    episodeAirDateLabel(item.episode_labels, item.air_date) ??
    formatEpisodeCode(item.season_number, item.episode_number)
  );
}

/** "Nov 20, 2026 • Chicago Bulls vs Washington Wizards", or the date alone. */
export function airDateWithTitle(airDateLabel: string, title?: string | null): string {
  return title ? `${airDateLabel}${AIR_DATE_TITLE_SEPARATOR}${title}` : airDateLabel;
}

/**
 * A short episode subtitle for pickers and sheets: "S1 E2", or the air date
 * ("Nov 20, 2026") when the series is labeled by date. Watch Together sends it
 * to the party, so it is data and stays English in every app language.
 */
export function episodeSubtitle(
  episodeLabels: string | undefined,
  airDate: string | null | undefined,
  seasonNumber: number,
  episodeNumber: number,
): string {
  const date = labelsEpisodesByAirDate(episodeLabels) ? parseCalendarDate(airDate) : null;
  return date
    ? date.toLocaleDateString("en-US", { month: "short", day: "numeric", year: "numeric" })
    : `S${seasonNumber} E${episodeNumber}`;
}

export function buildEpisodeCardLabels(item: EpisodeCardLabelItem): EpisodeCardLabels | null {
  if (
    item.type !== "episode" ||
    item.season_number == null ||
    item.episode_number == null ||
    !item.title
  ) {
    return null;
  }

  const hasSeriesTitle = Boolean(item.series_title && item.series_title !== item.title);

  return {
    seriesTitle: hasSeriesTitle ? item.series_title! : item.title,
    episodeTitle: hasSeriesTitle ? item.title : undefined,
    episodeCode: formatEpisodeLabel({
      season_number: item.season_number,
      episode_number: item.episode_number,
      episode_labels: item.episode_labels,
      air_date: item.air_date,
    }),
  };
}
