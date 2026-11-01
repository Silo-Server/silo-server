import type { EpisodeListItem } from "@/api/types";
import { useItemEpisodes } from "@/hooks/queries/episodes";

/**
 * The episodes playback picks from, for a list read under `libraryId`.
 *
 * With catalog.scope_versions_to_library on, a list read under a library holds
 * only that library's files, but playback uses every version. When an episode
 * shows no file in `episodes`, this also reads the list without the library,
 * and that list decides what plays. `pending` stays true until it arrives (or
 * if it fails), and `episodes` falls back to the scoped list meanwhile.
 */
export function useEpisodePlaybackList(
  itemId: string | undefined,
  libraryId: number | undefined,
  episodes: EpisodeListItem[],
) {
  const needsUnscopedFiles =
    libraryId !== undefined && episodes.some((episode) => (episode.files?.length ?? 0) === 0);
  const { data } = useItemEpisodes(needsUnscopedFiles ? itemId : undefined);
  return {
    episodes: (needsUnscopedFiles ? data?.episodes : undefined) ?? episodes,
    pending: needsUnscopedFiles && !data,
  };
}
