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

type Reason = (typeof NOTIFICATION_REASONS)[number]["key"];

/** The preference that lets each reason notify, named as its switch is. */
const REASON_PREFERENCES: Record<Reason, { key: keyof NotificationPreferences; label: string }> = {
  favorite: { key: "notify_favorites", label: "Favorites" },
  watchlist: { key: "notify_watchlist", label: "Watchlist" },
  continue_watching: { key: "notify_continue_watching", label: "Continue Watching" },
  next_up: { key: "notify_next_up", label: "Next Up" },
};

/** What a new episode has to be for the series to notify. */
function triggerText(series: NotifiedSeries): string {
  const { favorite, watchlist, continue_watching: continueWatching } = series.active_reasons;
  const code = series.next_up_after && formatEpisodeCode(series.next_up_after);
  if (favorite || watchlist || continueWatching || !code) return "Any new episode";
  return `Any episode after ${code}`;
}

/** Why a listed series does not notify: the mute, or the switches that
 * silence it. */
function silencedText(series: NotifiedSeries, prefs: NotificationPreferences | undefined) {
  if (series.muted) return "Muted";
  if (prefs && !prefs.enabled) return "Notifications are off";
  const off = prefs
    ? NOTIFICATION_REASONS.filter(
        (reason) => series.reasons[reason.key] && !prefs[REASON_PREFERENCES[reason.key].key],
      ).map((reason) => REASON_PREFERENCES[reason.key].label)
    : [];
  if (off.length === 0) return "Won’t notify right now";
  const names = off.length === 1 ? off[0] : `${off.slice(0, -1).join(", ")} and ${off.at(-1)}`;
  return `${names} notifications are off`;
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
  const { item } = series;
  const muteLabel = series.muted ? "Unmute" : "Mute";
  return (
    <li className="hover:bg-muted/60 flex items-center gap-1 rounded-xl pr-2 transition-colors">
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
            {series.notifies ? triggerText(series) : silencedText(series, prefs)}
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
                  {reason.label}
                  {off && <span className="sr-only"> (off)</span>}
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
          aria-label={`${muteLabel} ${item.title}`}
          title={
            series.muted
              ? "Notify me about new episodes again"
              : "Stop notifying me about new episodes"
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
  { value: "all", label: "All reasons" },
  { value: "favorite", label: "Favorites" },
  { value: "watchlist", label: "Watchlist" },
  { value: "continue_watching", label: "Continue Watching" },
  { value: "next_up", label: "Next Up" },
] as const;

const SORTS = [
  { value: "title", label: "Title (A–Z)" },
  { value: "-title", label: "Title (Z–A)" },
  { value: "-latest_episode_added", label: "Latest episode" },
  { value: "-added_at", label: "Recently added" },
  { value: "-year", label: "Release year" },
] as const;

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
        <Search className="text-muted-foreground pointer-events-none absolute top-1/2 left-2.5 h-4 w-4 -translate-y-1/2" />
        <Input
          type="search"
          value={text}
          onChange={(event) => setText(event.target.value)}
          placeholder="Search series"
          aria-label="Search series"
          className="h-9 pl-8"
        />
      </div>
      <div className="flex gap-2">
        <Select
          value={series.reason}
          onValueChange={(value) => series.setReason(value as ReasonFilter)}
        >
          <SelectTrigger
            className="h-9 flex-1 sm:w-auto sm:min-w-[9.5rem] sm:flex-none"
            aria-label="Filter by reason"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {REASON_FILTERS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
        <Select value={series.sort} onValueChange={(value) => series.setSort(value as SortOption)}>
          <SelectTrigger
            className="h-9 flex-1 sm:w-auto sm:min-w-[9.5rem] sm:flex-none"
            aria-label="Sort"
          >
            <SelectValue />
          </SelectTrigger>
          <SelectContent>
            {SORTS.map((option) => (
              <SelectItem key={option.value} value={option.value}>
                {option.label}
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
        Could not load the series that notify you.{" "}
        <Button
          variant="outline"
          size="sm"
          onClick={() => void Promise.all([active.refetch(), inactive.refetch()])}
        >
          Retry
        </Button>
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
        No series match. Try another search or reason.
      </div>
    ) : (
      <div className="text-muted-foreground flex flex-col items-center gap-3 py-20 text-center">
        <BellOff className="h-10 w-10 opacity-50" />
        <div className="text-sm">No series notify you yet</div>
        <div className="max-w-sm text-xs">
          Series you favorite, add to your watchlist, or start watching show up here.
        </div>
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
              Won’t notify
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
          errorMessage="Couldn’t load more series."
          showSpinner
        />
      </div>
    );
  }
  return (
    <div>
      <p className="text-muted-foreground mb-3 px-3 text-sm">
        New episodes of these series notify this profile. Change which reasons count in{" "}
        <button
          type="button"
          className="text-foreground underline underline-offset-4"
          onClick={onOpenPreferences}
        >
          Preferences
        </button>
        {canMute ? ", or mute one series." : "."}
      </p>
      <SeriesToolbar series={series} />
      {body}
    </div>
  );
}
