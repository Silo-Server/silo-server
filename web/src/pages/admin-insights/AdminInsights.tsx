import { useCallback, useMemo, useRef, useState } from "react";
import { Link, useSearchParams } from "react-router";
import { useQueryClient } from "@tanstack/react-query";
import { History, RefreshCw, RotateCcw } from "lucide-react";

import { PageHeader } from "@/components/page/PageHeader";
import { Button } from "@/components/ui/button";
import { Input } from "@/components/ui/input";
import {
  Select,
  SelectContent,
  SelectItem,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { markAdminInsightsRefresh } from "@/hooks/queries/admin/insights";
import { useAdminLibraries } from "@/hooks/queries/admin/libraries";
import { useAdminSessions } from "@/hooks/queries/admin/stats";
import { useAdminUsers } from "@/hooks/queries/admin/users";
import { adminKeys } from "@/hooks/queries/keys";
import { edgeMask, useScrollStrip } from "@/hooks/useScrollStrip";
import { useT } from "@/i18n";
import { cn } from "@/lib/utils";
import type { MessageKey } from "@/locales/keys";

import { BandwidthTab } from "./BandwidthTab";
import { DevicesTab } from "./DevicesTab";
import { InsightsDashboardPinsProvider } from "./InsightsDashboardPins";
import { LibraryTab } from "./LibraryTab";
import { LiveActivityTab } from "./LiveActivityTab";
import { PeopleTab } from "./PeopleTab";
import { PlaybackTab } from "./PlaybackTab";
import { PersonDetailSheet } from "./PersonDetailSheet";
import { TitleDetailSheet } from "./TitleDetailSheet";
import { TitlesTab } from "./TitlesTab";
import {
  DEFAULT_INSIGHTS_TAB,
  INSIGHTS_DETAIL_PARAMS,
  INSIGHTS_RANGES,
  defaultCustomDates,
  detectBrowserTimezone,
  parseInsightsDetailState,
  parseInsightsUrlState,
  type InsightsRange,
  type InsightsTab,
} from "./insightsHelpers";

const ALL_VALUE = "all";
const RANGE_SEPARATOR = "–";
const REFRESH_SPINNER_MIN_VISIBLE_MS = 600;

const RANGE_LABELS: Record<InsightsRange, MessageKey> = {
  "7d": "web.admin.insights.range.7d",
  "30d": "web.admin.insights.range.30d",
  "90d": "web.admin.insights.range.90d",
  "1y": "web.admin.insights.range.1y",
  all: "web.admin.insights.range.all",
  custom: "web.admin.insights.range.custom",
};

export default function AdminInsights() {
  const t = useT();
  const queryClient = useQueryClient();
  const [searchParams, setSearchParams] = useSearchParams();
  const browserTz = useMemo(() => detectBrowserTimezone(), []);
  const filters = useMemo(
    () => parseInsightsUrlState(searchParams, browserTz),
    [searchParams, browserTz],
  );
  const detail = useMemo(() => parseInsightsDetailState(searchParams), [searchParams]);

  const { data: libraries = [] } = useAdminLibraries();
  const { data: users = [] } = useAdminUsers();
  const { refetch: refetchSessions } = useAdminSessions();

  const [isRefreshing, setIsRefreshing] = useState(false);
  const refreshStartRef = useRef<number | null>(null);

  const updateParams = useCallback(
    (mutate: (next: URLSearchParams) => void) => {
      const next = new URLSearchParams(searchParams);
      mutate(next);
      setSearchParams(next, { replace: true });
    },
    [searchParams, setSearchParams],
  );

  const handleTabChange = useCallback(
    (value: string) => {
      updateParams((next) => {
        if (value === DEFAULT_INSIGHTS_TAB) {
          next.delete("tab");
        } else {
          next.set("tab", value);
        }
      });
    },
    [updateParams],
  );

  const handleRangeChange = useCallback(
    (nextRange: InsightsRange) => {
      updateParams((next) => {
        if (nextRange === "30d") {
          next.delete("range");
        } else {
          next.set("range", nextRange);
        }
        if (nextRange === "custom") {
          const defaults = defaultCustomDates();
          if (!next.get("from")) next.set("from", defaults.fromDate);
          if (!next.get("to")) next.set("to", defaults.toDate);
        } else {
          next.delete("from");
          next.delete("to");
        }
      });
    },
    [updateParams],
  );

  const closeDetail = useCallback(() => {
    updateParams((next) => {
      for (const key of INSIGHTS_DETAIL_PARAMS) next.delete(key);
    });
  }, [updateParams]);

  // A title opened from a person's panel keeps the person in the URL; closing
  // it returns to that person.
  const closeTitle = useCallback(() => {
    updateParams((next) => {
      next.delete("title");
    });
  }, [updateParams]);

  const handleReset = useCallback(() => {
    const next = new URLSearchParams();
    if (filters.tab !== DEFAULT_INSIGHTS_TAB) {
      next.set("tab", filters.tab);
    }
    setSearchParams(next, { replace: true });
  }, [filters.tab, setSearchParams]);

  const handleRefresh = useCallback(async () => {
    refreshStartRef.current = Date.now();
    setIsRefreshing(true);
    try {
      if (filters.tab === "activity") {
        await refetchSessions();
      } else {
        markAdminInsightsRefresh();
        await queryClient.invalidateQueries({ queryKey: adminKeys.insightsRoot() });
      }
    } finally {
      const started = refreshStartRef.current;
      if (started !== null) {
        const remaining = REFRESH_SPINNER_MIN_VISIBLE_MS - (Date.now() - started);
        if (remaining > 0) {
          await new Promise<void>((resolve) => {
            window.setTimeout(resolve, remaining);
          });
        }
      }
      refreshStartRef.current = null;
      setIsRefreshing(false);
    }
  }, [filters.tab, queryClient, refetchSessions]);

  const timezoneOptions = useMemo(() => {
    const opts = new Set<string>([browserTz, "UTC"]);
    if (filters.tz) opts.add(filters.tz);
    return Array.from(opts);
  }, [browserTz, filters.tz]);

  const activeTab: InsightsTab = filters.tab;
  // Live streams have no date window or filters of the page's own; the tab
  // brings its own search and filters.
  const isLiveTab = activeTab === "activity";
  const hasNonDefaultFilters =
    !isLiveTab &&
    (filters.range !== "30d" ||
      filters.tz !== browserTz ||
      filters.libraryId !== undefined ||
      filters.userId !== undefined);

  // Seven tabs don't fit a phone: the strip scrolls sideways there and keeps
  // the active tab in view.
  const {
    ref: tabStrip,
    edges: tabEdges,
    measure: measureTabStrip,
  } = useScrollStrip<HTMLDivElement>(`[role="tab"][data-state="active"]`, activeTab);
  const showLibraryFilter = activeTab !== "bandwidth";
  const showAccountFilter =
    activeTab === "playback" ||
    activeTab === "titles" ||
    activeTab === "library" ||
    activeTab === "devices";
  const showTimezoneFilter = activeTab !== "bandwidth";
  // The detail panels honor only the filters this tab shows: an account or
  // library chosen on another tab stays in the URL but is hidden here.
  const detailFilters = useMemo(
    () => ({
      ...filters,
      libraryId: showLibraryFilter ? filters.libraryId : undefined,
      userId: showAccountFilter ? filters.userId : undefined,
    }),
    [filters, showAccountFilter, showLibraryFilter],
  );

  return (
    <InsightsDashboardPinsProvider>
      <div className="space-y-6 pb-4 sm:pb-6">
        <PageHeader
          title={t("web.admin.insights.title")}
          subtitle={t("web.admin.insights.subtitle")}
          actions={
            <div className="flex flex-wrap items-center gap-2">
              <Button variant="ghost" size="sm" asChild>
                <Link to="/admin/history">
                  <History className="h-3.5 w-3.5" />
                  {t("web.admin.insights.action.history")}
                </Link>
              </Button>
              {hasNonDefaultFilters ? (
                <Button variant="ghost" size="sm" onClick={handleReset}>
                  <RotateCcw className="h-3.5 w-3.5" />
                  {t("web.admin.insights.filter.reset")}
                </Button>
              ) : null}
              <Button
                variant="outline"
                size="sm"
                className="min-w-[7.5rem] justify-center"
                onClick={() => {
                  void handleRefresh();
                }}
                disabled={isRefreshing}
                aria-busy={isRefreshing}
              >
                <RefreshCw className={cn("h-3.5 w-3.5", isRefreshing && "animate-spin")} />
                {isRefreshing
                  ? t("web.admin.insights.filter.refreshing")
                  : t("web.admin.insights.filter.refresh")}
              </Button>
            </div>
          }
        />

        <Tabs value={activeTab} onValueChange={handleTabChange} className="space-y-5">
          <div
            ref={tabStrip}
            onScroll={measureTabStrip}
            className={cn(
              "border-border/70 -mx-1 overflow-x-auto border-b px-1 max-lg:[scrollbar-width:none] max-lg:[&::-webkit-scrollbar]:hidden",
              edgeMask(tabEdges),
            )}
          >
            <TabsList
              variant="line"
              className="h-auto w-max min-w-full flex-nowrap justify-start gap-2 pb-1"
            >
              <TabsTrigger value="activity" className="flex-none">
                {t("web.admin.activity.page.title")}
              </TabsTrigger>
              <TabsTrigger value="playback" className="flex-none">
                {t("web.admin.insights.tabs.activity")}
              </TabsTrigger>
              <TabsTrigger value="titles" className="flex-none">
                {t("web.admin.insights.tabs.titles")}
              </TabsTrigger>
              <TabsTrigger value="library" className="flex-none">
                {t("web.admin.insights.tabs.library")}
              </TabsTrigger>
              <TabsTrigger value="people" className="flex-none">
                {t("web.admin.insights.tabs.people")}
              </TabsTrigger>
              <TabsTrigger value="devices" className="flex-none">
                {t("web.admin.insights.tabs.devices")}
              </TabsTrigger>
              <TabsTrigger value="bandwidth" className="flex-none">
                {t("web.admin.insights.tabs.bandwidth")}
              </TabsTrigger>
            </TabsList>
          </div>

          {/* Shared filter toolbar */}
          <div
            className={cn(
              "surface-panel flex flex-wrap items-center gap-2.5 rounded-2xl border-0 p-3",
              isLiveTab && "hidden",
            )}
          >
            <div
              role="group"
              aria-label={t("web.admin.insights.filter.rangeLabel")}
              className="border-border/60 bg-muted/25 inline-flex flex-wrap items-center gap-0.5 rounded-lg border p-0.5"
            >
              {INSIGHTS_RANGES.map((preset) => {
                const active = filters.range === preset;
                return (
                  <button
                    key={preset}
                    type="button"
                    aria-pressed={active}
                    onClick={() => handleRangeChange(preset)}
                    className={cn(
                      "cursor-pointer rounded-md px-2.5 py-1.5 text-xs font-medium transition-colors",
                      "focus-visible:ring-ring focus-visible:ring-2 focus-visible:outline-none",
                      active
                        ? "bg-primary/15 text-foreground shadow-xs"
                        : "text-muted-foreground hover:text-foreground hover:bg-muted/60",
                    )}
                  >
                    {t(RANGE_LABELS[preset])}
                  </button>
                );
              })}
            </div>

            {filters.range === "custom" ? (
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  type="date"
                  aria-label={t("web.admin.insights.filter.fromDate")}
                  value={filters.fromDate}
                  max={filters.toDate}
                  onChange={(e) => {
                    const val = e.target.value;
                    updateParams((next) => {
                      next.set("range", "custom");
                      if (val) next.set("from", val);
                    });
                  }}
                  className="h-8 w-[145px] text-xs"
                />
                <span className="text-muted-foreground text-xs" aria-hidden="true">
                  {RANGE_SEPARATOR}
                </span>
                <Input
                  type="date"
                  aria-label={t("web.admin.insights.filter.toDate")}
                  value={filters.toDate}
                  min={filters.fromDate}
                  onChange={(e) => {
                    const val = e.target.value;
                    updateParams((next) => {
                      next.set("range", "custom");
                      if (val) next.set("to", val);
                    });
                  }}
                  className="h-8 w-[145px] text-xs"
                />
              </div>
            ) : null}

            {showLibraryFilter || showAccountFilter || showTimezoneFilter ? (
              <div className="ms-auto flex flex-wrap items-center gap-2">
                {showLibraryFilter ? (
                  <Select
                    value={filters.libraryId ? String(filters.libraryId) : ALL_VALUE}
                    onValueChange={(value) => {
                      updateParams((next) => {
                        if (value === ALL_VALUE) {
                          next.delete("library_id");
                        } else {
                          next.set("library_id", value);
                        }
                      });
                    }}
                  >
                    <SelectTrigger className="h-8 max-w-[14rem] min-w-[8.5rem] text-xs">
                      <SelectValue placeholder={t("web.admin.insights.filter.allLibraries")} />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ALL_VALUE}>
                        {t("web.admin.insights.filter.allLibraries")}
                      </SelectItem>
                      {libraries.map((lib) => (
                        <SelectItem key={lib.id} value={String(lib.id)}>
                          {lib.name}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                ) : null}

                {showAccountFilter ? (
                  <Select
                    value={filters.userId ? String(filters.userId) : ALL_VALUE}
                    onValueChange={(value) => {
                      updateParams((next) => {
                        if (value === ALL_VALUE) {
                          next.delete("user_id");
                        } else {
                          next.set("user_id", value);
                        }
                      });
                    }}
                  >
                    <SelectTrigger className="h-8 max-w-[14rem] min-w-[8.5rem] text-xs">
                      <SelectValue placeholder={t("web.admin.insights.filter.allAccounts")} />
                    </SelectTrigger>
                    <SelectContent>
                      <SelectItem value={ALL_VALUE}>
                        {t("web.admin.insights.filter.allAccounts")}
                      </SelectItem>
                      {users.map((u) => (
                        <SelectItem key={u.id} value={String(u.id)}>
                          {u.username}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                ) : null}

                {showTimezoneFilter ? (
                  <Select
                    value={filters.tz}
                    onValueChange={(value) => {
                      updateParams((next) => {
                        if (value === browserTz) {
                          next.delete("tz");
                        } else {
                          next.set("tz", value);
                        }
                      });
                    }}
                  >
                    <SelectTrigger
                      aria-label={t("web.admin.insights.filter.timezone")}
                      className="h-8 max-w-full min-w-[7.5rem] text-xs"
                    >
                      <SelectValue />
                    </SelectTrigger>
                    <SelectContent>
                      {timezoneOptions.map((tzOpt) => (
                        <SelectItem key={tzOpt} value={tzOpt}>
                          {tzOpt === "UTC"
                            ? t("web.admin.insights.filter.timezoneUtc")
                            : tzOpt === browserTz
                              ? t("web.admin.insights.filter.timezoneBrowser", { tz: tzOpt })
                              : tzOpt}
                        </SelectItem>
                      ))}
                    </SelectContent>
                  </Select>
                ) : null}
              </div>
            ) : null}
          </div>

          <TabsContent value="activity" className="mt-0 space-y-5">
            {activeTab === "activity" ? <LiveActivityTab /> : null}
          </TabsContent>
          <TabsContent value="playback" className="mt-0 space-y-5">
            {activeTab === "playback" ? <PlaybackTab filters={filters} /> : null}
          </TabsContent>
          <TabsContent value="titles" className="mt-0 space-y-5">
            {activeTab === "titles" ? <TitlesTab filters={filters} /> : null}
          </TabsContent>
          <TabsContent value="library" className="mt-0 space-y-5">
            {activeTab === "library" ? <LibraryTab filters={filters} /> : null}
          </TabsContent>
          <TabsContent value="people" className="mt-0 space-y-5">
            {activeTab === "people" ? <PeopleTab filters={filters} /> : null}
          </TabsContent>
          <TabsContent value="devices" className="mt-0 space-y-5">
            {activeTab === "devices" ? <DevicesTab filters={filters} /> : null}
          </TabsContent>
          <TabsContent value="bandwidth" className="mt-0 space-y-5">
            {activeTab === "bandwidth" ? <BandwidthTab filters={filters} /> : null}
          </TabsContent>
        </Tabs>

        <TitleDetailSheet
          titleId={detail.titleId}
          person={detail.person}
          filters={detailFilters}
          onClose={detail.person ? closeTitle : closeDetail}
        />
        <PersonDetailSheet
          person={detail.titleId ? undefined : detail.person}
          filters={detailFilters}
          onClose={closeDetail}
        />
      </div>
    </InsightsDashboardPinsProvider>
  );
}
