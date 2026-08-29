import { useCallback } from "react";
import type { ItemDetail } from "@/api/types";
import { useWatchedStateMutation } from "@/hooks/queries/items";
import { getWatchedActionLabel } from "../watchedState";
import ActionBar, { type ActionBarProps } from "./ActionBar";

type WatchedActionProps = "watchedLabel" | "onToggleWatched" | "isUpdatingWatched";

interface WatchedActionBarProps extends Omit<ActionBarProps, WatchedActionProps> {
  item: ItemDetail;
}

/** Keeps mutation lifecycle renders inside the episode action bar. */
export default function WatchedActionBar({ item, ...props }: WatchedActionBarProps) {
  const { mutate: toggleWatched, isPending: isUpdatingWatched } = useWatchedStateMutation(item);
  const handleToggleWatched = useCallback(
    () => toggleWatched(!(item.user_data?.played ?? false)),
    [item.user_data?.played, toggleWatched],
  );

  return (
    <ActionBar
      {...props}
      watchedLabel={getWatchedActionLabel(item)}
      onToggleWatched={handleToggleWatched}
      isUpdatingWatched={isUpdatingWatched}
    />
  );
}
