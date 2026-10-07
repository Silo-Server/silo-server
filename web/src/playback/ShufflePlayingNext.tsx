import { useCallback, useEffect, useMemo, useRef } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { toast } from "sonner";
import { V2ProblemError } from "@/api/v2/request";
import { advanceShuffle, deleteShuffle, skipShuffleItem, type Shuffle } from "@/api/v2/shuffles";
import type { ContinueWatchingItem } from "@/hooks/queries/progress";
import { shuffleKeys } from "@/hooks/queries/keys";
import { useShuffle } from "@/hooks/queries/shuffles";
import type { WatchRouteRequest } from "@/pages/watchRouteHelpers";
import { PlayingNextScreen } from "@/player/components/PlayingNextScreen";
import type { EpisodeRef, PlaybackStartTrigger } from "@/player/types";
import { useWatchPlaybackController } from "./watchPlaybackContext";

interface ShufflePlayingNextProps {
  /** The playback that is ending, and the shuffle it belongs to. */
  request: WatchRouteRequest;
  shuffleId: string;
  seriesId?: string;
  continueWatchingItems: ContinueWatchingItem[];
  videoEnded: boolean;
  onPlayItem: (contentId: string) => void;
  onClose: () => void;
}

/**
 * The post-roll of a shuffle: the server's next pick, with Pick Another and
 * Stop shuffling. Loaded only when a shuffled item reaches its end, so none of
 * it ships in the launch bundle.
 */
export default function ShufflePlayingNext({
  request,
  shuffleId,
  seriesId,
  continueWatchingItems,
  videoEnded,
  onPlayItem,
  onClose,
}: ShufflePlayingNextProps) {
  const queryClient = useQueryClient();
  const controller = useWatchPlaybackController();
  const shuffleQuery = useShuffle(shuffleId);
  // When the server says nothing in the scope can play any more (409), the
  // cached pick must not be offered: the screen shows Finished. Any other
  // failed read keeps the last pick; advancing re-checks it on the server.
  const exhausted =
    shuffleQuery.error instanceof V2ProblemError && shuffleQuery.error.status === 409;
  const shuffle = exhausted ? undefined : shuffleQuery.data;
  // Set once the viewer leaves this screen. A Play Next request still in
  // flight then must not start playback again.
  const leftRef = useRef(false);
  useEffect(() => {
    leftRef.current = false;
    return () => {
      leftRef.current = true;
    };
  }, []);
  const handleClose = useCallback(() => {
    leftRef.current = true;
    onClose();
  }, [onClose]);
  const handlePlayItem = useCallback(
    (contentId: string) => {
      leftRef.current = true;
      onPlayItem(contentId);
    },
    [onPlayItem],
  );
  // A scope with one playable item announces that item again; playing it
  // would restart the same session, so the screen shows Finished instead.
  const nextEpisode = useMemo(
    () =>
      shuffle && shuffle.next.content_id !== request.contentId
        ? shuffleNextEpisodeRef(shuffle)
        : undefined,
    [request.contentId, shuffle],
  );

  // Plays the shuffle's next pick. The server moves the shuffle on only while
  // the item that just played is still its current one, so a repeated press
  // or a retry plays the same pick.
  const handlePlayNext = useCallback(
    async (trigger: PlaybackStartTrigger) => {
      try {
        const advanced = await advanceShuffle(shuffleId, request.contentId);
        queryClient.setQueryData(shuffleKeys.detail(shuffleId), advanced);
        if (leftRef.current) return;
        controller.startPlayback(
          {
            contentId: advanced.current.content_id,
            shuffleId,
            // Like the first pick, every shuffled item plays from the start.
            restart: true,
            returnHref: request.returnHref,
          },
          trigger,
        );
      } catch {
        if (!leftRef.current) toast.error("Couldn't continue the shuffle.");
      }
    },
    [controller, queryClient, request.contentId, request.returnHref, shuffleId],
  );

  const handlePickAnother = useCallback(async () => {
    if (!shuffle) return;
    try {
      const skipped = await skipShuffleItem(shuffleId, shuffle.next.content_id);
      queryClient.setQueryData(shuffleKeys.detail(shuffleId), skipped);
    } catch {
      toast.error("Couldn't pick another.");
    }
  }, [queryClient, shuffle, shuffleId]);

  const handleStop = useCallback(() => {
    void deleteShuffle(shuffleId).catch(() => undefined);
    queryClient.removeQueries({ queryKey: shuffleKeys.detail(shuffleId) });
    handleClose();
  }, [handleClose, queryClient, shuffleId]);

  return (
    <PlayingNextScreen
      seriesId={seriesId}
      seriesTitle={shuffle ? shuffleNextHeading(shuffle) : undefined}
      nextEpisode={nextEpisode}
      continueWatchingItems={continueWatchingItems}
      videoEnded={videoEnded}
      onPlayNow={nextEpisode ? (trigger) => void handlePlayNext(trigger) : undefined}
      onPlayItem={handlePlayItem}
      onClose={handleClose}
      shuffle={
        shuffle
          ? {
              scopeLabel: shuffleScopeLabel(shuffle),
              onStop: handleStop,
              onReshuffle: () => void handlePickAnother(),
            }
          : undefined
      }
    />
  );
}

/** The shuffle's next pick in the shape the post-roll screen renders. */
function shuffleNextEpisodeRef(shuffle: Shuffle): EpisodeRef {
  const next = shuffle.next;
  const episode = next.type === "episode";
  return {
    contentId: next.content_id,
    // A movie has no season or episode; the screen hides that line at zero.
    seasonNumber: episode ? (next.season_number ?? 0) : 0,
    episodeNumber: episode ? (next.episode_number ?? 0) : 0,
    title: next.title,
    runtime: next.runtime ?? 0,
    overview: next.overview,
    // An episode card's poster is its 16:9 still; a movie's poster is
    // portrait, so it shows its backdrop instead.
    stillUrl: (episode ? next.poster_url : undefined) ?? next.backdrop_url,
    stillThumbhash: (episode ? next.poster_thumbhash : undefined) ?? next.backdrop_thumbhash,
    airDate: next.release_date ?? null,
  };
}

/** The post-roll heading: an episode's series, or a movie's own title. */
function shuffleNextHeading(shuffle: Shuffle): string {
  return shuffle.next.type === "episode" ? (shuffle.next.series_title ?? "") : shuffle.next.title;
}

/** Names what the shuffle draws from: "Movies", "Breaking Bad · Season 2". */
function shuffleScopeLabel(shuffle: Shuffle): string {
  const { title, parent_title: parentTitle } = shuffle.scope;
  return parentTitle ? `${parentTitle} · ${title}` : title;
}
