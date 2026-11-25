import { useMemo, useState } from "react";

import {
  BarList,
  ChartEmptyState,
  LineChart,
  type BarListItem,
  chartSeriesColor,
  type LineChartOverlay,
  type LineChartPoint,
  type LineChartStack,
} from "@/components/admin/dashboard/charts";
import { formatWatchTime } from "@/components/admin/dashboard/format";
import { WidgetViewToggle } from "@/components/admin/dashboard/WidgetViewToggle";
import { NEUTRAL_SERIES_COLOR } from "@/components/admin/dashboard/widgets/concurrentStreamsSeries";
import { ActivePeopleChart } from "@/components/admin/insights/ActivePeopleChart";
import { PlaysOverTimeChart } from "@/components/admin/insights/PlaysOverTimeChart";
import { ViewingHeatmap } from "@/components/admin/insights/ViewingHeatmap";
import { ViewingTimeChart } from "@/components/admin/insights/ViewingTimeChart";
import type { ViewingTimeMetric } from "@/components/admin/insights/viewingTime";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  useAdminInsightsActivity,
  type AdminInsightsActivity,
} from "@/hooks/queries/admin/insights";
import { useT, type MessageKey } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

import { AddToDashboardButton } from "./InsightsDashboardPins";
import {
  buildDailyUtcGrid,
  buildInsightsWindowQueryParams,
  formatInsightsCount,
  formatRatePercentNumber,
  formatShortCalendarDate,
  formatShortTimestamp,
  type InsightsFilterState,
  insightRefreshProps,
} from "./insightsHelpers";
import { InsightKpiCard, InsightNoticeBanner, InsightSectionError } from "./insightsShared";

type VolumeMetric = "plays" | "watch_time";
type VolumeSplit = "method" | "media";
type PeakView = "lines" | "stacked" | "total";

export function PlaybackTab({ filters }: { filters: InsightsFilterState }) {
  const t = useT();
  const [volumeMetric, setVolumeMetric] = useState<VolumeMetric>("plays");
  const [volumeSplit, setVolumeSplit] = useState<VolumeSplit>("method");
  const [peakView, setPeakView] = useState<PeakView>("lines");
  const queryParams = useMemo(() => buildInsightsWindowQueryParams(filters), [filters]);
  const query = useAdminInsightsActivity(queryParams, { keepPrevious: true });
  const data = query.data;

  const peakSeries = useMemo(() => {
    const raw = data?.peak_streams ?? [];
    if (raw.length === 0) {
      return {
        points: [] as LineChartPoint[],
        overlays: undefined as LineChartOverlay[] | undefined,
        stacks: undefined as LineChartStack[] | undefined,
        edgeLabels: undefined as { start: string; end: string } | undefined,
      };
    }
    const byDate = new Map(raw.map((pt) => [pt.date, pt]));
    const first = raw[0]?.date ?? "";
    const last = raw[raw.length - 1]?.date ?? "";
    const grid = buildDailyUtcGrid(first, last);
    const makePoints = (pick: (pt: (typeof raw)[number]) => number): LineChartPoint[] =>
      grid.map((day) => {
        const pt = byDate.get(day);
        return {
          t: Date.parse(`${day}T12:00:00Z`),
          value: pt ? pick(pt) : null,
        };
      });

    const points = makePoints((pt) => pt.peak_streams);
    const edgeLabels = {
      start: formatShortCalendarDate(first),
      end: formatShortCalendarDate(last),
    };

    if (peakView === "lines") {
      const overlays: LineChartOverlay[] = [
        {
          label: t("web.admin.dashboard.widgets.playbackActivity.series.directPlay"),
          seriesIndex: 0,
          points: makePoints((pt) => pt.peak_direct),
        },
        {
          label: t("web.admin.dashboard.widgets.playbackActivity.series.directStream"),
          seriesIndex: 1,
          points: makePoints((pt) => pt.peak_remux),
        },
        {
          label: t("web.admin.dashboard.widgets.playbackActivity.series.transcode"),
          seriesIndex: 2,
          points: makePoints((pt) => pt.peak_transcode),
        },
      ];
      return { points, overlays, stacks: undefined, edgeLabels };
    }
    if (peakView === "stacked") {
      const stacks: LineChartStack[] = [
        {
          key: "direct",
          label: t("web.admin.dashboard.widgets.playbackActivity.series.directPlay"),
          color: chartSeriesColor(0),
          points: makePoints((pt) => pt.peak_streams_direct),
        },
        {
          key: "remux",
          label: t("web.admin.dashboard.widgets.playbackActivity.series.directStream"),
          color: chartSeriesColor(1),
          points: makePoints((pt) => pt.peak_streams_remux),
        },
        {
          key: "transcode",
          label: t("web.admin.dashboard.widgets.playbackActivity.series.transcode"),
          color: chartSeriesColor(2),
          points: makePoints((pt) => pt.peak_streams_transcode),
        },
      ];
      return { points, overlays: undefined, stacks, edgeLabels };
    }
    return { points, overlays: undefined, stacks: undefined, edgeLabels };
  }, [data?.peak_streams, peakView, t]);

  if (query.isLoading) {
    return (
      <div className="space-y-5">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-24 rounded-2xl" />
          ))}
        </div>
        <Skeleton className="h-72 rounded-2xl" />
        <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
          <Skeleton className="h-64 rounded-2xl" />
          <Skeleton className="h-64 rounded-2xl" />
        </div>
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

  const { summary, coverage } = data;
  const showMeasuredBanner =
    coverage.total_sessions > 0 && coverage.measured_sessions < coverage.total_sessions;
  const hasScopeFilter = filters.libraryId !== undefined || filters.userId !== undefined;

  return (
    <div {...insightRefreshProps(query.isPlaceholderData, "space-y-5")}>
      {showMeasuredBanner ? (
        <InsightNoticeBanner
          message={t("web.admin.insights.coverage.watchTimePartial", {
            measured: coverage.measured_sessions.toLocaleString(formatLocale()),
            total: coverage.total_sessions.toLocaleString(formatLocale()),
            total_count: coverage.total_sessions,
          })}
        />
      ) : null}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <InsightKpiCard
          label={t("web.admin.insights.activity.kpi.plays")}
          value={summary.plays.toLocaleString(formatLocale())}
          subtext={t("web.admin.insights.activity.kpi.playsBreakdown", {
            movies: summary.movies.toLocaleString(formatLocale()),
            movie_count: summary.movies,
            episodes: summary.episodes.toLocaleString(formatLocale()),
            episode_count: summary.episodes,
          })}
        />
        <InsightKpiCard
          label={t("web.admin.insights.activity.kpi.watchTime")}
          value={formatWatchTime(summary.watch_seconds)}
          subtext={
            showMeasuredBanner
              ? t("web.admin.insights.activity.kpi.watchTimeMeasured", {
                  measured: coverage.measured_sessions.toLocaleString(formatLocale()),
                  total: coverage.total_sessions.toLocaleString(formatLocale()),
                  total_count: coverage.total_sessions,
                })
              : formatInsightsCount(t, "web.admin.insights.sessionCount", coverage.total_sessions)
          }
        />
        <InsightKpiCard
          label={t("web.admin.insights.activity.kpi.completionRate")}
          value={t("web.admin.insights.percent", {
            value: formatRatePercentNumber(summary.completion_rate),
          })}
          subtext={t("web.admin.insights.activity.kpi.completionDetail", {
            completed: summary.completed_plays.toLocaleString(formatLocale()),
            total: summary.plays.toLocaleString(formatLocale()),
          })}
          accent="success"
        />
        <InsightKpiCard
          label={t("web.admin.insights.activity.kpi.peakStreams")}
          value={summary.peak_concurrent_streams.toLocaleString(formatLocale())}
          subtext={
            summary.peak_concurrent_at
              ? t("web.admin.insights.activity.kpi.peakStreamsAt", {
                  date: formatShortTimestamp(summary.peak_concurrent_at),
                })
              : t("web.admin.insights.activity.kpi.peakStreamsNone")
          }
          accent="info"
        />
      </div>

      {/* Playback volume stacked column chart */}
      <Card className="surface-panel rounded-2xl border-0">
        <CardHeader className="flex flex-row flex-wrap items-center justify-between gap-2 space-y-0">
          <div className="space-y-0.5">
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.activity.volume.title")}
            </CardTitle>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.insights.activity.volume.subtitle")}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <AddToDashboardButton
              widgetId="insights-plays"
              range={filters.range}
              chartName={t("web.admin.insights.activity.volume.title")}
            />
            <WidgetViewToggle
              value={volumeMetric}
              options={[
                {
                  value: "plays",
                  label: t("web.admin.insights.activity.volume.metricPlays"),
                  ariaLabel: t("web.admin.insights.activity.volume.metricPlays"),
                },
                {
                  value: "watch_time",
                  label: t("web.admin.insights.activity.volume.metricWatchTime"),
                  ariaLabel: t("web.admin.insights.activity.volume.metricWatchTime"),
                },
              ]}
              onChange={setVolumeMetric}
            />
            {volumeMetric === "plays" ? (
              <WidgetViewToggle
                value={volumeSplit}
                options={[
                  {
                    value: "method",
                    label: t("web.admin.insights.activity.volume.splitMethod"),
                    ariaLabel: t("web.admin.insights.activity.volume.splitMethod"),
                  },
                  {
                    value: "media",
                    label: t("web.admin.insights.activity.volume.splitMediaType"),
                    ariaLabel: t("web.admin.insights.activity.volume.splitMediaType"),
                  },
                ]}
                onChange={setVolumeSplit}
              />
            ) : null}
          </div>
        </CardHeader>
        <CardContent>
          <PlaysOverTimeChart
            data={data}
            view={volumeMetric === "watch_time" ? "watch_time" : volumeSplit}
            height={210}
          />
        </CardContent>
      </Card>

      {/* Viewing hours heatmap + Peak concurrent streams */}
      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2 space-y-0">
            <div className="space-y-0.5">
              <CardTitle className="text-sm font-bold">
                {t("web.admin.insights.activity.heatmap.title")}
              </CardTitle>
              <p className="text-muted-foreground text-xs">
                {t("web.admin.insights.activity.heatmap.subtitle", { tz: data.tz })}
              </p>
            </div>
            <AddToDashboardButton
              widgetId="insights-viewing-hours"
              range={filters.range}
              chartName={t("web.admin.insights.activity.heatmap.title")}
            />
          </CardHeader>
          <CardContent>
            {data.heatmap.length === 0 ? (
              <ChartEmptyState message={t("web.admin.insights.activity.heatmap.empty")} />
            ) : (
              <ViewingHeatmap cells={data.heatmap} />
            )}
          </CardContent>
        </Card>

        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2 space-y-0">
            <div className="space-y-0.5">
              <CardTitle className="text-sm font-bold">
                {t("web.admin.insights.activity.peak.title")}
              </CardTitle>
              <p className="text-muted-foreground text-xs">
                {t("web.admin.insights.activity.peak.subtitle")}
              </p>
            </div>
            <WidgetViewToggle
              value={peakView}
              options={[
                {
                  value: "lines",
                  label: t("web.admin.dashboard.widgets.concurrentStreams.viewLines"),
                  ariaLabel: t("web.admin.dashboard.widgets.concurrentStreams.viewLinesLabel"),
                },
                {
                  value: "stacked",
                  label: t("web.admin.dashboard.widgets.concurrentStreams.viewStacked"),
                  ariaLabel: t("web.admin.dashboard.widgets.concurrentStreams.viewStackedLabel"),
                },
                {
                  value: "total",
                  label: t("web.admin.dashboard.widgets.concurrentStreams.viewTotal"),
                  ariaLabel: t("web.admin.dashboard.widgets.concurrentStreams.viewTotalLabel"),
                },
              ]}
              onChange={setPeakView}
            />
          </CardHeader>
          <CardContent className="space-y-3">
            {hasScopeFilter ? (
              <InsightNoticeBanner message={t("web.admin.insights.activity.peak.scopeNotice")} />
            ) : null}
            {peakSeries.points.length === 0 ? (
              <ChartEmptyState message={t("web.admin.insights.activity.peak.empty")} />
            ) : (
              <LineChart
                height={200}
                points={peakSeries.points}
                seriesLabel={
                  peakView === "lines"
                    ? t("web.admin.dashboard.chart.total")
                    : t("web.admin.dashboard.widgets.concurrentStreams.seriesLabel")
                }
                color={peakView === "lines" ? NEUTRAL_SERIES_COLOR : undefined}
                overlays={peakSeries.overlays}
                stacks={peakSeries.stacks}
                edgeLabels={peakSeries.edgeLabels}
                formatTimestamp={(ms) =>
                  formatShortCalendarDate(new Date(ms).toISOString().slice(0, 10))
                }
                minTickStep={1}
                ariaLabel={t("web.admin.insights.activity.peak.chartLabel")}
              />
            )}
          </CardContent>
        </Card>
      </div>

      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <ViewingTimeCard axis="weekday" data={data} filters={filters} />
        <ViewingTimeCard axis="hour" data={data} filters={filters} />
      </div>

      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2 space-y-0">
            <div className="space-y-0.5">
              <CardTitle className="text-sm font-bold">
                {t("web.admin.insights.activity.activePeople.title")}
              </CardTitle>
              <p className="text-muted-foreground text-xs">
                {t("web.admin.insights.activity.activePeople.subtitle")}
              </p>
            </div>
            <AddToDashboardButton
              widgetId="insights-active-people"
              range={filters.range}
              chartName={t("web.admin.insights.activity.activePeople.title")}
            />
          </CardHeader>
          <CardContent>
            <ActivePeopleChart data={data} height={200} />
          </CardContent>
        </Card>
        <EndReasonsCard reasons={data.end_reasons} />
      </div>
    </div>
  );
}

/** Plays or watch time by weekday or hour, with its own metric switch. */
function ViewingTimeCard({
  axis,
  data,
  filters,
}: {
  axis: "weekday" | "hour";
  data: AdminInsightsActivity;
  filters: InsightsFilterState;
}) {
  const t = useT();
  const [metric, setMetric] = useState<ViewingTimeMetric>("plays");
  const title =
    axis === "weekday"
      ? t("web.admin.insights.activity.weekday.title")
      : t("web.admin.insights.activity.hour.title");
  return (
    <Card className="surface-panel rounded-2xl border-0">
      <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2 space-y-0">
        <div className="space-y-0.5">
          <CardTitle className="text-sm font-bold">{title}</CardTitle>
          <p className="text-muted-foreground text-xs">
            {t("web.admin.insights.activity.heatmap.subtitle", { tz: data.tz })}
          </p>
        </div>
        <div className="flex flex-wrap items-center gap-2">
          <AddToDashboardButton
            widgetId={axis === "weekday" ? "insights-weekday" : "insights-hour-of-day"}
            range={filters.range}
            chartName={title}
          />
          <WidgetViewToggle
            value={metric}
            options={[
              {
                value: "plays",
                label: t("web.admin.insights.activity.volume.metricPlays"),
                ariaLabel: t("web.admin.insights.activity.volume.metricPlays"),
              },
              {
                value: "watch_time",
                label: t("web.admin.insights.activity.volume.metricWatchTime"),
                ariaLabel: t("web.admin.insights.activity.volume.metricWatchTime"),
              },
            ]}
            onChange={setMetric}
          />
        </div>
      </CardHeader>
      <CardContent>
        <ViewingTimeChart cells={data.heatmap} axis={axis} metric={metric} height={180} />
      </CardContent>
    </Card>
  );
}

const END_REASON_LABELS: Record<string, MessageKey> = {
  stopped: "web.admin.history.endReason.stopped",
  terminated: "web.admin.history.endReason.terminated",
  expired: "web.admin.history.endReason.expired",
  aborted: "web.admin.history.endReason.aborted",
  replaced: "web.admin.history.endReason.replaced",
};

/**
 * Sessions by why they ended. A high share of timeouts points at clients that
 * crash or lose their connection; "Ended by server" at missing media.
 */
function EndReasonsCard({ reasons }: { reasons: AdminInsightsActivity["end_reasons"] }) {
  const t = useT();
  const total = reasons.reduce((sum, reason) => sum + reason.sessions, 0);
  const items: BarListItem[] = reasons.map((reason) => {
    const labelKey = END_REASON_LABELS[reason.key];
    return {
      id: reason.key,
      label: labelKey ? t(labelKey) : t("web.admin.insights.activity.endReasons.unknown"),
      value: reason.sessions,
      secondary: t("web.admin.insights.percent", {
        value: formatRatePercentNumber(total > 0 ? reason.sessions / total : 0),
      }),
    };
  });
  return (
    <Card className="surface-panel rounded-2xl border-0">
      <CardHeader className="space-y-0.5">
        <CardTitle className="text-sm font-bold">
          {t("web.admin.insights.activity.endReasons.title")}
        </CardTitle>
        <p className="text-muted-foreground text-xs">
          {t("web.admin.insights.activity.endReasons.subtitle")}
        </p>
      </CardHeader>
      <CardContent>
        <BarList
          items={items}
          seriesIndex={3}
          formatValue={(sessions) =>
            formatInsightsCount(t, "web.admin.insights.sessionCount", sessions)
          }
          emptyLabel={t("web.admin.insights.activity.volume.empty")}
        />
      </CardContent>
    </Card>
  );
}
