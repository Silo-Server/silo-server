import { codeNumber, t } from "@/i18n";
import type { EpisodeListItem, SpannedEpisode } from "@/api/types";
import { formatEpisodeCode } from "@/lib/episodeCardLabels";

type RangeEpisode = Pick<
  EpisodeListItem,
  "episode_number" | "episode_number_end" | "spanned_episodes"
>;

/**
 * The last episode number a multi-episode row plays (S01E02-E03 gives 3), or
 * undefined for a single episode.
 */
export function episodeRangeEnd(episode: RangeEpisode): number | undefined {
  const end = episode.episode_number_end;
  return end != null && end > episode.episode_number ? end : undefined;
}

/** How many episodes a row plays: 1 for a single episode. */
export function episodeRangeCount(episode: Pick<EpisodeListItem, "spanned_episodes">): number {
  return 1 + (episode.spanned_episodes?.length ?? 0);
}

/** "15–16", the episode number column of a multi-episode row; "15" for one episode. */
export function formatEpisodeNumberRange(episode: RangeEpisode): string {
  const end = episodeRangeEnd(episode);
  return end == null
    ? codeNumber(episode.episode_number)
    : t("web.detail.episodes.numberRange", {
        start: codeNumber(episode.episode_number),
        end: codeNumber(end),
      });
}

/** "S12E15–E16", or "S12E15" for a single episode. */
export function formatEpisodeRangeCode(seasonNumber: number, episode: RangeEpisode): string {
  const end = episodeRangeEnd(episode);
  return end == null
    ? formatEpisodeCode(seasonNumber, episode.episode_number)
    : t("web.detail.episodeRangeCode", {
        season: codeNumber(seasonNumber, 2),
        start: codeNumber(episode.episode_number, 2),
        end: codeNumber(end, 2),
      });
}

/**
 * Where an episode sits in a season-ordered list of playable rows: the row
 * for that episode, or the multi-episode row whose range holds it (E05 sits in
 * the E04–E05 row). -1 when no row holds it.
 */
export function indexOfEpisodeRow(
  rows: readonly { seasonNumber: number; episodeNumber: number; episodeNumberEnd?: number }[],
  seasonNumber: number,
  episodeNumber: number,
): number {
  return rows.findIndex(
    (row) =>
      row.seasonNumber === seasonNumber &&
      row.episodeNumber <= episodeNumber &&
      episodeNumber <= Math.max(row.episodeNumber, row.episodeNumberEnd ?? 0),
  );
}

// A trailing part number: "Name (2)", "Name, Part 2", "Name: Part 2",
// "Name - Part 2". Metadata providers title the halves of a two-parter this way.
const PART_SUFFIX = /^(.*?)(?:\s*\(\d+\)|[\s,:–-]+Part\s+\d+)\s*$/i;

/**
 * The title a multi-episode row shows. Parts named "Name (1)", "Name (2)" (or
 * "Name, Part 1") show their shared name once; numbered parts with other
 * names drop their numbers and are joined with " / ", as are parts without
 * numbers. Untitled parts are left out.
 */
export function combinedEpisodeTitle(
  episode: Pick<EpisodeListItem, "title"> & { spanned_episodes?: Pick<SpannedEpisode, "title">[] },
): string {
  const titles = [episode.title, ...(episode.spanned_episodes ?? []).map((part) => part.title)]
    .map((title) => title?.trim() ?? "")
    .filter(Boolean);
  if (titles.length <= 1) {
    return titles[0] ?? "";
  }
  const bases = titles.map((title) => PART_SUFFIX.exec(title)?.[1]?.trim() ?? "");
  if (bases.some((base) => !base)) {
    return titles.join(" / ");
  }
  return bases.every((base) => base === bases[0]) ? (bases[0] ?? "") : bases.join(" / ");
}

/**
 * Whether a multi-episode row's episodes have different descriptions, so its
 * page offers a tab per episode. When they all share one description, or none
 * has one, the page shows the first episode's description alone.
 */
export function episodePartsDiffer(
  episode: Pick<EpisodeListItem, "overview"> & {
    spanned_episodes?: Pick<SpannedEpisode, "overview">[];
  },
): boolean {
  const parts = episode.spanned_episodes ?? [];
  const first = episode.overview?.trim() ?? "";
  return parts.some((part) => (part.overview?.trim() ?? "") !== first);
}
