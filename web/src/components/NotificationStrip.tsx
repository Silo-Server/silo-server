import { Bell, X } from "lucide-react";
import { Link } from "react-router";

import { Button } from "@/components/ui/button";
import { useMarkRead, useNotificationsList } from "@/hooks/queries/notifications";
import { timeAgo } from "@/lib/timeAgo";

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

  const when = timeAgo(item.created_at, "") ?? new Date(item.created_at).toLocaleDateString();

  return (
    <div className="border-border bg-surface flex items-center gap-3 rounded-lg border px-4 py-2">
      <Link
        to={item.link ?? "/notifications"}
        onClick={() => markRead.mutate({ ids: [item.id] })}
        className="flex min-w-0 flex-1 items-center gap-3"
      >
        <Bell className="text-muted-foreground h-4 w-4 shrink-0" />
        <span className="shrink-0 font-medium">{item.title}</span>
        {item.body && (
          <span className="text-muted-foreground min-w-0 flex-1 truncate text-xs">{item.body}</span>
        )}
        {when && <span className="text-muted-foreground shrink-0 text-xs">{when}</span>}
      </Link>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label="Dismiss"
        className={reserveActivityWidget ? "lg:mr-10" : undefined}
        onClick={(e) => {
          e.stopPropagation();
          markRead.mutate({ ids: [item.id] });
        }}
      >
        <X className="h-3 w-3" />
      </Button>
    </div>
  );
}
