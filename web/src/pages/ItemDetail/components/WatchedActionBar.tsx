import type { ItemDetail } from "@/api/types";
import ActionBar, { type ActionBarProps } from "./ActionBar";
import { useDetailWatchedActions } from "./useDetailWatchedActions";

type WatchedActionProps =
  | "isWatched"
  | "watchedLabel"
  | "onToggleWatched"
  | "isUpdatingWatched"
  | "onMarkUnwatched";

interface WatchedActionBarProps extends Omit<ActionBarProps, WatchedActionProps> {
  item: ItemDetail;
}

/** Keeps mutation lifecycle renders inside the episode action bar. */
export default function WatchedActionBar({ item, ...props }: WatchedActionBarProps) {
  const watchedActions = useDetailWatchedActions(item);

  return <ActionBar {...props} {...watchedActions} itemTitle={item.title} />;
}
