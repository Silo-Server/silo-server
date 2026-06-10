import { Bell } from "lucide-react";
import { Link } from "react-router";

import { Badge } from "@/components/ui/badge";
import { Button } from "@/components/ui/button";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { useMarkRead, useNotificationsList, useUnreadCount } from "@/hooks/queries/notifications";

let bellOpenFlag = false;

/** True while the bell dropdown is open — used to suppress toasts.
 * Single-instance only: the bell must be mounted once; a second instance would fight over this flag. */
export function isNotificationDropdownOpen() {
  return bellOpenFlag;
}

/** Test-only escape hatch. */
export function setNotificationDropdownOpenForTests(open: boolean) {
  bellOpenFlag = open;
}

export function NotificationBell() {
  const { data: count = 0 } = useUnreadCount();
  const list = useNotificationsList({});
  const markRead = useMarkRead();
  const items = (list.data?.pages ?? []).flatMap((p) => p.items ?? []).slice(0, 10);

  return (
    <Popover
      onOpenChange={(open) => {
        bellOpenFlag = open;
      }}
    >
      <PopoverTrigger asChild>
        <Button variant="ghost" size="icon" aria-label="Notifications" className="relative">
          <Bell className="h-5 w-5" />
          {count > 0 && (
            <Badge className="absolute -top-1 -right-1 h-4 min-w-4 px-1 text-[10px]">
              {count > 99 ? "99+" : count}
            </Badge>
          )}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-0">
        <div className="flex items-center justify-between border-b px-3 py-2">
          <span className="text-sm font-medium">Notifications</span>
          <Button
            variant="ghost"
            size="sm"
            disabled={count === 0}
            onClick={() => markRead.mutate({ all: true })}
          >
            Mark all read
          </Button>
        </div>
        <ul className="max-h-96 list-none overflow-y-auto">
          {items.length === 0 && (
            <li className="text-muted-foreground px-3 py-6 text-center text-sm">
              You&apos;re all caught up
            </li>
          )}
          {items.map((n) => (
            <li key={n.id} className={n.read_at ? "opacity-60" : ""}>
              <Link to={n.link ?? "/notifications"} className="hover:bg-accent block px-3 py-2">
                <div className="text-sm font-medium">{n.title}</div>
                {n.body && <div className="text-muted-foreground truncate text-xs">{n.body}</div>}
              </Link>
            </li>
          ))}
        </ul>
        <div className="border-t px-3 py-2 text-center">
          <Link to="/notifications" className="text-primary text-sm">
            View all
          </Link>
        </div>
      </PopoverContent>
    </Popover>
  );
}
