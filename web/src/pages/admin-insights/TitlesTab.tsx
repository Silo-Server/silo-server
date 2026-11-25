import { useCallback, useMemo, useState } from "react";
import { useSearchParams } from "react-router";

import { BarList, BarListSkeleton, type BarListItem } from "@/components/admin/dashboard/charts";
import { formatWatchTime } from "@/components/admin/dashboard/format";
import { WidgetViewToggle } from "@/components/admin/dashboard/WidgetViewToggle";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAdminInsightsTitles, type AdminInsightsTitles } from "@/hooks/queries/admin/insights";
import { useT } from "@/i18n";

import {
  buildInsightsWindowQueryParams,
  formatInsightsCount,
  insightsHrefWith,
  type InsightsFilterState,
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
  const query = useAdminInsightsTitles(queryParams);
  const data = query.data;

  const formatMetric = useCallback(
    (value: number) => {
      if (sort === "watch_time") return formatWatchTime(value);
      if (sort === "viewers")
        return formatInsightsCount(t, "web.admin.insights.viewerCount", value);
      return formatInsightsCount(t, "web.admin.insights.playCount", value);
    },
    [sort, t],
  );

  // The secondary line carries the two metrics the ranking does not show.
  const secondaryFor = useCallback(
    (row: { plays: number; viewers: number; watch_seconds: number; episodes?: number }) => {
      const parts: string[] = [];
      if (row.episodes) {
        parts.push(formatInsightsCount(t, "web.admin.insights.episodeCount", row.episodes));
      }
      if (sort !== "plays") {
        parts.push(formatInsightsCount(t, "web.admin.insights.playCount", row.plays));
      }
      if (sort !== "viewers") {
        parts.push(formatInsightsCount(t, "web.admin.insights.viewerCount", row.viewers));
      }
      if (sort !== "watch_time") parts.push(formatWatchTime(row.watch_seconds));
      return parts.join(" · ");
    },
    [sort, t],
  );

  const metricOf = useCallback(
    (row: { plays: number; viewers: number; watch_seconds: number }) =>
      sort === "watch_time" ? row.watch_seconds : sort === "viewers" ? row.viewers : row.plays,
    [sort],
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
      <div className="flex justify-end">
        <div role="group" aria-label={t("web.admin.insights.titles.sortLabel")}>
          <WidgetViewToggle value={sort} options={sortOptions} onChange={setSort} />
        </div>
      </div>

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
    </div>
  );
}
