import { useCallback } from "react";
import type { ItemDetail } from "@/api/types";
import { useWatchedStateMutation } from "@/hooks/queries/items";
import { getWatchedActionLabel } from "../watchedState";

/**
 * The detail page's watched actions. A partly watched movie or episode,
 * rewatches included, offers Mark Watched, which records a play, and a Mark
 * Unwatched for the More menu.
 */
export function useDetailWatchedActions(item: ItemDetail) {
  const { mutate: setWatched, isPending: isUpdatingWatched } = useWatchedStateMutation(item);
  // Leaf detail also carries the season counters, so read the position itself.
  const userData = item.user_data;
  const position =
    userData && "position_seconds" in userData ? (userData.position_seconds ?? 0) : 0;
  const inProgress = (item.type === "movie" || item.type === "episode") && position > 0;
  const isWatched = !inProgress && (userData?.played ?? false);
  const onToggleWatched = useCallback(() => setWatched(!isWatched), [isWatched, setWatched]);
  const markUnwatched = useCallback(() => setWatched(false), [setWatched]);

  return {
    watchedLabel: getWatchedActionLabel({ type: item.type, user_data: { played: isWatched } }),
    isWatched,
    onToggleWatched,
    isUpdatingWatched,
    onMarkUnwatched: inProgress ? markUnwatched : undefined,
  };
}
