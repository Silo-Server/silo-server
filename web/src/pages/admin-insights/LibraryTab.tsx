import { useMemo } from "react";
import { Link, useSearchParams } from "react-router";

import {
  BarList,
  ChartEmptyState,
  StackedColumnChart,
  type BarListItem,
  type StackedColumnBucket,
} from "@/components/admin/dashboard/charts";
import { LibraryCoverageList } from "@/components/admin/insights/LibraryCoverageList";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminInsightsLibrary, type AdminInsightsLibrary } from "@/hooks/queries/admin/insights";
import { useT, type MessageKey } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

import { AddToDashboardButton } from "./InsightsDashboardPins";
import {
  buildInsightsWindowQueryParams,
  formatInsightsCount,
  formatRatePercentNumber,
  formatShortCalendarDate,
  insightsHrefWith,
  type InsightsFilterState,
} from "./insightsHelpers";
import { InsightKpiCard, InsightSectionError } from "./insightsShared";
import { formatInsightsTitle } from "./insightsTitleFormat";

type LabeledPlays = AdminInsightsLibrary["genres"][number];

const FIRST_PLAY_LABELS: Record<string, MessageKey> = {
  day: "web.admin.insights.library.firstPlay.day",
  week: "web.admin.insights.library.firstPlay.week",
  month: "web.admin.insights.library.firstPlay.month",
  later: "web.admin.insights.library.firstPlay.later",
  not_yet: "web.admin.insights.library.firstPlay.notYet",
};

/**
 * How the catalog is used: how much of each library was played, what was
 * never started, which genres, decades and ratings people watch, and how
 * soon new titles get their first play. A report only; Silo leaves managing
 * the files to other tools.
 */
export function LibraryTab({ filters }: { filters: InsightsFilterState }) {
  const t = useT();
  const [searchParams] = useSearchParams();
  const queryParams = useMemo(() => buildInsightsWindowQueryParams(filters), [filters]);
  const query = useAdminInsightsLibrary(queryParams);
  const data = query.data;

  if (query.isLoading) {
    return (
      <div className="space-y-5">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-24 rounded-2xl" />
          ))}
        </div>
        <Skeleton className="h-64 rounded-2xl" />
      </div>
    );
  }
  if (query.error || !data) {
    return (
      <InsightSectionError
        error={query.error}
        onRetry={() => {
          void query.refetch();
        }}
      />
    );
  }

  const { summary } = data;
  const playsCount = (plays: number) =>
    formatInsightsCount(t, "web.admin.insights.playCount", plays);
  const titlesCount = (titles: number) =>
    formatInsightsCount(t, "web.admin.insights.library.titleCount", titles);
  const playsItems = (rows: readonly LabeledPlays[]) =>
    rows.map<BarListItem>((row) => ({
      id: row.key,
      label: row.key === "unknown" ? t("web.admin.insights.library.unknown") : row.key,
      value: row.plays,
      secondary: titlesCount(row.titles),
    }));

  return (
    <div className="space-y-5">
      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <InsightKpiCard
          label={t("web.admin.insights.library.kpi.played")}
          value={t("web.admin.insights.percent", {
            value: formatRatePercentNumber(summary.played_share),
          })}
          subtext={t("web.admin.insights.library.kpi.playedDetail", {
            played: summary.played_titles.toLocaleString(formatLocale()),
            titles: summary.titles.toLocaleString(formatLocale()),
            titles_count: summary.titles,
          })}
          accent="success"
        />
        <InsightKpiCard
          label={t("web.admin.insights.library.kpi.neverStarted")}
          value={summary.never_started.toLocaleString(formatLocale())}
          subtext={
            data.oldest_history_at
              ? t("web.admin.insights.library.kpi.neverStartedSince", {
                  date: formatShortCalendarDate(data.oldest_history_at),
                })
              : undefined
          }
        />
        <InsightKpiCard
          label={t("web.admin.insights.library.kpi.added")}
          value={summary.added_titles.toLocaleString(formatLocale())}
          subtext={t("web.admin.insights.library.kpi.addedDetail", {
            played: summary.added_played.toLocaleString(formatLocale()),
          })}
          accent="info"
        />
        <InsightKpiCard
          label={t("web.admin.insights.library.kpi.firstPlay")}
          value={
            summary.median_days_to_first_play == null
              ? "—"
              : t("web.admin.insights.library.kpi.firstPlayDays", {
                  days: summary.median_days_to_first_play.toLocaleString(formatLocale()),
                })
          }
          subtext={t("web.admin.insights.library.kpi.firstPlayDetail")}
        />
      </div>

      <Card className="surface-panel rounded-2xl border-0">
        <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2 space-y-0">
          <div className="space-y-0.5">
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.library.coverage.title")}
            </CardTitle>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.insights.library.coverage.subtitle")}
            </p>
          </div>
          <AddToDashboardButton
            widgetId="insights-library-coverage"
            range={filters.range}
            chartName={t("web.admin.insights.library.coverage.title")}
          />
        </CardHeader>
        <CardContent>
          <LibraryCoverageList
            libraries={data.libraries}
            linkFor={(libraryId) => insightsHrefWith(searchParams, { library_id: libraryId })}
          />
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader className="space-y-0.5">
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.library.genres.title")}
            </CardTitle>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.insights.library.genres.subtitle")}
            </p>
          </CardHeader>
          <CardContent>
            <BarList
              items={playsItems(data.genres)}
              seriesIndex={0}
              formatValue={playsCount}
              emptyLabel={t("web.admin.insights.activity.volume.empty")}
            />
          </CardContent>
        </Card>
        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader>
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.library.ratings.title")}
            </CardTitle>
          </CardHeader>
          <CardContent>
            <BarList
              items={playsItems(data.content_ratings)}
              seriesIndex={1}
              formatValue={playsCount}
              emptyLabel={t("web.admin.insights.activity.volume.empty")}
            />
          </CardContent>
        </Card>
      </div>

      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <DecadesCard decades={data.decades} />
        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader className="space-y-0.5">
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.library.firstPlay.title")}
            </CardTitle>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.insights.library.firstPlay.subtitle")}
            </p>
          </CardHeader>
          <CardContent>
            <BarList
              items={data.time_to_first_play.map((bucket) => ({
                id: bucket.key,
                label: t(FIRST_PLAY_LABELS[bucket.key] ?? "web.admin.insights.library.unknown"),
                value: bucket.sessions,
              }))}
              seriesIndex={3}
              formatValue={titlesCount}
              emptyLabel={t("web.admin.insights.library.firstPlay.empty")}
            />
          </CardContent>
        </Card>
      </div>

      <Card className="surface-panel rounded-2xl border-0">
        <CardHeader className="space-y-0.5">
          <CardTitle className="text-sm font-bold">
            {t("web.admin.insights.library.unplayed.title")}
          </CardTitle>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.insights.library.unplayed.subtitle")}
          </p>
        </CardHeader>
        <CardContent>
          {data.recently_added_unplayed.length === 0 ? (
            <p className="text-muted-foreground py-6 text-center text-xs">
              {t("web.admin.insights.library.unplayed.empty")}
            </p>
          ) : (
            <ul className="divide-border/50 divide-y text-xs">
              {data.recently_added_unplayed.map((title) => (
                <li
                  key={title.content_id}
                  className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-0.5 py-2"
                >
                  <Link
                    to={`/item/${encodeURIComponent(title.content_id)}`}
                    className="hover:text-primary min-w-0 font-medium transition-colors"
                  >
                    {formatInsightsTitle(title, t)}
                  </Link>
                  <span className="text-muted-foreground tabular-nums">
                    {t("web.admin.insights.library.unplayed.added", {
                      library: title.library_name,
                      date: formatShortCalendarDate(title.added_at),
                    })}
                  </span>
                </li>
              ))}
            </ul>
          )}
        </CardContent>
      </Card>
    </div>
  );
}

/** Plays by release decade, oldest first; unknown years last. */
function DecadesCard({ decades }: { decades: readonly LabeledPlays[] }) {
  const t = useT();
  const labels = decades.map((decade) =>
    decade.key === "unknown"
      ? t("web.admin.insights.library.unknown")
      : t("web.admin.insights.library.decades.label", { year: decade.key }),
  );
  const buckets: StackedColumnBucket[] = decades.map((decade, index) => ({
    t: index,
    segments: [decade.plays],
  }));
  return (
    <Card className="surface-panel rounded-2xl border-0">
      <CardHeader>
        <CardTitle className="text-sm font-bold">
          {t("web.admin.insights.library.decades.title")}
        </CardTitle>
      </CardHeader>
      <CardContent>
        {buckets.length === 0 ? (
          <ChartEmptyState message={t("web.admin.insights.activity.volume.empty")} />
        ) : (
          <StackedColumnChart
            height={180}
            buckets={buckets}
            seriesLabels={[t("web.admin.insights.activity.volume.metricPlays")]}
            formatBucket={(index) => labels[index] ?? ""}
            ariaLabel={t("web.admin.insights.library.decades.title")}
          />
        )}
      </CardContent>
    </Card>
  );
}
