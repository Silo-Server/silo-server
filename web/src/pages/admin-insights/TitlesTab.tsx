import { useCallback, useMemo, useState } from "react";
import { useSearchParams } from "react-router";

import { BarList, BarListSkeleton, type BarListItem } from "@/components/admin/dashboard/charts";
import { formatWatchTime } from "@/components/admin/dashboard/format";
import { WidgetViewToggle } from "@/components/admin/dashboard/WidgetViewToggle";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAdminInsightsTitles, type AdminInsightsTitles } from "@/hooks/queries/admin/insights";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

import { AddToDashboardButton } from "./InsightsDashboardPins";
import {
  buildInsightsWindowQueryParams,
  formatInsightsCount,
  insightsHrefWith,
  type InsightsFilterState,
  insightRefreshProps,
} from "./insightsHelpers";
import { InsightSectionError } from "./insightsShared";
import { formatInsightsTitle } from "./insightsTitleFormat";

type TitlesSort = "plays" | "watch_time" | "viewers";
type TitleRow = AdminInsightsTitles["movies"][number];
type LibraryRow = AdminInsightsTitles["libraries"][number];

const TITLES_LIMIT = 10;

export function TitlesTab({ filters }: { filters: InsightsFilterState }) {
  const t = useT();
  const [searchParams] = useSearchParams();
  const [sort, setSort] = useState<TitlesSort>("plays");
  const queryParams = useMemo(
    () => ({ ...buildInsightsWindowQueryParams(filters), sort, limit: TITLES_LIMIT }),
    [filters, sort],
  );
  const query = useAdminInsightsTitles(queryParams, { keepPrevious: true });
  const data = query.data;
  // While a new sort loads, the previous ranking stays on screen; format it by
  // the sort it was ranked by, not the one the toggle now shows.
  const shownSort = data?.sort ?? sort;

  const formatMetric = useCallback(
    (value: number) => {
      if (shownSort === "watch_time") return formatWatchTime(value);
      if (shownSort === "viewers")
        return formatInsightsCount(t, "web.admin.insights.viewerCount", value);
      return formatInsightsCount(t, "web.admin.insights.playCount", value);
    },
    [shownSort, t],
  );

  // The secondary line carries the two metrics the ranking does not show.
  const secondaryFor = useCallback(
    (row: { plays: number; viewers: number; watch_seconds: number; episodes?: number }) => {
      const parts: string[] = [];
      if (row.episodes) {
        parts.push(formatInsightsCount(t, "web.admin.insights.episodeCount", row.episodes));
      }
      if (shownSort !== "plays") {
        parts.push(formatInsightsCount(t, "web.admin.insights.playCount", row.plays));
      }
      if (shownSort !== "viewers") {
        parts.push(formatInsightsCount(t, "web.admin.insights.viewerCount", row.viewers));
      }
      if (shownSort !== "watch_time") parts.push(formatWatchTime(row.watch_seconds));
      return parts.join(" · ");
    },
    [shownSort, t],
  );

  const metricOf = useCallback(
    (row: { plays: number; viewers: number; watch_seconds: number }) =>
      shownSort === "watch_time"
        ? row.watch_seconds
        : shownSort === "viewers"
          ? row.viewers
          : row.plays,
    [shownSort],
  );

  const titleItems = useCallback(
    (rows: readonly TitleRow[]): BarListItem[] =>
      rows.map((row) => ({
        id: row.content_id,
        label: formatInsightsTitle(row, t),
        value: metricOf(row),
        secondary: secondaryFor(row),
        to: insightsHrefWith(searchParams, { title: row.content_id, person: null, profile: null }),
      })),
    [metricOf, searchParams, secondaryFor, t],
  );

  const libraryItems = useMemo<BarListItem[]>(
    () =>
      (data?.libraries ?? []).map((row: LibraryRow) => ({
        id: row.library_id,
        label: row.name,
        value: metricOf(row),
        secondary: secondaryFor(row),
        // A library row narrows the whole page to that library.
        to: insightsHrefWith(searchParams, { library_id: row.library_id }),
      })),
    [data?.libraries, metricOf, searchParams, secondaryFor],
  );

  // Least finished ranks by the share of plays left unfinished, whatever the
  // sort, so a longer bar is a title more people gave up on.
  const leastFinishedItems = useMemo<BarListItem[]>(
    () =>
      (data?.least_finished ?? []).map((row) => ({
        id: row.content_id,
        label: formatInsightsTitle(row, t),
        value: row.plays > 0 ? Math.round((1 - row.completed_plays / row.plays) * 100) : 0,
        secondary: t("web.admin.insights.activity.kpi.completionDetail", {
          completed: row.completed_plays.toLocaleString(formatLocale()),
          total: row.plays.toLocaleString(formatLocale()),
        }),
        to: insightsHrefWith(searchParams, { title: row.content_id, person: null, profile: null }),
      })),
    [data?.least_finished, searchParams, t],
  );

  if (query.error) {
    return (
      <InsightSectionError
        error={query.error}
        onRetry={() => {
          void query.refetch();
        }}
      />
    );
  }

  const sortOptions = (
    [
      ["plays", t("web.admin.insights.titles.sort.plays")],
      ["watch_time", t("web.admin.insights.titles.sort.watchTime")],
      ["viewers", t("web.admin.insights.titles.sort.viewers")],
    ] as const
  ).map(([value, label]) => ({ value, label, ariaLabel: label }));

  const renderList = (items: BarListItem[] | undefined, seriesIndex: number, empty: string) =>
    !data || items === undefined ? (
      <BarListSkeleton />
    ) : (
      <BarList
        items={items}
        seriesIndex={seriesIndex}
        formatValue={formatMetric}
        emptyLabel={empty}
      />
    );

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-center justify-end gap-2">
        <AddToDashboardButton
          widgetId="top-titles"
          range={filters.range}
          chartName={t("web.admin.dashboard.widgetCatalog.topTitles.title")}
        />
        <div role="group" aria-label={t("web.admin.insights.titles.sortLabel")}>
          <WidgetViewToggle value={sort} options={sortOptions} onChange={setSort} />
        </div>
      </div>

      <div {...insightRefreshProps(query.isPlaceholderData, "space-y-5")}>
        <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
          <Card className="surface-panel rounded-2xl border-0">
            <CardHeader>
              <CardTitle className="text-sm font-bold">
                {t("web.admin.insights.titles.movies.title")}
              </CardTitle>
            </CardHeader>
            <CardContent>
              {renderList(
                data ? titleItems(data.movies) : undefined,
                0,
                t("web.admin.insights.titles.movies.empty"),
              )}
            </CardContent>
          </Card>
          <Card className="surface-panel rounded-2xl border-0">
            <CardHeader>
              <CardTitle className="text-sm font-bold">
                {t("web.admin.insights.titles.shows.title")}
              </CardTitle>
            </CardHeader>
            <CardContent>
              {renderList(
                data ? titleItems(data.shows) : undefined,
                1,
                t("web.admin.insights.titles.shows.empty"),
              )}
            </CardContent>
          </Card>
        </div>

        <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
          <Card className="surface-panel rounded-2xl border-0">
            <CardHeader>
              <CardTitle className="text-sm font-bold">
                {t("web.admin.insights.titles.libraries.title")}
              </CardTitle>
            </CardHeader>
            <CardContent>
              {renderList(
                data ? libraryItems : undefined,
                2,
                t("web.admin.insights.titles.libraries.empty"),
              )}
            </CardContent>
          </Card>
          <Card className="surface-panel rounded-2xl border-0">
            <CardHeader className="space-y-0.5">
              <CardTitle className="text-sm font-bold">
                {t("web.admin.insights.titles.leastFinished.title")}
              </CardTitle>
              <p className="text-muted-foreground text-xs">
                {t("web.admin.insights.titles.leastFinished.subtitle")}
              </p>
            </CardHeader>
            <CardContent>
              {!data ? (
                <BarListSkeleton />
              ) : (
                <BarList
                  items={leastFinishedItems}
                  seriesIndex={3}
                  formatValue={(value) =>
                    t("web.admin.insights.titles.leastFinished.unfinishedShare", {
                      value: value.toLocaleString(formatLocale()),
                    })
                  }
                  emptyLabel={t("web.admin.insights.titles.leastFinished.empty")}
                />
              )}
            </CardContent>
          </Card>
        </div>
      </div>
    </div>
  );
}
