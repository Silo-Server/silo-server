import { useCallback } from "react";
import type { ItemDetail } from "@/api/types";
import { useToggleFavorite } from "@/hooks/queries/favorites";
import { useWatchedStateMutation } from "@/hooks/queries/items";
import { useClearResumePositionAction } from "@/hooks/queries/progress";
import { useDeleteRating, useSetRating } from "@/hooks/queries/ratings";
import { useToggleWatchlist } from "@/hooks/queries/watchlist";
import { canMarkNotInterested, useToggleNotInterested } from "@/hooks/queries/notInterested";
import { getWatchedActionLabel } from "../watchedState";
import ActionBar, { type ActionBarProps } from "./ActionBar";

type UserActionProps =
  | "isWatched"
  | "watchedLabel"
  | "onToggleWatched"
  | "isUpdatingWatched"
  | "onClearResumePosition"
  | "onToggleFavorite"
  | "isFavorite"
  | "onToggleWatchlist"
  | "inWatchlist"
  | "onToggleNotInterested"
  | "notInterested"
  | "rating"
  | "onRatingChange";

interface MediaUserActionBarProps extends Omit<ActionBarProps, UserActionProps> {
  item: ItemDetail;
}

/**
 * Owns the frequently changing mutation state for the detail-page user actions.
 * Keeping these hooks below the page content boundary prevents a mutation's
 * pending/settled notifications from re-rendering the hero and below-fold page.
 */
export default function MediaUserActionBar({ item, ...props }: MediaUserActionBarProps) {
  const isFavorite = item.user_state?.is_favorite ?? false;
  const inWatchlist = item.user_state?.in_watchlist ?? false;
  const notInterested = item.user_state?.not_interested ?? false;
  const { mutate: toggleWatched, isPending: isUpdatingWatched } = useWatchedStateMutation(item);
  const { mutate: toggleFavorite } = useToggleFavorite(item.content_id);
  const { mutate: toggleWatchlist } = useToggleWatchlist(item.content_id);
  const { mutate: toggleNotInterested } = useToggleNotInterested(item.content_id);
  const { mutate: setRating } = useSetRating(item.content_id);
  const { mutate: deleteRating } = useDeleteRating(item.content_id);
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
  const handleToggleFavorite = useCallback(
    () => toggleFavorite(isFavorite),
    [isFavorite, toggleFavorite],
  );
  const handleToggleWatchlist = useCallback(
    () => toggleWatchlist(inWatchlist),
    [inWatchlist, toggleWatchlist],
  );
  const handleToggleNotInterested = useCallback(
    () => toggleNotInterested(notInterested),
    [notInterested, toggleNotInterested],
  );
  const handleRatingChange = useCallback(
    (rating: number | null) => {
      if (rating === null) {
        deleteRating();
      } else {
        setRating(rating);
      }
    },
    [deleteRating, setRating],
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
      onToggleFavorite={handleToggleFavorite}
      isFavorite={isFavorite}
      onToggleWatchlist={handleToggleWatchlist}
      inWatchlist={inWatchlist}
      onToggleNotInterested={
        canMarkNotInterested(item.type) && item.user_state ? handleToggleNotInterested : undefined
      }
      notInterested={notInterested}
      rating={item.user_rating ?? null}
      onRatingChange={handleRatingChange}
    />
  );
}
