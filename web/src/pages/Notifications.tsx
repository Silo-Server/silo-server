import { useAuth } from "@/hooks/useAuth";
import { notificationScope } from "@/api/v2/notifications";
import { Fragment, useRef, useState } from "react";
import { useSearchParams } from "react-router";
import { Bell, CheckCheck, Settings2 } from "lucide-react";
import { Button } from "@/components/ui/button";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import { Popover, PopoverContent, PopoverTrigger } from "@/components/ui/popover";
import { Separator } from "@/components/ui/separator";
import {
  useMarkAllNotificationsRead,
  useNotificationPreferences,
  useNotifications,
  useUnreadNotificationCount,
  useUpdateNotificationPreferences,
} from "@/hooks/queries/notifications";
import { useDocumentTitle } from "@/hooks/useDocumentTitle";
import { NotificationInbox } from "@/components/notifications/NotificationInbox";
import { NotifiedSeriesList } from "@/components/notifications/NotifiedSeriesList";
import { useNotificationCapability } from "@/hooks/queries/notificationWebhooks";
import { cn } from "@/lib/utils";
import { useT } from "@/i18n";

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
    key: "enabled" | "notify_next_up";
    label: string;
    description: string;
  }> = [
    {
      key: "enabled",
      label: t("web.notifications.preferences.enabled"),
      description: t("web.notifications.preferences.enabledDescription"),
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
          <Settings2 className="me-1.5 h-4 w-4" />
          {t("web.notifications.preferences.open")}
        </Button>
      </PopoverTrigger>
      <PopoverContent align="end" className="w-80 p-4">
        {isLoading ? (
          <div className="space-y-3">
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
  return <NotificationsPage key={notificationScope()} />;
}

/** Inbox or Series: the two views of the page, as one segmented control. */
function ViewSwitcher({
  showSeries,
  unreadCount,
  onInbox,
  onSeries,
}: {
  showSeries: boolean;
  unreadCount: number | undefined;
  onInbox: () => void;
  onSeries: () => void;
}) {
  const t = useT();
  const option = (active: boolean) =>
    cn(
      "inline-flex items-center gap-2 rounded-lg px-3.5 py-1.5 text-sm font-medium transition-colors",
      active
        ? "bg-secondary text-foreground shadow-xs"
        : "text-muted-foreground hover:text-foreground",
    );
  return (
    <div
      role="group"
      aria-label={t("web.notifications.inbox.views")}
      className="bg-card inline-flex gap-0.5 rounded-xl p-[3px]"
    >
      <button
        type="button"
        className={option(!showSeries)}
        aria-pressed={!showSeries}
        onClick={onInbox}
      >
        {t("web.notifications.inbox.viewInbox")}
        {(unreadCount ?? 0) > 0 && (
          <span className="text-muted-foreground text-xs font-medium">
            {t("web.notifications.inbox.unreadBadge", {
              count: unreadCount ?? 0,
              count_text: String(unreadCount ?? 0),
            })}
          </span>
        )}
      </button>
      <button
        type="button"
        className={option(showSeries)}
        aria-pressed={showSeries}
        onClick={onSeries}
      >
        {t("web.notifications.inbox.filterSeries")}
      </button>
    </div>
  );
}

function NotificationsPage() {
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

  const switcher = openSeries ? (
    <ViewSwitcher
      showSeries={showSeries}
      unreadCount={unreadCount}
      onInbox={() => setSearchParams({}, { replace: true })}
      onSeries={openSeries}
    />
  ) : (
    <span />
  );

  return (
    <div className="mx-auto w-full max-w-5xl px-4 py-8 sm:px-6">
      <div className="mb-6 flex flex-wrap items-center gap-3">
        <h1 className="flex items-center gap-2.5 text-2xl font-semibold sm:text-3xl">
          <Bell className="h-6 w-6 sm:h-7 sm:w-7" />
          {t("web.notifications.inbox.title")}
        </h1>
        <div className="ms-auto flex items-center gap-2">
          {!showSeries && (unreadCount ?? 0) > 0 && (
            <Button
              variant="ghost"
              size="sm"
              className="text-muted-foreground"
              onClick={() => void markDisplayedRead()}
              disabled={markAllRead.isPending || markAllRead.isError || !cutoff}
            >
              <CheckCheck className="h-4 w-4" />
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

      {showSeries ? (
        <NotifiedSeriesList
          switcher={switcher}
          canMute={capability?.series?.mute === true}
          canBulkMute={capability?.series?.bulk_mute === true}
        />
      ) : (
        <NotificationInbox
          switcher={switcher}
          status={statusFilter}
          onStatusChange={setStatusFilter}
          list={list}
          unreadCount={unreadCount}
          markAllRead={markAllRead}
          canMuteSeries={capability?.series?.bulk_mute === true}
          onOpenSeries={openSeries}
        />
      )}
    </div>
  );
}
