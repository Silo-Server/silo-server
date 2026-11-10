import { useAuth } from "@/hooks/useAuth";
import { notificationScope } from "@/api/v2/notifications";
import { Fragment, useState, useRef } from "react";
import { useSearchParams } from "react-router";
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
import { preferredDateLocale } from "@/lib/datetime";
import { requestDetailHref } from "@/lib/mediaRequests";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { NotifiedSeriesList } from "@/components/notifications/NotifiedSeriesList";
import { useNotificationCapability } from "@/hooks/queries/notificationWebhooks";
import { NotificationPoster } from "@/components/notifications/NotificationPoster";
import { NOTIFICATION_REASONS } from "@/components/notifications/reasons";
import { useT, type Translate } from "@/i18n";

function formatNotificationTime(t: Translate, value: string): string {
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) {
    return "";
  }
  const diffMs = Date.now() - date.getTime();
  const diffMinutes = Math.round(diffMs / 60_000);
  if (diffMinutes < 1) {
    return t("web.notifications.inbox.justNow");
  }
  if (diffMinutes < 60) {
    return t("web.notifications.inbox.minutesAgo", { minutes: String(diffMinutes) });
  }
  const diffHours = Math.round(diffMinutes / 60);
  if (diffHours < 24) {
    return t("web.notifications.inbox.hoursAgo", { hours: String(diffHours) });
  }
  const diffDays = Math.round(diffHours / 24);
  if (diffDays < 7) {
    return t("web.notifications.inbox.daysAgo", { days: String(diffDays) });
  }
  return date.toLocaleDateString(preferredDateLocale(), { month: "short", day: "numeric" });
}

function notificationTitle(t: Translate, notification: AppNotification): string {
  if (notification.type === "episode.available") {
    return notification.series_title || t("web.notifications.inbox.newEpisode");
  }
  if (notification.type === "request.fulfilled") {
    return notification.series_title || t("web.notifications.inbox.requestAvailable");
  }
  if (notification.type === "request.approved" || notification.type === "request.declined") {
    return notification.reason_flags?.title || t("web.notifications.inbox.mediaRequest");
  }
  // Unknown types render with a generic fallback by design — the type
  // registry is extensible.
  return t("web.notifications.inbox.genericTitle");
}

function notificationDescription(t: Translate, notification: AppNotification): string {
  if (notification.type === "episode.available") {
    const code = formatEpisodeCode(notification);
    const title = notification.episode_title;
    if (code && title) return t("web.notifications.inbox.episodeLine", { code, title });
    return code || title || t("web.notifications.inbox.newEpisode");
  }
  if (notification.type === "request.fulfilled") {
    const mediaType = notification.reason_flags?.media_type;
    if (notification.reason_flags?.follower) {
      return mediaType === "movie"
        ? t("web.notifications.inbox.followedMovieAvailable")
        : mediaType === "series"
          ? t("web.notifications.inbox.followedSeriesAvailable")
          : t("web.notifications.inbox.followedTitleAvailable");
    }
    return mediaType === "movie"
      ? t("web.notifications.inbox.requestedMovieAvailable")
      : mediaType === "series"
        ? t("web.notifications.inbox.requestedSeriesAvailable")
        : t("web.notifications.inbox.requestAvailableBody");
  }
  if (notification.type === "request.approved") {
    return t("web.notifications.inbox.requestApproved");
  }
  if (notification.type === "request.declined") {
    const reason = notification.reason_flags?.reason;
    return reason
      ? t("web.notifications.inbox.requestDeclinedReason", { reason })
      : t("web.notifications.inbox.requestDeclined");
  }
  return notification.type;
}

/**
 * Where a row leads. Episode and fulfilled-request rows open the catalog item;
 * approved and declined requests have no catalog item yet, so they open the
 * title's page from the TMDB id their payload carries.
 */
function notificationHref(notification: AppNotification): string | null {
  if (notification.episode_id) return `/item/${notification.episode_id}`;
  if (notification.series_id) return `/item/${notification.series_id}`;
  if (notification.type === "request.approved" || notification.type === "request.declined") {
    const { media_type: mediaType, tmdb_id: tmdbID } = notification.reason_flags ?? {};
    if ((mediaType === "movie" || mediaType === "series") && tmdbID && tmdbID > 0) {
      return requestDetailHref(mediaType, tmdbID);
    }
  }
  return null;
}

function reasonLabels(t: Translate, notification: AppNotification): string[] {
  const flags = notification.reason_flags ?? {};
  return NOTIFICATION_REASONS.filter((reason) => flags[reason.key]).map((reason) =>
    t(reason.labelKey),
  );
}

function NotificationRow({
  notification,
  onMarkRead,
}: {
  notification: AppNotification;
  onMarkRead: (id: string) => void;
}) {
  const t = useT();
  const unread = !notification.read_at;
  const detailHref = notificationHref(notification);
  const reasons = reasonLabels(t, notification);

  const body = (
    <>
      <NotificationPoster url={notification.poster_url} thumbhash={notification.poster_thumbhash} />
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          {unread && (
            <span
              className="h-2 w-2 shrink-0 rounded-full"
              style={{ background: "var(--primary)" }}
              aria-label={t("web.notifications.inbox.unread")}
            />
          )}
          <span className={`truncate text-sm ${unread ? "font-semibold" : "font-medium"}`}>
            {notificationTitle(t, notification)}
          </span>
          <span className="text-muted-foreground ml-auto shrink-0 text-xs">
            {formatNotificationTime(t, notification.created_at)}
          </span>
        </div>
        <div className="text-muted-foreground mt-0.5 truncate text-sm">
          {notificationDescription(t, notification)}
        </div>
        {reasons.length > 0 && (
          <div className="mt-1.5 flex flex-wrap gap-1">
            {reasons.map((label) => (
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
        <ViewTransitionLink
          to={detailHref}
          onClick={() => unread && onMarkRead(notification.id)}
          className={`hover:bg-muted/60 flex items-start gap-3 rounded-xl px-3 py-3 transition-colors ${
            unread ? "bg-muted/30" : ""
          }`}
        >
          {body}
        </ViewTransitionLink>
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
          title={t("web.notifications.inbox.markRead")}
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

function NotificationPreferencesPopover({
  open,
  onOpenChange,
  onSeeSeries,
}: {
  open: boolean;
  onOpenChange: (open: boolean) => void;
  /** Opens the Series view; absent when the server has none. */
  onSeeSeries?: () => void;
}) {
  const t = useT();
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
      label: t("web.notifications.preferences.enabled"),
      description: t("web.notifications.preferences.enabledDescription"),
    },
    {
      key: "notify_favorites",
      label: t("web.notifications.preferences.favorites"),
      description: t("web.notifications.preferences.favoritesDescription"),
    },
    {
      key: "notify_watchlist",
      label: t("web.notifications.preferences.watchlist"),
      description: t("web.notifications.preferences.watchlistDescription"),
    },
    {
      key: "notify_continue_watching",
      label: t("web.notifications.preferences.continueWatching"),
      description: t("web.notifications.preferences.continueWatchingDescription"),
    },
    {
      key: "notify_next_up",
      label: t("web.notifications.preferences.nextUp"),
      description: t("web.notifications.preferences.nextUpDescription"),
    },
  ];

  return (
    <Popover open={open} onOpenChange={onOpenChange}>
      <PopoverTrigger asChild>
        <Button variant="outline" size="sm">
          <Settings2 className="mr-1.5 h-4 w-4" />
          {t("web.notifications.preferences.open")}
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
            <p className="text-muted-foreground text-sm">
              {t("web.notifications.preferences.loadFailed")}
            </p>
            <Button size="sm" variant="outline" onClick={() => void refetch()}>
              {t("common.retry")}
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
            {onSeeSeries && (
              <>
                <Separator />
                <Button
                  variant="link"
                  size="sm"
                  className="h-auto p-0"
                  onClick={() => {
                    onOpenChange(false);
                    onSeeSeries();
                  }}
                >
                  {t("web.notifications.preferences.seeSeries")}
                </Button>
              </>
            )}
          </div>
        )}
      </PopoverContent>
    </Popover>
  );
}

export default function Notifications() {
  useAuth();
  return <NotificationsInbox key={notificationScope()} />;
}
function NotificationsInbox() {
  const t = useT();
  useDocumentTitle(t("web.notifications.inbox.title"));
  const [statusFilter, setStatusFilter] = useState<"all" | "unread">("all");
  const [searchParams, setSearchParams] = useSearchParams();
  const [preferencesOpen, setPreferencesOpen] = useState(false);
  const { data: capability } = useNotificationCapability();
  const seriesAvailable = capability?.series?.available === true;
  const showSeries = seriesAvailable && searchParams.get("view") === "series";
  const openSeries = seriesAvailable ? () => setSearchParams({ view: "series" }) : undefined;
  const list = useNotifications(statusFilter);
  const { data: unreadCount } = useUnreadNotificationCount();
  const markRead = useMarkNotificationRead();
  const markAllRead = useMarkAllNotificationsRead();
  const markAllBusy = useRef(false);
  const cutoff = list.data?.pages[0]?.read_cutoff;
  async function markDisplayedRead() {
    if (markAllBusy.current || !cutoff) return;
    markAllBusy.current = true;
    try {
      await markAllRead.mutateAsync(cutoff);
    } catch {
      /* The mutation exposes the error and refreshes authoritative state. */
    } finally {
      markAllBusy.current = false;
    }
  }

  const notifications = list.data?.pages.flatMap((page) => page.notifications) ?? [];

  return (
    <div className="mx-auto w-full max-w-3xl px-4 py-8">
      <div className="mb-6 flex flex-wrap items-center gap-3">
        <h1 className="flex items-center gap-2.5 text-2xl font-semibold">
          <Bell className="h-6 w-6" />
          {t("web.notifications.inbox.title")}
        </h1>
        <div className="ml-auto flex items-center gap-2">
          {!showSeries && (unreadCount ?? 0) > 0 && (
            <Button
              variant="outline"
              size="sm"
              onClick={() => void markDisplayedRead()}
              disabled={markAllRead.isPending || markAllRead.isError || !cutoff}
            >
              <CheckCheck className="mr-1.5 h-4 w-4" />
              {t("web.notifications.inbox.markAllRead")}
            </Button>
          )}
          <NotificationPreferencesPopover
            open={preferencesOpen}
            onOpenChange={setPreferencesOpen}
            onSeeSeries={openSeries}
          />
        </div>
      </div>

      <div className="mb-4 flex gap-1.5">
        {(["all", "unread"] as const).map((status) => (
          <Button
            key={status}
            variant={!showSeries && statusFilter === status ? "secondary" : "ghost"}
            size="sm"
            onClick={() => {
              setStatusFilter(status);
              if (showSeries) setSearchParams({}, { replace: true });
            }}
          >
            {status === "all"
              ? t("web.notifications.inbox.filterAll")
              : unreadCount
                ? t("web.notifications.inbox.filterUnreadCount", { count: String(unreadCount) })
                : t("web.notifications.inbox.filterUnread")}
          </Button>
        ))}
        {openSeries && (
          <Button variant={showSeries ? "secondary" : "ghost"} size="sm" onClick={openSeries}>
            {t("web.notifications.inbox.filterSeries")}
          </Button>
        )}
      </div>

      {showSeries ? (
        <NotifiedSeriesList
          onOpenPreferences={() => setPreferencesOpen(true)}
          canMute={capability?.series?.mute === true}
        />
      ) : (
        <>
          {markAllRead.isError && (
            <p role="alert">
              {t("web.notifications.inbox.markAllFailed", {
                reload: (chunks) => (
                  <Button
                    onClick={() => {
                      void list.restart().then(() => markAllRead.reset());
                    }}
                  >
                    {chunks}
                  </Button>
                ),
              })}
            </p>
          )}
          {list.isError && (
            <div role="alert">
              {t("web.notifications.inbox.loadFailed", {
                reload: (chunks) => (
                  <Button
                    onClick={() => {
                      void list.restart().then(() => markAllRead.reset());
                    }}
                  >
                    {chunks}
                  </Button>
                ),
              })}
            </div>
          )}
          {list.isLoading ? (
            <div className="space-y-2">
              {Array.from({ length: 5 }).map((_, index) => (
                <Skeleton key={index} className="h-20 w-full rounded-xl" />
              ))}
            </div>
          ) : notifications.length === 0 && !list.isError ? (
            <div className="text-muted-foreground flex flex-col items-center gap-3 py-20 text-center">
              <BellOff className="h-10 w-10 opacity-50" />
              <div className="text-sm">
                {statusFilter === "unread"
                  ? t("web.notifications.inbox.emptyUnread")
                  : t("web.notifications.inbox.emptyAll")}
              </div>
              <div className="max-w-sm text-xs">{t("web.notifications.inbox.emptyHint")}</div>
              {openSeries && (
                <Button variant="link" size="sm" onClick={openSeries}>
                  {t("web.notifications.inbox.seeSeriesCanNotify")}
                </Button>
              )}
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
                    {t("web.notifications.inbox.loadMore")}
                  </Button>
                </div>
              )}
            </>
          )}
        </>
      )}
    </div>
  );
}
