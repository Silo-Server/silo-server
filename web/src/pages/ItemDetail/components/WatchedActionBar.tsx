import { useCallback } from "react";
import type { ItemDetail } from "@/api/types";
import { useWatchedStateMutation } from "@/hooks/queries/items";
import { itemTitleLinks } from "@/lib/titleLinks";
import { getWatchedActionLabel } from "../watchedState";
import ActionBar, { type ActionBarProps } from "./ActionBar";

type WatchedActionProps =
  | "isWatched"
  | "watchedLabel"
  | "onToggleWatched"
  | "isUpdatingWatched"
  | "links";

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
      itemTitle={item.title}
      links={itemTitleLinks(item)}
      watchedLabel={getWatchedActionLabel(item)}
      isWatched={item.user_data?.played ?? false}
      onToggleWatched={handleToggleWatched}
      isUpdatingWatched={isUpdatingWatched}
    />
  );
}
