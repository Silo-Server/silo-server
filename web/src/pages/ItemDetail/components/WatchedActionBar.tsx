import { useCallback } from "react";
import type { ItemDetail } from "@/api/types";
import { useWatchedStateMutation } from "@/hooks/queries/items";
import { useRatingChange } from "@/hooks/queries/ratings";
import { getWatchedActionLabel } from "../watchedState";
import ActionBar, { type ActionBarProps } from "./ActionBar";

type WatchedActionProps =
  | "isWatched"
  | "watchedLabel"
  | "onToggleWatched"
  | "isUpdatingWatched"
  | "rating"
  | "onRatingChange";

interface WatchedActionBarProps extends Omit<ActionBarProps, WatchedActionProps> {
  item: ItemDetail;
  /**
   * Shows the star picker. An episode is rateable and a season is not: Silo
   * rates movies, series and episodes, which is also the full set the watch
   * providers can carry, so a season rating would stay on this server alone.
   */
  rateable?: boolean;
}

/** Keeps mutation lifecycle renders inside the season and episode action bar. */
export default function WatchedActionBar({
  item,
  rateable = false,
  ...props
}: WatchedActionBarProps) {
  const { mutate: toggleWatched, isPending: isUpdatingWatched } = useWatchedStateMutation(item);
  const handleRatingChange = useRatingChange(item.content_id);
  const handleToggleWatched = useCallback(
    () => toggleWatched(!(item.user_data?.played ?? false)),
    [item.user_data?.played, toggleWatched],
  );

  return (
    <ActionBar
      {...props}
      itemTitle={item.title}
      watchedLabel={getWatchedActionLabel(item)}
      isWatched={item.user_data?.played ?? false}
      onToggleWatched={handleToggleWatched}
      isUpdatingWatched={isUpdatingWatched}
      {...(rateable
        ? { rating: item.user_rating ?? null, onRatingChange: handleRatingChange }
        : {})}
    />
  );
}
