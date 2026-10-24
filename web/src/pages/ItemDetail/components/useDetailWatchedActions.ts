import { useCallback } from "react";
import type { ItemDetail } from "@/api/types";
import { useWatchedStateMutation } from "@/hooks/queries/items";
import { useClearResumePositionAction } from "@/hooks/queries/progress";
import { getWatchedActionLabel } from "../watchedState";

/**
 * The detail page's watched actions. A partly watched movie or episode,
 * rewatches included, offers Mark Watched, which records a play, and a Mark
 * Unwatched for the More menu that only clears the resume position.
 */
export function useDetailWatchedActions(item: ItemDetail) {
  const { mutate: setWatched, isPending: isUpdatingWatched } = useWatchedStateMutation(item);
  const onClearResumePosition = useClearResumePositionAction(item);
  const inProgress = onClearResumePosition !== undefined;
  const isWatched = !inProgress && (item.user_data?.played ?? false);
  const onToggleWatched = useCallback(() => setWatched(!isWatched), [isWatched, setWatched]);

  return {
    watchedLabel: getWatchedActionLabel({ type: item.type, user_data: { played: isWatched } }),
    isWatched,
    onToggleWatched,
    isUpdatingWatched,
    onClearResumePosition,
  };
}
