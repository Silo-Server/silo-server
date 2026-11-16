import { useEffect, useRef, useState, type ComponentType, type ReactNode } from "react";
import { useSearchParams } from "react-router";
import { Bell, BellOff, Bookmark, Heart, Play, Search, SkipForward } from "lucide-react";
import { toast } from "sonner";
import type { NotificationPreferences } from "@/api/types";
import type { NotifiedSeries, NotifiedSeriesOptions } from "@/api/v2/notifications";
import ViewTransitionLink from "@/components/ViewTransitionLink";
import { buildItemHref } from "@/lib/mediaNavigation";
import { cn } from "@/lib/utils";
import { LoadMoreOnScroll } from "@/components/LoadMoreOnScroll";
import { Button } from "@/components/ui/button";
import { Checkbox } from "@/components/ui/checkbox";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Skeleton } from "@/components/ui/skeleton";
import { Switch } from "@/components/ui/switch";
import {
  formatEpisodeCode,
  useNotificationPreferences,
  useNotificationSeries,
  useSetManyNotificationSeriesMuted,
  useSetNotificationSeriesMuted,
} from "@/hooks/queries/notifications";
import { useDebounce } from "@/hooks/useDebounce";
import { NotificationPoster } from "./NotificationPoster";
import { NOTIFICATION_REASONS } from "./reasons";
import { SelectionBar } from "./SelectionBar";
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

const REASON_ICONS: Record<Reason, ComponentType<{ className?: string }>> = {
  favorite: Heart,
  watchlist: Bookmark,
  continue_watching: Play,
  next_up: SkipForward,
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

/** The table's columns: selection, series, what notifies, why, and the
 * switch. Narrow screens drop the middle two into the series cell. */
function rowGrid(selectable: boolean) {
  return cn(
    "grid items-center gap-x-3 px-3",
    selectable
      ? "grid-cols-[1.25rem_minmax(0,1fr)_auto] sm:grid-cols-[1.25rem_minmax(0,1fr)_13rem_auto] lg:grid-cols-[1.25rem_minmax(0,1fr)_13rem_15rem_auto]"
      : "grid-cols-[minmax(0,1fr)_auto] sm:grid-cols-[minmax(0,1fr)_13rem_auto] lg:grid-cols-[minmax(0,1fr)_13rem_15rem_auto]",
  );
}

function ReasonChips({ series }: { series: NotifiedSeries }) {
  const t = useT();
  return (
    <span className="flex min-w-0 flex-wrap gap-1">
      {NOTIFICATION_REASONS.filter((reason) => series.reasons[reason.key]).map((reason) => {
        const off = series.muted || !series.active_reasons[reason.key];
        const Icon = REASON_ICONS[reason.key];
        return (
          <span
            key={reason.key}
            className={cn(
              "text-muted-foreground inline-flex h-[22px] items-center gap-1 rounded-full px-2 text-[11.5px] font-medium whitespace-nowrap",
              off ? "border-border border line-through" : "bg-secondary",
            )}
          >
            <Icon className="h-3 w-3" />
            {t(reason.labelKey)}
            {off && <span className="sr-only"> {t("web.notifications.series.reasonOff")}</span>}
          </span>
        );
      })}
    </span>
  );
}

function NotifiedSeriesRow({
  series,
  prefs,
  mute,
  selection,
}: {
  series: NotifiedSeries;
  prefs: NotificationPreferences | undefined;
  mute: MuteAction | undefined;
  selection: { selected: boolean; toggle: () => void; disabled: boolean } | undefined;
}) {
  const t = useT();
  const { item } = series;
  const notifyText = series.notifies ? triggerText(t, series) : silencedText(t, series, prefs);
  const dim = !series.notifies;
  return (
    <li
      className={cn(
        rowGrid(selection !== undefined),
        "border-border/60 min-h-[3.5rem] border-b py-2 transition-colors",
        selection?.selected ? "bg-secondary/70" : "hover:bg-muted/40",
      )}
    >
      {selection && (
        <Checkbox
          checked={selection.selected}
          disabled={selection.disabled}
          onCheckedChange={selection.toggle}
          aria-label={t("web.notifications.series.selectSeries", { title: item.title })}
          className="size-[1.125rem] rounded-[5px]"
        />
      )}
      <ViewTransitionLink
        to={buildItemHref({ contentId: item.content_id })}
        className="flex min-w-0 items-center gap-3 rounded-md"
      >
        <NotificationPoster
          url={item.poster_url}
          thumbhash={item.poster_thumbhash}
          className={cn("h-[39px] w-[26px] rounded", dim && "opacity-40")}
        />
        <span className="min-w-0">
          <span
            className={cn("block truncate text-sm font-medium", dim && "text-muted-foreground")}
          >
            {item.title}
          </span>
          {/* Narrow screens show what notifies under the title. */}
          <span className="text-muted-foreground block truncate text-xs sm:hidden">
            {notifyText}
          </span>
        </span>
      </ViewTransitionLink>
      <span
        className={cn(
          "hidden truncate text-[13px] sm:block",
          dim ? "text-muted-foreground/80" : "text-muted-foreground",
        )}
      >
        {series.muted ? (
          <span className="inline-flex items-center gap-1.5">
            <BellOff className="h-3.5 w-3.5" />
            {notifyText}
          </span>
        ) : (
          notifyText
        )}
      </span>
      <span className="hidden lg:block">
        <ReasonChips series={series} />
      </span>
      <span className="flex justify-end">
        {mute && (
          <Switch
            checked={!series.muted}
            disabled={mute.pending === item.content_id}
            onCheckedChange={(on) => mute.set(series, !on)}
            aria-label={t("web.notifications.series.notifySwitch", { title: item.title })}
            title={
              series.muted
                ? t("web.notifications.series.unmuteHint")
                : t("web.notifications.series.muteHint")
            }
          />
        )}
      </span>
    </li>
  );
}

/** The tabs over the table: every series, one reason, or the muted ones. */
const SERIES_FILTERS = [
  { value: "all", labelKey: "web.notifications.inbox.filterAll", icon: undefined },
  { value: "next_up", labelKey: "web.notifications.preferences.nextUp", icon: SkipForward },
  { value: "favorite", labelKey: "web.notifications.preferences.favorites", icon: Heart },
  { value: "watchlist", labelKey: "web.notifications.preferences.watchlist", icon: Bookmark },
  {
    value: "continue_watching",
    labelKey: "web.notifications.preferences.continueWatching",
    icon: Play,
  },
  { value: "muted", labelKey: "web.notifications.series.muted", icon: BellOff },
] as const satisfies readonly {
  value: string;
  labelKey: MessageKey;
  icon: ComponentType<{ className?: string }> | undefined;
}[];

const SORTS = [
  { value: "title", labelKey: "web.notifications.series.sortTitleAsc" },
  { value: "-title", labelKey: "web.notifications.series.sortTitleDesc" },
  { value: "-latest_episode_added", labelKey: "web.notifications.series.sortLatestEpisode" },
  { value: "-added_at", labelKey: "web.notifications.series.sortRecentlyAdded" },
  { value: "-year", labelKey: "web.notifications.series.sortReleaseYear" },
] as const satisfies readonly { value: string; labelKey: MessageKey }[];

type SeriesFilter = (typeof SERIES_FILTERS)[number]["value"];
type SortOption = (typeof SORTS)[number]["value"];

function oneOf<T extends string>(
  values: readonly { value: T }[],
  value: string | null,
  fallback: T,
) {
  return values.find((option) => option.value === value)?.value ?? fallback;
}

/**
 * The Series view's search, filter tab, and sort, kept in the URL so they
 * survive opening a series and coming back.
 */
function useSeriesOptions(canFilterMuted: boolean) {
  const [params, setParams] = useSearchParams();
  const filters = canFilterMuted
    ? SERIES_FILTERS
    : SERIES_FILTERS.filter((option) => option.value !== "muted");
  const filter = oneOf(filters, params.get("reason"), "all");
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
    filters,
    filter,
    sort,
    q,
    setFilter: (value: SeriesFilter) => set("reason", value, "all"),
    setSort: (value: SortOption) => set("sort", value, "title"),
    setQ: (value: string) => set("q", value, ""),
    status: filter === "muted" ? ("muted" as const) : ("all" as const),
    options: {
      q: q.trim(),
      reason: filter === "all" || filter === "muted" ? undefined : filter,
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
    <>
      <div className="mt-5 flex flex-col gap-2 sm:flex-row sm:items-center">
        <div className="relative min-w-0 flex-1">
          <Search className="text-muted-foreground pointer-events-none absolute start-3 top-1/2 h-4 w-4 -translate-y-1/2" />
          <Input
            type="search"
            value={text}
            onChange={(event) => setText(event.target.value)}
            placeholder={t("web.notifications.series.search")}
            aria-label={t("web.notifications.series.search")}
            className="h-10 ps-9"
          />
        </div>
        <Select value={series.sort} onValueChange={(value) => series.setSort(value as SortOption)}>
          <SelectTrigger
            className="h-10 w-full sm:w-56"
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
      <div
        role="group"
        aria-label={t("web.notifications.series.filterByReason")}
        className="-mx-1 mt-3 flex gap-1 overflow-x-auto px-1 pb-1"
      >
        {series.filters.map((option) => {
          const Icon = option.icon;
          const active = series.filter === option.value;
          return (
            <Button
              key={option.value}
              variant={active ? "secondary" : "ghost"}
              size="sm"
              className={cn("shrink-0 rounded-full", !active && "text-muted-foreground")}
              aria-pressed={active}
              onClick={() => series.setFilter(option.value)}
            >
              {Icon && <Icon className="h-3.5 w-3.5" />}
              {t(option.labelKey)}
            </Button>
          );
        })}
      </div>
    </>
  );
}

/**
 * The Series view of the notifications page: every series whose new episodes
 * can notify this profile, or that it muted, as a table filtered by reason.
 * With canMute each row has a switch that mutes its series; with canBulkMute
 * rows can be selected and muted or unmuted together.
 */
export function NotifiedSeriesList({
  switcher,
  canMute = false,
  canBulkMute = false,
}: {
  /** The Inbox/Series control, placed above the toolbar. */
  switcher: ReactNode;
  canMute?: boolean;
  canBulkMute?: boolean;
}) {
  const t = useT();
  // The Muted tab needs status=muted, which comes with bulk mute.
  const series = useSeriesOptions(canBulkMute);
  const setMuted = useSetNotificationSeriesMuted();
  const setManyMuted = useSetManyNotificationSeriesMuted();
  const [selected, setSelected] = useState<ReadonlySet<string>>(new Set());
  const [selectingAll, setSelectingAll] = useState(false);
  const mute: MuteAction | undefined = canMute
    ? {
        pending: setMuted.isPending ? setMuted.variables?.seriesId : undefined,
        set: (entry, muted) =>
          setMuted.mutate({ seriesId: entry.item.content_id, title: entry.item.title, muted }),
      }
    : undefined;
  const list = useNotificationSeries(series.status, series.options);
  const { data: prefs } = useNotificationPreferences();
  const rows = list.data?.pages.flatMap((page) => page.series) ?? [];
  const filtered = series.options.q !== "" || series.filter !== "all";
  const selectedRows = rows.filter((row) => selected.has(row.item.content_id));
  // While a new search, tab, or sort loads, the previous rows stay on screen;
  // they must not be selected or acted on, since they no longer match it.
  const stale = list.isPlaceholderData;
  // Checkboxes and the bulk actions also wait for Select all matching, which
  // would overwrite the selection they change.
  const selectionLocked = stale || selectingAll;
  // Select all matching and a bulk mute or unmute never overlap, in either
  // order: the bulk action deselects what Select all matching would re-add.
  const bulkLocked = selectionLocked || setManyMuted.isPending;
  const allLoadedSelected = rows.length > 0 && selectedRows.length === rows.length;

  // Counts selection resets, so a Select all matching still loading when the
  // selection is cleared, or the search, tab, or sort changes, selects nothing.
  const selectionRun = useRef(0);
  const clearSelection = () => {
    selectionRun.current += 1;
    setSelected(new Set());
  };
  // A new search, tab, or sort starts a new selection.
  const optionsKey = JSON.stringify([series.status, series.options]);
  useEffect(clearSelection, [optionsKey]);
  /** Drops the series an action finished with, keeping any picked since. */
  const deselect = (ids: string[]) =>
    setSelected((set) => new Set([...set].filter((id) => !ids.includes(id))));

  const toggle = (id: string) =>
    setSelected((set) => {
      const next = new Set(set);
      if (!next.delete(id)) next.add(id);
      return next;
    });

  /** Loads every remaining page, then selects every series the view matches.
   * A failed page, or a selection reset meanwhile, selects nothing. */
  async function selectAllMatching() {
    const run = ++selectionRun.current;
    setSelectingAll(true);
    try {
      let { data, hasNextPage } = list;
      while (hasNextPage && selectionRun.current === run)
        ({ data, hasNextPage } = await list.fetchNextPage({ throwOnError: true }));
      if (selectionRun.current !== run) return;
      const ids = data?.pages.flatMap((page) => page.series.map((row) => row.item.content_id));
      if (ids) setSelected(new Set(ids));
    } catch {
      toast.error(t("web.notifications.series.loadMoreFailed"));
    } finally {
      setSelectingAll(false);
    }
  }

  function runBulk(muted: boolean) {
    const seriesIds = [...selected];
    setManyMuted.mutate({ seriesIds, muted }, { onSuccess: () => deselect(seriesIds) });
  }

  let body;
  if (list.isLoading) {
    body = (
      <div className="mt-2 space-y-2">
        {Array.from({ length: 8 }).map((_, index) => (
          <Skeleton key={index} className="h-12 w-full rounded-lg" />
        ))}
      </div>
    );
  } else if (list.isError && !list.isFetchNextPageError) {
    body = (
      <div role="alert" className="mt-4 px-3 text-sm">
        {t("web.notifications.series.loadFailed", {
          retry: (chunks) => (
            <Button variant="outline" size="sm" onClick={() => void list.refetch()}>
              {chunks}
            </Button>
          ),
        })}
      </div>
    );
  } else if (rows.length === 0 && !list.hasNextPage) {
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
    const headCell = "text-muted-foreground text-xs font-semibold";
    body = (
      <div className={stale ? "opacity-60" : undefined}>
        <div className={cn(rowGrid(canBulkMute), "border-border h-10 border-y")}>
          {canBulkMute && (
            <Checkbox
              checked={allLoadedSelected ? true : selectedRows.length > 0 ? "indeterminate" : false}
              disabled={selectionLocked}
              onCheckedChange={() =>
                setSelected(
                  allLoadedSelected ? new Set() : new Set(rows.map((row) => row.item.content_id)),
                )
              }
              aria-label={t("web.notifications.series.selectShown")}
              className="size-[1.125rem] rounded-[5px]"
            />
          )}
          <span className={headCell}>{t("web.notifications.series.columnSeries")}</span>
          <span className={cn(headCell, "hidden sm:block")}>
            {t("web.notifications.series.columnNotifiesFor")}
          </span>
          <span className={cn(headCell, "hidden lg:block")}>
            {t("web.notifications.series.columnReasons")}
          </span>
          <span className={cn(headCell, "text-end")}>
            {mute ? t("web.notifications.series.columnNotify") : null}
          </span>
        </div>
        <ul className="list-none">
          {rows.map((entry) => (
            <NotifiedSeriesRow
              key={entry.item.content_id}
              series={entry}
              prefs={prefs}
              mute={mute}
              selection={
                canBulkMute
                  ? {
                      selected: selected.has(entry.item.content_id),
                      toggle: () => toggle(entry.item.content_id),
                      disabled: selectionLocked,
                    }
                  : undefined
              }
            />
          ))}
        </ul>
        <LoadMoreOnScroll
          hasNextPage={list.hasNextPage}
          isFetchingNextPage={list.isFetchingNextPage}
          isError={list.isFetchNextPageError}
          onLoadMore={() => void list.fetchNextPage()}
          errorMessage={t("web.notifications.series.loadMoreFailed")}
          showSpinner
        />
      </div>
    );
  }

  const count = { count: selected.size, count_text: String(selected.size) };
  return (
    <div>
      {switcher}
      <SeriesToolbar series={series} />
      <div className="mt-3">{body}</div>
      {selected.size > 0 && (
        <SelectionBar
          label={t("web.notifications.selection.seriesLabel")}
          onClear={clearSelection}
          summary={
            <>
              <span className="font-semibold">{t("web.notifications.selection.count", count)}</span>
              {(list.hasNextPage || !allLoadedSelected) && (
                <Button
                  variant="link"
                  size="sm"
                  className="text-foreground h-auto px-1 underline"
                  disabled={bulkLocked}
                  onClick={() => void selectAllMatching()}
                >
                  {t("web.notifications.selection.selectAllMatching")}
                </Button>
              )}
            </>
          }
        >
          <Button variant="ghost" size="sm" disabled={bulkLocked} onClick={() => runBulk(true)}>
            <BellOff className="h-4 w-4" />
            {t("web.notifications.series.mute")}
          </Button>
          <Button variant="ghost" size="sm" disabled={bulkLocked} onClick={() => runBulk(false)}>
            <Bell className="h-4 w-4" />
            {t("web.notifications.series.unmute")}
          </Button>
        </SelectionBar>
      )}
    </div>
  );
}
