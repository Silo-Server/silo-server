import type { Shuffle } from "@/api/v2/shuffles";
import type { EpisodeRef } from "@/player/types";

/** The shuffle's next pick in the shape the post-roll screen renders. */
export function shuffleNextEpisodeRef(shuffle: Shuffle): EpisodeRef {
  const next = shuffle.next;
  const episode = next.type === "episode";
  // An episode card's poster is its 16:9 still; a movie's poster is
  // portrait, so it shows its backdrop instead. The URL and the ThumbHash
  // fall back separately, so they can come from different images.
  const urlFromPoster = episode && next.poster_url != null;
  const thumbhashFromPoster = episode && next.poster_thumbhash != null;
  const isStill = (fromPoster: boolean) =>
    fromPoster ? next.poster_is_episode_still : next.backdrop_is_episode_still;
  return {
    contentId: next.content_id,
    // A movie has no season or episode; the screen hides that line at zero.
    seasonNumber: episode ? (next.season_number ?? 0) : 0,
    episodeNumber: episode ? (next.episode_number ?? 0) : 0,
    title: next.title,
    runtime: next.runtime ?? 0,
    overview: next.overview,
    stillUrl: urlFromPoster ? next.poster_url : next.backdrop_url,
    stillThumbhash: thumbhashFromPoster ? next.poster_thumbhash : next.backdrop_thumbhash,
    // Unprotected only when neither image is known to be an episode still.
    stillIsEpisodeStill:
      isStill(urlFromPoster) === false && isStill(thumbhashFromPoster) === false
        ? false
        : undefined,
    airDate: next.release_date ?? null,
    watchState: episode
      ? { played: next.user_state?.played, position_seconds: next.position_seconds }
      : undefined,
    isMovie: !episode,
  };
}
