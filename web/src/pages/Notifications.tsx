import { Fragment, useState } from "react";
import { Link } from "react-router";
import { Bell, BellOff, Check, CheckCheck, Loader2, Settings2 } from "lucide-react";
import type { AppNotification } from "@/api/types";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Separator } from "@/components/ui/separator";
import {
  formatEpisodeCode,
  useMarkAllNotificationsRead,
  useMarkNotificationRead,
  useNotificationPreferences,
  useNotifications,
  useUnreadNotificationCount,
  useUpdateNotificationPreferences,
} from "@/hooks/queries/notifications";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { decodeThumbhash } from "@/lib/thumbhash";

function formatNotificationTime(value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  const diffMs = Date.now() - date.getTime();
  const diffMinutes = Math.round(diffMs / 60_000);
  if (diffMinutes < 1) {
    return "Just now";
  }
  if (diffMinutes < 60) {
    return `${diffMinutes}m ago`;
  }
  const diffHours = Math.round(diffMinutes / 60);
  if (diffHours < 24) {
    return `${diffHours}h ago`;
  }
  const diffDays = Math.round(diffHours / 24);
  if (diffDays < 7) {
    return `${diffDays}d ago`;
  }
  return date.toLocaleDateString(undefined, { month: "short", day: "numeric" });
}

function notificationTitle(notification: AppNotification): string {
  if (notification.type === "episode.available") {
    return notification.series_title || "New episode available";
  }
  if (notification.type === "request.fulfilled") {
    return notification.series_title || "Request available";
  }
  if (notification.type === "request.approved" || notification.type === "request.declined") {
    return notification.reason_flags?.title || "Media request";
  }
  // Unknown types render with a generic fallback by design — the type
  // registry is extensible.
  return "Notification";
}

function notificationDescription(notification: AppNotification): string {
  if (notification.type === "episode.available") {
    const code = formatEpisodeCode(notification);
    return (
      [code, notification.episode_title].filter(Boolean).join(" — ") || "New episode available"
    );
  }
  if (notification.type === "request.fulfilled") {
    const mediaType = notification.reason_flags?.media_type;
    return mediaType === "movie"
      ? "Your requested movie is now available"
      : mediaType === "series"
        ? "Your requested series is now available"
        : "Your request is now available";
  }
  if (notification.type === "request.approved") {
    return "Your request was approved";
  }
  if (notification.type === "request.declined") {
    const reason = notification.reason_flags?.reason;
    return reason ? `Your request was declined — ${reason}` : "Your request was declined";
  }
  return notification.type;
}

function reasonLabels(notification: AppNotification): string[] {
  const flags = notification.reason_flags ?? {};
  const labels: string[] = [];
  if (flags.favorite) {
    labels.push("Favorite");
  }
  if (flags.watchlist) {
    labels.push("Watchlist");
  }
  if (flags.continue_watching) {
    labels.push("Continue Watching");
  }
  if (flags.next_up) {
    labels.push("Next Up");
  }
  return labels;
}

function NotificationRow({
  notification,
  onMarkRead,
}: {
  notification: AppNotification;
  onMarkRead: (id: string) => void;
}) {
  const unread = !notification.read_at;
  const thumbhashUrl = notification.poster_thumbhash
    ? decodeThumbhash(notification.poster_thumbhash)
    : "";
  const detailHref = notification.episode_id
    ? `/item/${notification.episode_id}`
    : notification.series_id
      ? `/item/${notification.series_id}`
      : null;

  const body = (
    <>
      <div
        className="bg-muted relative h-16 w-11 shrink-0 overflow-hidden rounded-md"
        style={
          thumbhashUrl
            ? {
                backgroundImage: `url(${thumbhashUrl})`,
                backgroundSize: "cover",
                backgroundPosition: "center",
              }
            : undefined
        }
      >
        {notification.poster_url && (
          <img
            src={notification.poster_url}
            alt=""
            className="h-full w-full object-cover"
            loading="lazy"
          />
        )}
      </div>
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          {unread && (
            <span
              className="h-2 w-2 shrink-0 rounded-full"
              style={{ background: "var(--primary)" }}
              aria-label="Unread"
            />
          )}
          <span className={`truncate text-sm ${unread ? "font-semibold" : "font-medium"}`}>
            {notificationTitle(notification)}
          </span>
          <span className="text-muted-foreground ml-auto shrink-0 text-xs">
            {formatNotificationTime(notification.created_at)}
          </span>
        </div>
        <div className="text-muted-foreground mt-0.5 truncate text-sm">
          {notificationDescription(notification)}
        </div>
        {reasonLabels(notification).length > 0 && (
          <div className="mt-1.5 flex flex-wrap gap-1">
            {reasonLabels(notification).map((label) => (
              <span
                key={label}
                className="bg-muted text-muted-foreground rounded-full px-2 py-0.5 text-[10px] font-medium"
              >
                {label}
              </span>
            ))}
          </div>
        )}
      </div>
    </>
  );

  return (
    <li className="group relative">
      {detailHref ? (
        <Link
          to={detailHref}
          onClick={() => unread && onMarkRead(notification.id)}
          className={`hover:bg-muted/60 flex items-start gap-3 rounded-xl px-3 py-3 transition-colors ${
            unread ? "bg-muted/30" : ""
          }`}
        >
          {body}
        </Link>
      ) : (
        <div
          className={`flex items-start gap-3 rounded-xl px-3 py-3 ${unread ? "bg-muted/30" : ""}`}
        >
          {body}
        </div>
      )}
      {unread && (
        <Button
          variant="ghost"
          size="icon"
          className="absolute right-2 bottom-2 h-7 w-7 opacity-0 transition-opacity group-hover:opacity-100"
          title="Mark as read"
          onClick={(event) => {
            event.preventDefault();
            onMarkRead(notification.id);
          }}
        >
          <Check className="h-4 w-4" />
        </Button>
      )}
    </li>
  );
}

function NotificationPreferencesPopover() {
  const { data: prefs, isLoading, refetch } = useNotificationPreferences();
  const updatePrefs = useUpdateNotificationPreferences();

  const toggles: Array<{
    key:
      | "enabled"
      | "notify_favorites"
      | "notify_watchlist"
      | "notify_continue_watching"
      | "notify_next_up";
    label: string;
    description: string;
  }> = [
    {
      key: "enabled",
      label: "Notifications",
      description: "Master switch for this profile",
    },
    {
      key: "notify_favorites",
      label: "Favorites",
      description: "New episodes of favorited series",
    },
    {
      key: "notify_watchlist",
      label: "Watchlist",
      description: "New episodes of watchlisted series",
    },
    {
      key: "notify_continue_watching",
      label: "Continue Watching",
      description: "Series you are actively watching",
    },
    {
      key: "notify_next_up",
      label: "Next Up",
      description: "The next episode after your progress",
    },
  ];

  return (
    <Popover>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm">
          <Settings2 className="mr-1.5 h-4 w-4" />
          Preferences
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-4">
        {isLoading ? (
          <div className="space-y-3">
            <Skeleton className="h-6 w-full" />
            <Skeleton className="h-6 w-full" />
            <Skeleton className="h-6 w-full" />
          </div>
        ) : !prefs ? (
          <div className="space-y-3">
            <p className="text-muted-foreground text-sm">Couldn’t load preferences.</p>
            <Button size="sm" variant="outline" onClick={() => void refetch()}>
              Retry
            </Button>
          </div>
        ) : (
          <div className="space-y-3.5">
            {toggles.map((toggle, index) => (
              <Fragment key={toggle.key}>
                {index === 1 && <Separator />}
                <div
                  className={`flex items-center justify-between gap-3 transition-opacity ${
                    index > 0 && !prefs.enabled ? "opacity-50" : ""
                  }`}
                >
                  <div className="min-w-0">
                    <div className="text-sm font-medium">{toggle.label}</div>
                    <div className="text-muted-foreground text-xs">{toggle.description}</div>
                  </div>
                  <Switch
                    checked={prefs[toggle.key]}
                    disabled={index > 0 && !prefs.enabled}
                    onCheckedChange={(checked) => updatePrefs.mutate({ [toggle.key]: checked })}
                  />
                </div>
              </Fragment>
            ))}
          </div>
        )}
      </PopoverContent>
    </Popover>
  );
}

export default function Notifications() {
  useDocumentTitle("Notifications");
  const [statusFilter, setStatusFilter] = useState<"all" | "unread">("all");
  const list = useNotifications(statusFilter);
  const { data: unreadCount } = useUnreadNotificationCount();
  const markRead = useMarkNotificationRead();
  const markAllRead = useMarkAllNotificationsRead();

  const notifications = list.data?.pages.flatMap((page) => page.notifications) ?? [];

  return (
    <div className="mx-auto w-full max-w-3xl px-4 py-8">
      <div className="mb-6 flex flex-wrap items-center gap-3">
        <h1 className="flex items-center gap-2.5 text-2xl font-semibold">
          <Bell className="h-6 w-6" />
          Notifications
        </h1>
        <div className="ml-auto flex items-center gap-2">
          {(unreadCount ?? 0) > 0 && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => markAllRead.mutate()}
              disabled={markAllRead.isPending}
            >
              <CheckCheck className="mr-1.5 h-4 w-4" />
              Mark all read
            </Button>
          )}
          <NotificationPreferencesPopover />
        </div>
      </div>

      <div className="mb-4 flex gap-1.5">
        {(["all", "unread"] as const).map((status) => (
          <Button
            key={status}
            variant={statusFilter === status ? "secondary" : "ghost"}
            size="sm"
            onClick={() => setStatusFilter(status)}
          >
            {status === "all" ? "All" : `Unread${unreadCount ? ` (${unreadCount})` : ""}`}
          </Button>
        ))}
      </div>

      {list.isLoading ? (
        <div className="space-y-2">
          {Array.from({ length: 5 }).map((_, index) => (
            <Skeleton key={index} className="h-20 w-full rounded-xl" />
          ))}
        </div>
      ) : notifications.length === 0 ? (
        <div className="text-muted-foreground flex flex-col items-center gap-3 py-20 text-center">
          <BellOff className="h-10 w-10 opacity-50" />
          <div className="text-sm">
            {statusFilter === "unread" ? "No unread notifications" : "No notifications yet"}
          </div>
          <div className="max-w-sm text-xs">
            You will be notified here when new episodes arrive for series you favorite, watchlist,
            or are watching.
          </div>
        </div>
      ) : (
        <>
          <ul className="list-none space-y-1">
            {notifications.map((notification) => (
              <NotificationRow
                key={notification.id}
                notification={notification}
                onMarkRead={(id) => markRead.mutate(id)}
              />
            ))}
          </ul>
          {list.hasNextPage && (
            <div className="mt-6 flex justify-center">
              <Button
                variant="outline"
                onClick={() => list.fetchNextPage()}
                disabled={list.isFetchingNextPage}
              >
                {list.isFetchingNextPage && <Loader2 className="mr-2 h-4 w-4 animate-spin" />}
                Load more
              </Button>
            </div>
          )}
        </>
      )}
    </div>
  );
}
