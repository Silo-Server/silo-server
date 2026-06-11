import { Bell } from "lucide-react";

import { NotificationStripBar } from "@/components/NotificationStripBar";
import { useMarkRead, useNotificationsList } from "@/hooks/queries/notifications";

interface NotificationStripProps {
  /**
   * When true, shift the dismiss button left on large screens so it clears the
   * admin-only ServerActivity widget pinned at `fixed top-5 right-5`.
   */
  reserveActivityWidget?: boolean;
}

export function NotificationStrip({ reserveActivityWidget }: NotificationStripProps = {}) {
  // Always refetch on home mount so newly pushed notifications appear without a
  // manual reload (matches AnnouncementBar).
  const list = useNotificationsList({ unread: true }, { staleTime: 0, refetchOnMount: "always" });
  const markRead = useMarkRead();

  const items = (list.data?.pages ?? []).flatMap((p) => p.items ?? []);
  const item = items.find((n) => n.category !== "announcement");

  if (!item) return null;

  return (
    <NotificationStripBar
      item={item}
      icon={Bell}
      variant="default"
      reserveActivityWidget={reserveActivityWidget}
      onDismiss={(id) => markRead.mutate({ ids: [id] })}
    />
  );
}
