import { Megaphone } from "lucide-react";

import { NotificationStripBar } from "@/components/NotificationStripBar";
import { useMarkRead, useNotificationsList } from "@/hooks/queries/notifications";

interface AnnouncementBarProps {
  /**
   * When true, shift the dismiss button left on large screens so it clears the
   * admin-only ServerActivity widget pinned at `fixed top-5 right-5` (which
   * floats over the top-right corner of the page content).
   */
  reserveActivityWidget?: boolean;
}

export function AnnouncementBar({ reserveActivityWidget }: AnnouncementBarProps = {}) {
  // Always refetch when the home page mounts so a just-published announcement
  // shows the moment you land on the app, without a manual reload.
  const list = useNotificationsList(
    { unread: true, category: "announcement" },
    { staleTime: 0, refetchOnMount: "always" },
  );
  const markRead = useMarkRead();

  const items = (list.data?.pages ?? []).flatMap((p) => p.items ?? []);
  const item = items[0];

  if (!item) return null;

  return (
    <NotificationStripBar
      item={item}
      icon={Megaphone}
      label="ANNOUNCEMENT"
      variant="announcement"
      reserveActivityWidget={reserveActivityWidget}
      onDismiss={(id) => markRead.mutate({ ids: [id] })}
    />
  );
}
