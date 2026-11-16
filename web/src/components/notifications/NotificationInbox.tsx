import { useId, useMemo, useState, type ReactNode } from "react";
import { BellOff, Check, ChevronDown, ChevronRight, Film, Loader2 } from "lucide-react";
import type { AppNotification } from "@/api/types";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Skeleton } from "@/components/ui/skeleton";
import {
  formatEpisodeCode,
  useMarkNotificationsRead,
  useSetManyNotificationSeriesMuted,
  type useMarkAllNotificationsRead,
  type useNotifications,
} from "@/hooks/queries/notifications";
import { preferredDateLocale } from "@/lib/datetime";
import { requestDetailHref } from "@/lib/mediaRequests";
import { cn } from "@/lib/utils";
import { useT, type MessageKey, type Translate } from "@/i18n";
import { NotificationPoster } from "./NotificationPoster";
import { SelectionBar } from "./SelectionBar";
import { groupInbox, type InboxDay, type InboxEntry } from "./inboxGroups";

const DAY_HEADINGS: Record<InboxDay, MessageKey> = {
  today: "web.notifications.inbox.dayToday",
  yesterday: "web.notifications.inbox.dayYesterday",
  week: "web.notifications.inbox.dayWeek",
  older: "web.notifications.inbox.dayOlder",
};

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
    return t("web.notifications.inbox.minutesAgo", { minutes: diffMinutes });
  }
  const diffHours = Math.round(diffMinutes / 60);
  if (diffHours < 24) {
    return t("web.notifications.inbox.hoursAgo", { hours: diffHours });
  }
  const diffDays = Math.round(diffHours / 24);
  if (diffDays < 7) {
    return t("web.notifications.inbox.daysAgo", { days: diffDays });
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

/** The episodes a series entry announces, from first to last: "S5E2 – S5E4 ·
 * latest “Valentine’s Day”". */
function episodeRange(t: Translate, notifications: AppNotification[]): string {
  const order = (n: AppNotification) => (n.season_number ?? 0) * 100_000 + (n.episode_number ?? 0);
  const sorted = [...notifications].sort((a, b) => order(a) - order(b));
  const [earliest] = sorted;
  const last = sorted.at(-1);
  const first = earliest && formatEpisodeCode(earliest);
  const lastCode = last && formatEpisodeCode(last);
  if (!last || !first || !lastCode) {
    const count = notifications.length;
    return t("web.notifications.inbox.episodeCount", { count, count_text: String(count) });
  }
  return last.episode_title
    ? t("web.notifications.inbox.episodeRangeLatest", {
        first,
        last: lastCode,
        title: last.episode_title,
      })
    : t("web.notifications.inbox.episodeRange", { first, last: lastCode });
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

function unreadIds(entry: InboxEntry) {
  return entry.notifications.filter((n) => !n.read_at).map((n) => n.id);
}

function MarkReadButton({
  describedBy,
  onClick,
  className,
}: {
  describedBy?: string;
  onClick: () => void;
  className?: string;
}) {
  const t = useT();
  return (
    <Button
      variant="ghost"
      size="icon"
      className={cn("text-muted-foreground h-7 w-7 shrink-0", className)}
      title={t("web.notifications.inbox.markRead")}
      aria-label={t("web.notifications.inbox.markRead")}
      aria-describedby={describedBy}
      onClick={(event) => {
        event.preventDefault();
        event.stopPropagation();
        onClick();
      }}
    >
      <Check className="h-4 w-4" />
    </Button>
  );
}

/** A series entry's poster: one poster, or a short stack when the entry
 * holds several episodes. */
function EntryPoster({
  notification,
  stacked,
}: {
  notification: AppNotification;
  stacked: boolean;
}) {
  const poster = (
    <NotificationPoster url={notification.poster_url} thumbhash={notification.poster_thumbhash} />
  );
  if (!stacked) return <div className="flex w-12 shrink-0">{poster}</div>;
  return (
    <div className="relative h-[4.375rem] w-12 shrink-0">
      <div className="absolute end-0 top-0 opacity-40">{poster}</div>
      <div className="absolute start-0 bottom-0">{poster}</div>
    </div>
  );
}

function InboxEntryRow({
  entry,
  selecting,
  selected,
  expanded,
  onToggleSelected,
  onToggleExpanded,
  onMarkRead,
}: {
  entry: InboxEntry;
  selecting: boolean;
  selected: boolean;
  expanded: boolean;
  onToggleSelected: () => void;
  onToggleExpanded: () => void;
  onMarkRead: (ids: string[]) => void;
}) {
  const t = useT();
  const titleId = useId();
  const newest = entry.latest;
  const count = entry.notifications.length;
  const grouped = count > 1;
  const unread = unreadIds(entry);
  const isRequest = newest.type.startsWith("request.");
  const href = grouped ? null : notificationHref(newest);

  const body = (
    <>
      {selecting && (
        <Checkbox
          checked={selected}
          onCheckedChange={onToggleSelected}
          onClick={(event) => event.stopPropagation()}
          aria-labelledby={titleId}
          className="size-[1.125rem] rounded-[5px]"
        />
      )}
      <EntryPoster notification={newest} stacked={grouped} />
      <div className="min-w-0 flex-1">
        <div className="flex min-w-0 items-center gap-2">
          <span
            id={titleId}
            className={cn("truncate text-sm", unread.length > 0 ? "font-semibold" : "font-medium")}
          >
            {notificationTitle(t, newest)}
          </span>
          {grouped && unread.length > 0 && (
            <span className="bg-secondary text-foreground shrink-0 rounded-md px-1.5 py-0.5 text-[11px] font-semibold">
              {t("web.notifications.inbox.newCount", {
                count: unread.length,
                count_text: String(unread.length),
              })}
            </span>
          )}
          {isRequest && (
            <span className="bg-secondary text-muted-foreground hidden shrink-0 items-center gap-1 rounded-full px-2 py-0.5 text-[11px] font-medium sm:inline-flex">
              <Film className="h-3 w-3" />
              {t("web.notifications.inbox.requestTag")}
            </span>
          )}
        </div>
        <div className="text-muted-foreground mt-0.5 truncate text-[13px]">
          {grouped ? episodeRange(t, entry.notifications) : notificationDescription(t, newest)}
        </div>
      </div>
      {unread.length > 0 ? (
        <span
          className="bg-primary h-2 w-2 shrink-0 rounded-full"
          role="img"
          aria-label={t("web.notifications.inbox.unread")}
        />
      ) : (
        <span className="w-2 shrink-0" />
      )}
      {/* Room for the mark-read button, which sits outside the link. */}
      {!selecting && !grouped && unread.length > 0 && (
        <span className="hidden w-7 shrink-0 sm:block" />
      )}
      <span className="text-muted-foreground shrink-0 text-end text-xs sm:w-16">
        {formatNotificationTime(t, newest.created_at)}
      </span>
      {!selecting &&
        (grouped ? (
          expanded ? (
            <ChevronDown className="text-foreground h-4 w-4 shrink-0" aria-hidden />
          ) : (
            <ChevronRight className="text-muted-foreground h-4 w-4 shrink-0" aria-hidden />
          )
        ) : (
          <ChevronRight
            className={cn(
              "text-muted-foreground hidden h-4 w-4 shrink-0 sm:block",
              !href && "invisible",
            )}
            aria-hidden
          />
        ))}
    </>
  );

  const rowClass = cn(
    "flex w-full items-center gap-3 rounded-xl px-3 py-2.5 text-start transition-colors",
    selected
      ? "bg-secondary ring-border ring-1"
      : cn("hover:bg-muted/60", unread.length > 0 && "bg-muted/30"),
  );

  let row: ReactNode;
  if (selecting) {
    row = (
      <div className={cn(rowClass, "cursor-pointer")} onClick={onToggleSelected}>
        {body}
      </div>
    );
  } else if (grouped) {
    row = (
      <button
        type="button"
        className={rowClass}
        aria-expanded={expanded}
        onClick={onToggleExpanded}
      >
        {body}
      </button>
    );
  } else if (href) {
    row = (
      <ViewTransitionLink
        to={href}
        onClick={() => unread.length > 0 && onMarkRead(unread)}
        className={rowClass}
      >
        {body}
      </ViewTransitionLink>
    );
  } else {
    row = <div className={rowClass}>{body}</div>;
  }

  return (
    <li className="group relative">
      {row}
      {!selecting && !grouped && unread.length > 0 && (
        <MarkReadButton
          describedBy={titleId}
          onClick={() => onMarkRead(unread)}
          className="absolute end-[7.25rem] top-1/2 hidden -translate-y-1/2 opacity-0 transition-opacity group-focus-within:opacity-100 group-hover:opacity-100 sm:inline-flex"
        />
      )}
      {grouped && expanded && !selecting && (
        <ul className="border-border ms-[4.5rem] mt-0.5 mb-2 list-none border-s ps-3.5">
          {entry.notifications.map((notification) => {
            const code = formatEpisodeCode(notification);
            const lineHref = notificationHref(notification);
            const lineUnread = !notification.read_at;
            const line = (
              <>
                <span className="text-muted-foreground w-16 shrink-0 font-mono text-xs">
                  {code}
                </span>
                <span
                  className={cn(
                    "min-w-0 flex-1 truncate",
                    lineUnread ? "font-semibold" : "text-muted-foreground",
                  )}
                >
                  {notification.episode_title || t("web.notifications.inbox.newEpisode")}
                </span>
                <span className="text-muted-foreground shrink-0 text-xs">
                  {formatNotificationTime(t, notification.created_at)}
                </span>
                <span className="w-7 shrink-0" />
              </>
            );
            const lineClass =
              "hover:bg-muted/60 flex h-9 items-center gap-3 rounded-lg px-2 text-[13px]";
            return (
              <li key={notification.id} className="relative">
                {lineHref ? (
                  <ViewTransitionLink
                    to={lineHref}
                    onClick={() => lineUnread && onMarkRead([notification.id])}
                    className={lineClass}
                  >
                    {line}
                  </ViewTransitionLink>
                ) : (
                  <div className={lineClass}>{line}</div>
                )}
                {lineUnread && (
                  <MarkReadButton
                    onClick={() => onMarkRead([notification.id])}
                    className="absolute end-2 top-1/2 -translate-y-1/2"
                  />
                )}
              </li>
            );
          })}
        </ul>
      )}
    </li>
  );
}

/**
 * The inbox view of the notifications page. Notifications sit under day
 * headings, and a series' new episodes under one heading share one row that
 * opens to list them. Select mode picks rows to mark read, or whose series to
 * mute, together.
 */
export function NotificationInbox({
  switcher,
  status,
  onStatusChange,
  list,
  unreadCount,
  markAllRead,
  canMuteSeries,
  onOpenSeries,
}: {
  /** The Inbox/Series control, placed at the start of the toolbar. */
  switcher: ReactNode;
  status: "all" | "unread";
  onStatusChange: (status: "all" | "unread") => void;
  list: ReturnType<typeof useNotifications>;
  unreadCount: number | undefined;
  markAllRead: ReturnType<typeof useMarkAllNotificationsRead>;
  /** The server can mute many series at once. */
  canMuteSeries: boolean;
  onOpenSeries?: () => void;
}) {
  const t = useT();
  const markRead = useMarkNotificationsRead();
  const muteMany = useSetManyNotificationSeriesMuted();
  const [selecting, setSelecting] = useState(false);
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [expanded, setExpanded] = useState<ReadonlySet<string>>(new Set());

  const sections = useMemo(
    () => groupInbox(list.data?.pages.flatMap((page) => page.notifications) ?? []),
    [list.data],
  );
  const chosen = sections.flatMap((section) => section.entries).filter((e) => selected.has(e.key));
  const chosenUnread = chosen.flatMap(unreadIds);
  const chosenSeries = [
    ...new Set(chosen.flatMap((entry) => (entry.seriesId ? [entry.seriesId] : []))),
  ];
  const chosenCount = chosen.reduce((sum, entry) => sum + entry.notifications.length, 0);

  const toggle = (set: ReadonlySet<string>, key: string) => {
    const next = new Set(set);
    if (!next.delete(key)) next.add(key);
    return next;
  };
  const markIds = (ids: string[]) => {
    if (ids.length > 0) markRead.mutate(ids);
  };
  const stopSelecting = () => {
    setSelecting(false);
    setSelected(new Set());
  };

  const reload = (chunks: ReactNode) => (
    <Button
      onClick={() => {
        void list.restart().then(() => markAllRead.reset());
      }}
    >
      {chunks}
    </Button>
  );

  let body: ReactNode;
  if (list.isLoading) {
    body = (
      <div className="space-y-2">
        {Array.from({ length: 5 }).map((_, index) => (
          <Skeleton key={index} className="h-20 w-full rounded-xl" />
        ))}
      </div>
    );
  } else if (sections.length === 0 && !list.isError) {
    body = (
      <div className="text-muted-foreground flex flex-col items-center gap-3 py-20 text-center">
        <BellOff className="h-10 w-10 opacity-50" />
        <div className="text-sm">
          {status === "unread"
            ? t("web.notifications.inbox.emptyUnread")
            : t("web.notifications.inbox.emptyAll")}
        </div>
        <div className="max-w-sm text-xs">{t("web.notifications.inbox.emptyHint")}</div>
        {onOpenSeries && (
          <Button variant="link" size="sm" onClick={onOpenSeries}>
            {t("web.notifications.inbox.seeSeriesCanNotify")}
          </Button>
        )}
      </div>
    );
  } else {
    body = (
      <>
        {sections.map((section) => (
          <section key={section.day} aria-labelledby={`inbox-day-${section.day}`}>
            <h2
              id={`inbox-day-${section.day}`}
              className="text-muted-foreground mt-5 mb-1.5 px-3 text-xs font-semibold tracking-wider uppercase"
            >
              {t(DAY_HEADINGS[section.day])}
            </h2>
            <ul className="list-none space-y-1">
              {section.entries.map((entry) => (
                <InboxEntryRow
                  key={entry.key}
                  entry={entry}
                  selecting={selecting}
                  selected={selected.has(entry.key)}
                  expanded={expanded.has(entry.key)}
                  onToggleSelected={() => setSelected((set) => toggle(set, entry.key))}
                  onToggleExpanded={() => setExpanded((set) => toggle(set, entry.key))}
                  onMarkRead={markIds}
                />
              ))}
            </ul>
          </section>
        ))}
        {list.hasNextPage && (
          <div className="mt-6 flex justify-center">
            <Button
              variant="outline"
              onClick={() => list.fetchNextPage()}
              disabled={list.isFetchingNextPage}
            >
              {list.isFetchingNextPage && <Loader2 className="me-2 h-4 w-4 animate-spin" />}
              {t("web.notifications.inbox.loadMore")}
            </Button>
          </div>
        )}
      </>
    );
  }

  return (
    <div>
      <div className="flex flex-wrap items-center justify-between gap-3">
        {switcher}
        <div className="flex flex-wrap items-center gap-1">
          {(["all", "unread"] as const).map((option) => (
            <Button
              key={option}
              variant={status === option ? "secondary" : "ghost"}
              size="sm"
              className="rounded-full"
              aria-pressed={status === option}
              onClick={() => onStatusChange(option)}
            >
              {option === "all"
                ? t("web.notifications.inbox.filterAll")
                : unreadCount
                  ? t("web.notifications.inbox.filterUnreadCount", { count: unreadCount })
                  : t("web.notifications.inbox.filterUnread")}
            </Button>
          ))}
          {sections.length > 0 &&
            (selecting ? (
              <Button size="sm" className="ms-1.5" onClick={stopSelecting}>
                {t("common.done")}
              </Button>
            ) : (
              <Button
                variant="outline"
                size="sm"
                className="ms-1.5"
                onClick={() => setSelecting(true)}
              >
                <Check className="h-4 w-4" />
                {t("web.notifications.selection.select")}
              </Button>
            ))}
        </div>
      </div>
      {markAllRead.isError && (
        <p role="alert" className="mt-3 text-sm">
          {t("web.notifications.inbox.markAllFailed", { reload })}
        </p>
      )}
      {list.isError && (
        <div role="alert" className="mt-3 text-sm">
          {t("web.notifications.inbox.loadFailed", { reload })}
        </div>
      )}
      <div className="mt-1">{body}</div>
      {selecting && chosen.length > 0 && (
        <SelectionBar
          label={t("web.notifications.selection.inboxLabel")}
          onClear={() => setSelected(new Set())}
          summary={
            <>
              <span className="font-semibold">
                {t("web.notifications.selection.count", {
                  count: chosen.length,
                  count_text: String(chosen.length),
                })}
              </span>
              <span className="text-muted-foreground">
                {t("web.notifications.selection.notificationCount", {
                  count: chosenCount,
                  count_text: String(chosenCount),
                })}
              </span>
            </>
          }
        >
          <Button
            variant="ghost"
            size="sm"
            disabled={chosenUnread.length === 0}
            onClick={() => {
              markIds(chosenUnread);
              setSelected(new Set());
            }}
          >
            <Check className="h-4 w-4" />
            {t("web.notifications.selection.markRead")}
          </Button>
          {canMuteSeries && (
            <Button
              variant="ghost"
              size="sm"
              disabled={chosenSeries.length === 0 || muteMany.isPending}
              onClick={() => {
                // Deselect only the series rows this mutes; keep request rows
                // and rows picked while the request runs.
                const done = chosen.flatMap((entry) => (entry.seriesId ? [entry.key] : []));
                muteMany.mutate(
                  { seriesIds: chosenSeries, muted: true },
                  {
                    onSuccess: () =>
                      setSelected((set) => new Set([...set].filter((key) => !done.includes(key)))),
                  },
                );
              }}
            >
              <BellOff className="h-4 w-4" />
              {t("web.notifications.selection.muteSeries")}
            </Button>
          )}
        </SelectionBar>
      )}
    </div>
  );
}
