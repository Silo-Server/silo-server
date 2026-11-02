import type { EpisodeListItem } from "@/api/types";
import { formatEpisodeCode } from "@/lib/episodeCardLabels";
import { formatDate } from "@/lib/datetime";
import { formatRuntimeMinutes } from "@/lib/mediaFormat";

type EpisodeIdentity = Pick<EpisodeListItem, "season_number" | "episode_number" | "title">;

/** First caption line: "S01E02 · Pilot", or the code alone when the episode has no title. */
export function episodeCardTitle(episode: EpisodeIdentity): string {
  const code = formatEpisodeCode(episode.season_number, episode.episode_number);
  const title = episode.title?.trim();
  return title ? `${code} · ${title}` : code;
}

/**
 * What a screen reader says for the first line, as the Apple apps do:
 * "Season 1, Episode 2, Pilot" (or "Specials, Episode 2, …") rather than the
 * letters of "S01E02".
 */
export function episodeCardSpokenTitle(episode: EpisodeIdentity): string {
  const season = episode.season_number === 0 ? "Specials" : `Season ${episode.season_number}`;
  const code = `${season}, Episode ${episode.episode_number}`;
  const title = episode.title?.trim();
  return title ? `${code}, ${title}` : code;
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
