import { Megaphone, X } from "lucide-react";
import { Link } from "react-router";

import { Button } from "@/components/ui/button";
import { useMarkRead, useNotificationsList } from "@/hooks/queries/notifications";
import { timeAgo } from "@/lib/timeAgo";

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

  const when = timeAgo(item.created_at, "") ?? new Date(item.created_at).toLocaleDateString();

  return (
    <div className="border-primary/30 bg-primary/10 text-primary flex items-center gap-3 rounded-lg border px-4 py-2">
      <Link
        to={item.link ?? "/notifications"}
        onClick={() => markRead.mutate({ ids: [item.id] })}
        className="flex min-w-0 flex-1 items-center gap-3"
      >
        <Megaphone className="h-4 w-4 shrink-0" />
        <span className="shrink-0 text-xs font-semibold tracking-wide">ANNOUNCEMENT</span>
        <span className="text-foreground shrink-0 font-medium">{item.title}</span>
        {item.body && (
          <span className="text-muted-foreground min-w-0 flex-1 truncate text-xs">{item.body}</span>
        )}
        {when && <span className="text-muted-foreground shrink-0 text-xs">{when}</span>}
      </Link>
      <Button
        variant="ghost"
        size="icon-xs"
        aria-label="Dismiss"
        className={`text-primary hover:text-primary ${reserveActivityWidget ? "lg:mr-10" : ""}`}
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
