import type { EpisodeListItem } from "@/api/types";
import {
  formatEpisodeCode,
  formatEpisodeLabel,
  labelsEpisodesByAirDate,
} from "@/lib/episodeCardLabels";
import { formatDate } from "@/lib/datetime";
import { formatRuntimeMinutes } from "@/lib/mediaFormat";
import { t } from "@/i18n";

type EpisodeIdentity = Pick<
  EpisodeListItem,
  "season_number" | "episode_number" | "title" | "air_date"
>;

/**
 * First caption line, "S01E02 · Pilot", in two parts: the code with its
 * separator, and the title. The code alone when the episode has no title.
 */
export function episodeCardTitleParts(
  episode: EpisodeIdentity,
  episodeLabels?: string,
): {
  prefix: string;
  title?: string;
} {
  const title = episode.title?.trim();
  if (labelsEpisodesByAirDate(episodeLabels)) {
    // The air date is on the line below, so the title leads on its own; an
    // untitled episode shows the date (or its code when undated).
    return title
      ? { prefix: "", title }
      : { prefix: formatEpisodeLabel({ ...episode, episode_labels: episodeLabels }) };
  }
  const code = formatEpisodeCode(episode.season_number, episode.episode_number);
  return title ? { prefix: `${code} · `, title } : { prefix: code };
}

/**
 * What a screen reader says for the first line, as the Apple apps do:
 * "Season 1, Episode 2, Pilot" (or "Specials, Episode 2, …") rather than the
 * letters of "S01E02".
 */
export function episodeCardSpokenTitle(episode: EpisodeIdentity, episodeLabels?: string): string {
  const episodeNumber = episode.episode_number;
  const title = episode.title?.trim();
  if (labelsEpisodesByAirDate(episodeLabels)) {
    return title || formatEpisodeLabel({ ...episode, episode_labels: episodeLabels });
  }
  if (episode.season_number === 0) {
    return title
      ? t("web.detail.episodes.spokenSpecialTitled", { episode: episodeNumber, title })
      : t("web.detail.episodes.spokenSpecial", { episode: episodeNumber });
  }
  const season = episode.season_number;
  return title
    ? t("web.detail.episodes.spokenTitled", { season, episode: episodeNumber, title })
    : t("web.detail.episodes.spoken", { season, episode: episodeNumber });
}

/**
 * Second caption line: "Feb 18, 2026  ·  52m", or null when neither part is
 * known. Two spaces on each side of the dot, as on the Apple and Android cards;
 * they don't break, so the date and runtime stay on one line.
 */
export function episodeCardMetadata(
  episode: Pick<EpisodeListItem, "air_date" | "runtime">,
): string | null {
  const airDate = episode.air_date
    ? formatDate(episode.air_date, "medium") || episode.air_date
    : "";
  const parts = [airDate, formatRuntimeMinutes(episode.runtime)].filter(Boolean);
  return parts.length > 0 ? parts.join("\u00a0\u00a0·\u00a0\u00a0") : null;
}
