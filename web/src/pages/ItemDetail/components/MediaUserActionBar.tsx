import { useCallback } from "react";
import type { ItemDetail } from "@/api/types";
import { useToggleFavorite } from "@/hooks/queries/favorites";
import { useWatchedStateMutation } from "@/hooks/queries/items";
import { useResetProgressAction } from "@/hooks/queries/progress";
import { useDeleteRating, useSetRating } from "@/hooks/queries/ratings";
import { useToggleWatchlist } from "@/hooks/queries/watchlist";
import { canMarkNotInterested, useToggleNotInterested } from "@/hooks/queries/notInterested";
import { itemTitleLinks } from "@/lib/titleLinks";
import { getWatchedActionLabel } from "../watchedState";
import ActionBar, { type ActionBarProps } from "./ActionBar";

type UserActionProps =
  | "isWatched"
  | "watchedLabel"
  | "onToggleWatched"
  | "isUpdatingWatched"
  | "onResetProgress"
  | "onToggleFavorite"
  | "isFavorite"
  | "onToggleWatchlist"
  | "inWatchlist"
  | "onToggleNotInterested"
  | "notInterested"
  | "rating"
  | "onRatingChange"
  | "links";

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
  const resetProgress = useResetProgressAction(item);

  const handleToggleWatched = useCallback(
    () => toggleWatched(!(item.user_data?.played ?? false)),
    [item.user_data?.played, toggleWatched],
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
      links={itemTitleLinks(item)}
      watchedLabel={getWatchedActionLabel(item)}
      isWatched={item.user_data?.played ?? false}
      onToggleWatched={handleToggleWatched}
      isUpdatingWatched={isUpdatingWatched}
      onResetProgress={resetProgress}
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
