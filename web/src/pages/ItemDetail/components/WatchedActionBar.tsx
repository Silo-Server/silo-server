import { useCallback } from "react";
import type { ItemDetail } from "@/api/types";
import { useWatchedStateMutation } from "@/hooks/queries/items";
import { useClearResumePositionAction } from "@/hooks/queries/progress";
import { getWatchedActionLabel } from "../watchedState";
import ActionBar, { type ActionBarProps } from "./ActionBar";

type WatchedActionProps =
  | "isWatched"
  | "watchedLabel"
  | "onToggleWatched"
  | "isUpdatingWatched"
  | "onClearResumePosition";

interface WatchedActionBarProps extends Omit<ActionBarProps, WatchedActionProps> {
  item: ItemDetail;
}

/** Keeps mutation lifecycle renders inside the episode action bar. */
export default function WatchedActionBar({ item, ...props }: WatchedActionBarProps) {
  const { mutate: toggleWatched, isPending: isUpdatingWatched } = useWatchedStateMutation(item);
  // A partly watched movie or episode offers Mark Watched, which records a
  // play even on a rewatch, and a Mark Unwatched in the More menu that only
  // clears the resume position.
  const clearResumePosition = useClearResumePositionAction(item);
  const marksWatched = clearResumePosition !== undefined;
  const played = item.user_data?.played ?? false;
  const handleToggleWatched = useCallback(
    () => toggleWatched(marksWatched || !played),
    [marksWatched, played, toggleWatched],
  );

  return (
    <ActionBar
      {...props}
      itemTitle={item.title}
      watchedLabel={marksWatched ? "Mark Watched" : getWatchedActionLabel(item)}
      isWatched={!marksWatched && played}
      onToggleWatched={handleToggleWatched}
      isUpdatingWatched={isUpdatingWatched}
      onClearResumePosition={clearResumePosition}
    />
  );
}
