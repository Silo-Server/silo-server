import { useCallback, useState } from "react";
import type { ItemDetail } from "@/api/types";
import { useToggleFavorite } from "@/hooks/queries/favorites";
import { useDeleteRating, useSetRating } from "@/hooks/queries/ratings";
import { useToggleWatchlist } from "@/hooks/queries/watchlist";
import { canMarkNotInterested, useToggleNotInterested } from "@/hooks/queries/notInterested";
import AllowTitleDialog from "@/components/profiles/AllowTitleDialog";
import {
  hasAllowableProfile,
  useCanManageTitleAllowances,
  useTitleAllowanceProfiles,
} from "@/hooks/queries/titleAllowances";
import ActionBar, { type ActionBarProps } from "./ActionBar";
import { useDetailWatchedActions } from "./useDetailWatchedActions";

type UserActionProps =
  | "isWatched"
  | "watchedLabel"
  | "onToggleWatched"
  | "isUpdatingWatched"
  | "onMarkUnwatched"
  | "onToggleFavorite"
  | "isFavorite"
  | "onToggleWatchlist"
  | "inWatchlist"
  | "onToggleNotInterested"
  | "notInterested"
  | "onAllowForProfiles"
  | "allowForLabel"
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
  const watchedActions = useDetailWatchedActions(item);
  const { mutate: toggleFavorite } = useToggleFavorite(item.content_id);
  const { mutate: toggleWatchlist } = useToggleWatchlist(item.content_id);
  const { mutate: toggleNotInterested } = useToggleNotInterested(item.content_id);
  const { mutate: setRating } = useSetRating(item.content_id);
  const { mutate: deleteRating } = useDeleteRating(item.content_id);
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
  const canManageAllowances = useCanManageTitleAllowances() && canMarkNotInterested(item.type);
  const { data: allowanceProfiles } = useTitleAllowanceProfiles(
    item.content_id,
    canManageAllowances,
  );
  const [allowDialogOpen, setAllowDialogOpen] = useState(false);
  const showAllowFor = canManageAllowances && hasAllowableProfile(allowanceProfiles);
  const allowedNames = (allowanceProfiles ?? [])
    .filter((p) => p.state === "allowed" || p.state === "allowed_by_collection")
    .map((p) => p.name);
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
    <>
      <ActionBar
        {...props}
        {...watchedActions}
        itemTitle={item.title}
        onToggleFavorite={handleToggleFavorite}
        isFavorite={isFavorite}
        onToggleWatchlist={handleToggleWatchlist}
        inWatchlist={inWatchlist}
        onToggleNotInterested={
          canMarkNotInterested(item.type) && item.user_state ? handleToggleNotInterested : undefined
        }
        notInterested={notInterested}
        onAllowForProfiles={showAllowFor ? () => setAllowDialogOpen(true) : undefined}
        allowForLabel={
          allowedNames.length > 0 ? `Allowed for ${allowedNames.join(", ")}` : "Allow for…"
        }
        rating={item.user_rating ?? null}
        onRatingChange={handleRatingChange}
      />
      {showAllowFor && (
        <AllowTitleDialog
          open={allowDialogOpen}
          onOpenChange={setAllowDialogOpen}
          itemId={item.content_id}
          itemTitle={item.title}
          profiles={allowanceProfiles ?? []}
        />
      )}
    </>
  );
}
