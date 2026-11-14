import { useEffect, useState } from "react";
import { useSearchParams } from "react-router";
import { Bell, BellOff, Search } from "lucide-react";
import type { NotificationPreferences } from "@/api/types";
import type { NotifiedSeries, NotifiedSeriesOptions } from "@/api/v2/notifications";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { buildItemHref } from "@/lib/mediaNavigation";
import { cn } from "@/lib/utils";
import { LoadMoreOnScroll } from "@/components/LoadMoreOnScroll";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import {
  formatEpisodeCode,
  useNotificationPreferences,
  useNotificationSeries,
  useSetNotificationSeriesMuted,
} from "@/hooks/queries/notifications";
import { useDebounce } from "@/hooks/useDebounce";
import { NotificationPoster } from "./NotificationPoster";
import { NOTIFICATION_REASONS } from "./reasons";
import { useT, type MessageKey, type Translate } from "@/i18n";

type Reason = (typeof NOTIFICATION_REASONS)[number]["key"];

/** The preference that lets each reason notify, named as its switch is. */
const REASON_PREFERENCES: Record<
  Reason,
  { key: keyof NotificationPreferences; labelKey: MessageKey }
> = {
  favorite: { key: "notify_favorites", labelKey: "web.notifications.preferences.favorites" },
  watchlist: { key: "notify_watchlist", labelKey: "web.notifications.preferences.watchlist" },
  continue_watching: {
    key: "notify_continue_watching",
    labelKey: "web.notifications.preferences.continueWatching",
  },
  next_up: { key: "notify_next_up", labelKey: "web.notifications.preferences.nextUp" },
};

/** What a new episode has to be for the series to notify. */
function triggerText(t: Translate, series: NotifiedSeries): string {
  const { favorite, watchlist, continue_watching: continueWatching } = series.active_reasons;
  const code = series.next_up_after && formatEpisodeCode(series.next_up_after);
  if (favorite || watchlist || continueWatching || !code) {
    return t("web.notifications.series.anyNewEpisode");
  }
  return t("web.notifications.series.anyEpisodeAfter", { code });
}

/** Why a listed series does not notify: the mute, or the switches that
 * silence it. */
function silencedText(
  t: Translate,
  series: NotifiedSeries,
  prefs: NotificationPreferences | undefined,
) {
  if (series.muted) return t("web.notifications.series.muted");
  if (prefs && !prefs.enabled) return t("web.notifications.series.notificationsOff");
  const off = prefs
    ? NOTIFICATION_REASONS.filter(
        (reason) => series.reasons[reason.key] && !prefs[REASON_PREFERENCES[reason.key].key],
      ).map((reason) => t(REASON_PREFERENCES[reason.key].labelKey))
    : [];
  if (off.length === 0) return t("web.notifications.series.wontNotifyNow");
  const names =
    off.length === 1
      ? off[0]
      : t("web.notifications.series.nameList", {
          names: off.slice(0, -1).join(", "),
          last: off.at(-1) ?? "",
        });
  return t("web.notifications.series.reasonsOff", { names });
}

/** Mutes or unmutes a row's series; absent when the server cannot. */
type MuteAction = {
  pending: string | undefined;
  set: (series: NotifiedSeries, muted: boolean) => void;
};

function NotifiedSeriesRow({
  series,
  prefs,
  mute,
}: {
  series: NotifiedSeries;
  prefs: NotificationPreferences | undefined;
  mute: MuteAction | undefined;
}) {
  const t = useT();
  const { item } = series;
  const muteLabel = series.muted
    ? t("web.notifications.series.unmute")
    : t("web.notifications.series.mute");
  return (
    <li className="hover:bg-muted/60 flex items-center gap-1 rounded-xl pe-2 transition-colors">
      <ViewTransitionLink
        to={buildItemHref({ contentId: item.content_id })}
        className="flex min-w-0 flex-1 items-start gap-3 rounded-xl px-3 py-3"
      >
        <div className={`shrink-0 ${series.notifies ? "" : "opacity-50"}`}>
          <NotificationPoster url={item.poster_url} thumbhash={item.poster_thumbhash} />
        </div>
        <div className="min-w-0 flex-1">
          <div
            className={`truncate text-sm font-medium ${series.notifies ? "" : "text-muted-foreground"}`}
          >
            {item.title}
          </div>
          <div className="text-muted-foreground mt-0.5 truncate text-sm">
            {series.notifies ? triggerText(t, series) : silencedText(t, series, prefs)}
          </div>
          <div className="mt-1.5 flex flex-wrap gap-1">
            {NOTIFICATION_REASONS.filter((reason) => series.reasons[reason.key]).map((reason) => {
              const off = series.muted || !series.active_reasons[reason.key];
              return (
                <span
                  key={reason.key}
                  className={cn(
                    "text-muted-foreground rounded-full px-2 py-0.5 text-[10px] font-medium",
                    off ? "border-border border line-through" : "bg-muted",
                  )}
                >
                  {t(reason.labelKey)}
                  {off && (
                    <span className="sr-only"> {t("web.notifications.series.reasonOff")}</span>
                  )}
                </span>
              );
            })}
          </div>
        </div>
      </ViewTransitionLink>
      {mute && (
        <Button
          variant="ghost"
          size="sm"
          className="text-muted-foreground shrink-0"
          disabled={mute.pending === item.content_id}
          aria-label={
            series.muted
              ? t("web.notifications.series.unmuteSeries", { title: item.title })
              : t("web.notifications.series.muteSeries", { title: item.title })
          }
          title={
            series.muted
              ? t("web.notifications.series.unmuteHint")
              : t("web.notifications.series.muteHint")
          }
          onClick={() => mute.set(series, !series.muted)}
        >
          {series.muted ? <Bell /> : <BellOff />}
          <span className="hidden sm:inline">{muteLabel}</span>
        </Button>
      )}
    </li>
  );
}

const REASON_FILTERS = [
  { value: "all", labelKey: "web.notifications.series.allReasons" },
  { value: "favorite", labelKey: "web.notifications.preferences.favorites" },
  { value: "watchlist", labelKey: "web.notifications.preferences.watchlist" },
  { value: "continue_watching", labelKey: "web.notifications.preferences.continueWatching" },
  { value: "next_up", labelKey: "web.notifications.preferences.nextUp" },
] as const satisfies readonly { value: string; labelKey: MessageKey }[];

const SORTS = [
  { value: "title", labelKey: "web.notifications.series.sortTitleAsc" },
  { value: "-title", labelKey: "web.notifications.series.sortTitleDesc" },
  { value: "-latest_episode_added", labelKey: "web.notifications.series.sortLatestEpisode" },
  { value: "-added_at", labelKey: "web.notifications.series.sortRecentlyAdded" },
  { value: "-year", labelKey: "web.notifications.series.sortReleaseYear" },
] as const satisfies readonly { value: string; labelKey: MessageKey }[];

type ReasonFilter = (typeof REASON_FILTERS)[number]["value"];
type SortOption = (typeof SORTS)[number]["value"];

function oneOf<T extends string>(
  values: readonly { value: T }[],
  value: string | null,
  fallback: T,
) {
  return values.find((option) => option.value === value)?.value ?? fallback;
}

/**
 * The Series view's search, reason filter, and sort, kept in the URL so they
 * survive opening a series and coming back.
 */
function useSeriesOptions() {
  const [params, setParams] = useSearchParams();
  const reason = oneOf(REASON_FILTERS, params.get("reason"), "all");
  const sort = oneOf(SORTS, params.get("sort"), "title");
  const q = params.get("q") ?? "";
  const set = (key: string, value: string, fallback: string) =>
    setParams(
      (current) => {
        const next = new URLSearchParams(current);
        if (value === fallback) next.delete(key);
        else next.set(key, value);
        return next;
      },
      { replace: true },
    );
  return {
    reason,
    sort,
    q,
    setReason: (value: ReasonFilter) => set("reason", value, "all"),
    setSort: (value: SortOption) => set("sort", value, "title"),
    setQ: (value: string) => set("q", value, ""),
    options: {
      q: q.trim(),
      reason: reason === "all" ? undefined : reason,
      sort,
    } satisfies NotifiedSeriesOptions,
  };
}

function SeriesToolbar({ series }: { series: ReturnType<typeof useSeriesOptions> }) {
  const t = useT();
  const [text, setText] = useState(series.q);
  const debounced = useDebounce(text, 300);
  const setQ = series.setQ;
  useEffect(() => {
    if (debounced !== series.q) setQ(debounced);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only a settled edit writes the URL; series.q follows from it
  }, [debounced]);
  useEffect(() => {
    // The URL changed from elsewhere (the Series button resets it); our own
    // writes match the settled text and leave typing in progress alone.
    if (series.q !== debounced) setText(series.q);
    // eslint-disable-next-line react-hooks/exhaustive-deps -- runs on URL changes only, not on each keystroke
  }, [series.q]);
  return (
    <div className="mb-3 flex flex-col gap-2 px-3 sm:flex-row sm:items-center">
      <div className="relative min-w-0 flex-1">
        <Search className="text-muted-foreground pointer-events-none absolute start-2.5 top-1/2 h-4 w-4 -translate-y-1/2" />
        <Input
          type="search"
          value={text}
          onChange={(event) => setText(event.target.value)}
          placeholder={t("web.notifications.series.search")}
          aria-label={t("web.notifications.series.search")}
          className="h-9 ps-8"
        />
      </div>
      <div className="flex gap-2">
        <Select
          value={series.reason}
          onValueChange={(value) => series.setReason(value as ReasonFilter)}
        >
          <SelectTrigger
            className="h-9 flex-1 sm:w-auto sm:min-w-[9.5rem] sm:flex-none"
            aria-label={t("web.notifications.series.filterByReason")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {REASON_FILTERS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {t(option.labelKey)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={series.sort} onValueChange={(value) => series.setSort(value as SortOption)}>
          <SelectTrigger
            className="h-9 flex-1 sm:w-auto sm:min-w-[9.5rem] sm:flex-none"
            aria-label={t("web.notifications.series.sort")}
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SORTS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {t(option.labelKey)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>
    </div>
  );
}

/**
 * The Series view of the notifications page: every series whose new episodes
 * notify this profile, then the ones that won't, because its preferences
 * silence them or it muted them. Both lists load as the viewer scrolls; the
 * second starts once the first is complete. With canMute, each row can mute
 * or unmute its series.
 */
export function NotifiedSeriesList({
  onOpenPreferences,
  canMute = false,
}: {
  onOpenPreferences: () => void;
  canMute?: boolean;
}) {
  const t = useT();
  const series = useSeriesOptions();
  const setMuted = useSetNotificationSeriesMuted();
  const mute: MuteAction | undefined = canMute
    ? {
        pending: setMuted.isPending ? setMuted.variables?.seriesId : undefined,
        set: (entry, muted) =>
          setMuted.mutate({ seriesId: entry.item.content_id, title: entry.item.title, muted }),
      }
    : undefined;
  const active = useNotificationSeries("active", series.options);
  const inactive = useNotificationSeries("inactive", series.options);
  const { data: prefs } = useNotificationPreferences();
  const notifying = active.data?.pages.flatMap((page) => page.series) ?? [];
  const silenced = inactive.data?.pages.flatMap((page) => page.series) ?? [];
  const filtered = series.options.q !== "" || series.options.reason !== undefined;
  // The scroll foot pages the list it sits under: notifying series first.
  const tail = active.hasNextPage ? active : inactive;

  let body;
  if (active.isLoading || inactive.isLoading) {
    body = (
      <div className="space-y-2">
        {Array.from({ length: 5 }).map((_, index) => (
          <Skeleton key={index} className="h-20 w-full rounded-xl" />
        ))}
      </div>
    );
  } else if (
    (active.isError && !active.isFetchNextPageError) ||
    (inactive.isError && !inactive.isFetchNextPageError)
  ) {
    body = (
      <div role="alert" className="px-3 text-sm">
        {t("web.notifications.series.loadFailed", {
          retry: (chunks) => (
            <Button
              variant="outline"
              size="sm"
              onClick={() => void Promise.all([active.refetch(), inactive.refetch()])}
            >
              {chunks}
            </Button>
          ),
        })}
      </div>
    );
  } else if (
    notifying.length === 0 &&
    silenced.length === 0 &&
    // A page can resolve to no visible series and still have more behind it.
    !active.hasNextPage &&
    !inactive.hasNextPage
  ) {
    body = filtered ? (
      <div className="text-muted-foreground py-16 text-center text-sm">
        {t("web.notifications.series.noMatch")}
      </div>
    ) : (
      <div className="text-muted-foreground flex flex-col items-center gap-3 py-20 text-center">
        <BellOff className="h-10 w-10 opacity-50" />
        <div className="text-sm">{t("web.notifications.series.empty")}</div>
        <div className="max-w-sm text-xs">{t("web.notifications.series.emptyHint")}</div>
      </div>
    );
  } else {
    body = (
      <div
        className={
          active.isPlaceholderData || inactive.isPlaceholderData ? "opacity-60" : undefined
        }
      >
        <ul className="list-none space-y-1">
          {notifying.map((entry) => (
            <NotifiedSeriesRow
              key={entry.item.content_id}
              series={entry}
              prefs={prefs}
              mute={mute}
            />
          ))}
        </ul>
        {!active.hasNextPage && silenced.length > 0 && (
          <section aria-labelledby="notified-series-silenced" className="mt-6">
            <h2
              id="notified-series-silenced"
              className="text-muted-foreground after:bg-border mb-1 flex items-center gap-2 px-3 text-xs font-semibold after:h-px after:flex-1"
            >
              {t("web.notifications.series.wontNotify")}
            </h2>
            <ul className="list-none space-y-1">
              {silenced.map((entry) => (
                <NotifiedSeriesRow
                  key={entry.item.content_id}
                  series={entry}
                  prefs={prefs}
                  mute={mute}
                />
              ))}
            </ul>
          </section>
        )}
        <LoadMoreOnScroll
          hasNextPage={tail.hasNextPage}
          isFetchingNextPage={tail.isFetchingNextPage}
          isError={tail.isFetchNextPageError}
          onLoadMore={() => void tail.fetchNextPage()}
          errorMessage={t("web.notifications.series.loadMoreFailed")}
          showSpinner
        />
      </div>
    );
  }
  return (
    <div>
      <p className="text-muted-foreground mb-3 px-3 text-sm">
        {t(canMute ? "web.notifications.series.introMute" : "web.notifications.series.intro", {
          prefs: (chunks) => (
            <button
              type="button"
              className="text-foreground underline underline-offset-4"
              onClick={onOpenPreferences}
            >
              {chunks}
            </button>
          ),
        })}
      </p>
      <SeriesToolbar series={series} />
      {body}
    </div>
  );
}
