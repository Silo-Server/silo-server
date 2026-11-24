import { useMemo, useRef, useState, type KeyboardEvent } from "react";

import {
  ChartEmptyState,
  LineChart,
  StackedColumnChart,
  chartSeriesColor,
  type LineChartOverlay,
  type LineChartPoint,
  type LineChartStack,
  type StackedColumnBucket,
} from "@/components/admin/dashboard/charts";
import { formatWatchTime } from "@/components/admin/dashboard/format";
import { WidgetViewToggle } from "@/components/admin/dashboard/WidgetViewToggle";
import { NEUTRAL_SERIES_COLOR } from "@/components/admin/dashboard/widgets/concurrentStreamsSeries";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import { useAdminInsightsActivity } from "@/hooks/queries/admin/insights";
import { useAppLanguage, useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import { cn } from "@/lib/utils";

import {
  buildActivityBucketGrid,
  buildDailyUtcGrid,
  buildInsightsWindowQueryParams,
  formatActivityBucketLabel,
  formatInsightsCount,
  formatRatePercentNumber,
  formatShortCalendarDate,
  formatShortTimestamp,
  type InsightsFilterState,
} from "./insightsHelpers";
import { InsightKpiCard, InsightNoticeBanner, InsightSectionError } from "./insightsShared";

type VolumeMetric = "plays" | "watch_time";
type VolumeSplit = "method" | "media";
type PeakView = "lines" | "stacked" | "total";

const HEATMAP_DAY_ANCHORS_UTC = [
  "2024-01-01T12:00:00Z", // Mon
  "2024-01-02T12:00:00Z", // Tue
  "2024-01-03T12:00:00Z", // Wed
  "2024-01-04T12:00:00Z", // Thu
  "2024-01-05T12:00:00Z", // Fri
  "2024-01-06T12:00:00Z", // Sat
  "2024-01-07T12:00:00Z", // Sun
] as const;

const HEATMAP_LEVEL_CLASSES = [
  "bg-muted/35",
  "bg-primary/20",
  "bg-primary/40",
  "bg-primary/65",
  "bg-primary/90",
] as const;

function heatmapLevel(plays: number, maxPlays: number): number {
  if (plays <= 0 || maxPlays <= 0) return 0;
  const ratio = plays / maxPlays;
  if (ratio <= 0.25) return 1;
  if (ratio <= 0.5) return 2;
  if (ratio <= 0.75) return 3;
  return 4;
}

export function ActivityTab({ filters }: { filters: InsightsFilterState }) {
  const t = useT();
  const language = useAppLanguage();
  const [volumeMetric, setVolumeMetric] = useState<VolumeMetric>("plays");
  const [volumeSplit, setVolumeSplit] = useState<VolumeSplit>("method");
  const [peakView, setPeakView] = useState<PeakView>("lines");
  const [activeHeatmapCell, setActiveHeatmapCell] = useState<{ dow: number; hour: number }>({
    dow: 0,
    hour: 0,
  });
  const heatmapCellRefs = useRef<Array<HTMLDivElement | null>>([]);

  function moveHeatmapFocus(dow: number, hour: number) {
    setActiveHeatmapCell({ dow, hour });
    heatmapCellRefs.current[dow * 24 + hour]?.focus();
  }

  function handleHeatmapKeyDown(event: KeyboardEvent<HTMLDivElement>, dow: number, hour: number) {
    switch (event.key) {
      case "ArrowRight":
        event.preventDefault();
        moveHeatmapFocus(dow, Math.min(23, hour + 1));
        break;
      case "ArrowLeft":
        event.preventDefault();
        moveHeatmapFocus(dow, Math.max(0, hour - 1));
        break;
      case "ArrowDown":
        event.preventDefault();
        moveHeatmapFocus(Math.min(6, dow + 1), hour);
        break;
      case "ArrowUp":
        event.preventDefault();
        moveHeatmapFocus(Math.max(0, dow - 1), hour);
        break;
      case "Home":
        event.preventDefault();
        moveHeatmapFocus(event.ctrlKey ? 0 : dow, 0);
        break;
      case "End":
        event.preventDefault();
        moveHeatmapFocus(event.ctrlKey ? 6 : dow, 23);
        break;
      default:
        break;
    }
  }

  const queryParams = useMemo(() => buildInsightsWindowQueryParams(filters), [filters]);
  const query = useAdminInsightsActivity(queryParams);
  const data = query.data;

  // Fill every quiet day/week/month bucket across [data.from, data.to) using
  // the API's local calendar label_date anchored at UTC midnight so columns
  // stay evenly spaced by time and never shift days in another browser TZ.
  const volumeBuckets = useMemo<StackedColumnBucket[]>(() => {
    // Buckets carry sessions too, so a window can have buckets but no plays.
    const hasMetric =
      volumeMetric === "watch_time"
        ? data?.buckets.some((b) => b.watch_seconds > 0)
        : data?.buckets.some((b) => b.plays > 0);
    if (!data || !hasMetric) return [];
    const byDate = new Map(data.buckets.map((b) => [b.label_date, b]));
    const grid = buildActivityBucketGrid(data.from, data.to, data.tz, data.bucket_unit);
    const dates = grid.length > 0 ? grid : data.buckets.map((b) => b.label_date);
    return dates.map((labelDate) => {
      const ts = Date.parse(`${labelDate}T00:00:00Z`);
      const bucket = byDate.get(labelDate);
      if (volumeMetric === "watch_time") {
        return { t: ts, segments: [bucket?.watch_seconds ?? 0] };
      }
      if (volumeSplit === "media") {
        return {
          t: ts,
          segments: [bucket?.movies ?? 0, bucket?.episodes ?? 0, bucket?.other_media ?? 0],
        };
      }
      return {
        t: ts,
        segments: [bucket?.direct ?? 0, bucket?.remux ?? 0, bucket?.transcode ?? 0],
      };
    });
  }, [data, volumeMetric, volumeSplit]);

  const volumeSeriesLabels = useMemo<string[]>(() => {
    if (volumeMetric === "watch_time") {
      return [t("web.admin.insights.activity.volume.metricWatchTime")];
    }
    if (volumeSplit === "media") {
      return [
        t("web.admin.insights.activity.volume.seriesMovies"),
        t("web.admin.insights.activity.volume.seriesEpisodes"),
        t("web.admin.insights.activity.volume.seriesOther"),
      ];
    }
    return [
      t("web.admin.dashboard.widgets.playbackActivity.series.directPlay"),
      t("web.admin.dashboard.widgets.playbackActivity.series.directStream"),
      t("web.admin.dashboard.widgets.playbackActivity.series.transcode"),
    ];
  }, [t, volumeMetric, volumeSplit]);

  const heatmapGrid = useMemo(() => {
    const map = new Map<string, { plays: number; watchSeconds: number }>();
    let maxPlays = 0;
    for (const cell of data?.heatmap ?? []) {
      map.set(`${cell.day_of_week}:${cell.hour}`, {
        plays: cell.plays,
        watchSeconds: cell.watch_seconds,
      });
      if (cell.plays > maxPlays) maxPlays = cell.plays;
    }
    return { map, maxPlays };
  }, [data?.heatmap]);

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

  const weekdayLabels = useMemo(() => {
    const fmt = new Intl.DateTimeFormat(formatLocale(), { weekday: "short", timeZone: "UTC" });
    return HEATMAP_DAY_ANCHORS_UTC.map((iso) => fmt.format(new Date(iso)));
    // eslint-disable-next-line react-hooks/exhaustive-deps -- language re-runs localized weekday names
  }, [language]);

  const hourLabels = useMemo(() => {
    const fmt = new Intl.DateTimeFormat(formatLocale(), { hour: "numeric", timeZone: "UTC" });
    return Array.from({ length: 24 }, (_, hour) =>
      fmt.format(new Date(Date.UTC(2024, 0, 1, hour, 0, 0))),
    );
    // eslint-disable-next-line react-hooks/exhaustive-deps -- language re-runs localized hour labels
  }, [language]);

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
    <div className="space-y-5">
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
          {volumeBuckets.length === 0 ? (
            <ChartEmptyState message={t("web.admin.insights.activity.volume.empty")} />
          ) : (
            <StackedColumnChart
              height={210}
              buckets={volumeBuckets}
              seriesLabels={volumeSeriesLabels}
              formatBucket={(timeMs) => formatActivityBucketLabel(timeMs, data.bucket_unit)}
              formatValue={
                volumeMetric === "watch_time"
                  ? (seconds) => formatWatchTime(seconds)
                  : (v) => v.toLocaleString(formatLocale())
              }
              totalLabel={
                volumeMetric === "watch_time"
                  ? undefined
                  : t("web.admin.insights.activity.volume.metricPlays")
              }
              ariaLabel={t("web.admin.insights.activity.volume.chartLabel")}
            />
          )}
        </CardContent>
      </Card>

      {/* Viewing hours heatmap + Peak concurrent streams */}
      <div className="grid grid-cols-1 gap-5 xl:grid-cols-2">
        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader className="space-y-0.5">
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.activity.heatmap.title")}
            </CardTitle>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.insights.activity.heatmap.subtitle", { tz: data.tz })}
            </p>
          </CardHeader>
          <CardContent>
            {data.heatmap.length === 0 ? (
              <ChartEmptyState message={t("web.admin.insights.activity.heatmap.empty")} />
            ) : (
              <div className="space-y-3">
                <div className="overflow-x-auto">
                  <div
                    role="grid"
                    aria-label={t("web.admin.insights.activity.heatmap.title")}
                    dir="ltr"
                    className="min-w-[440px] space-y-1"
                  >
                    <div
                      role="row"
                      className="grid grid-cols-[2.75rem_repeat(24,minmax(0,1fr))] items-center gap-1 pb-1"
                    >
                      <span role="columnheader" aria-hidden="true" />
                      {hourLabels.map((label, hour) => (
                        <span
                          key={hour}
                          role="columnheader"
                          className="text-muted-foreground text-center text-[0.625rem] tabular-nums"
                        >
                          {hour % 6 === 0 ? label : ""}
                        </span>
                      ))}
                    </div>
                    {weekdayLabels.map((dayLabel, dow) => (
                      <div
                        key={dow}
                        role="row"
                        className="grid grid-cols-[2.75rem_repeat(24,minmax(0,1fr))] items-center gap-1"
                      >
                        <span
                          role="rowheader"
                          className="text-muted-foreground pe-1 text-end text-[0.6875rem] font-medium"
                        >
                          {dayLabel}
                        </span>
                        {hourLabels.map((hourLabel, hour) => {
                          const cell = heatmapGrid.map.get(`${dow}:${hour}`);
                          const plays = cell?.plays ?? 0;
                          const watchSeconds = cell?.watchSeconds ?? 0;
                          const level = heatmapLevel(plays, heatmapGrid.maxPlays);
                          const isActive =
                            activeHeatmapCell.dow === dow && activeHeatmapCell.hour === hour;
                          const tooltip = t("web.admin.insights.activity.heatmap.cellTooltip", {
                            day: dayLabel,
                            hour: hourLabel,
                            plays: t("web.admin.insights.playCount", {
                              count: plays,
                              count_text: plays.toLocaleString(formatLocale()),
                            }),
                            watch_time: formatWatchTime(watchSeconds),
                          });
                          return (
                            <div
                              key={hour}
                              ref={(el) => {
                                heatmapCellRefs.current[dow * 24 + hour] = el;
                              }}
                              tabIndex={isActive ? 0 : -1}
                              role="gridcell"
                              aria-label={tooltip}
                              title={tooltip}
                              onFocus={() => setActiveHeatmapCell({ dow, hour })}
                              onKeyDown={(e) => handleHeatmapKeyDown(e, dow, hour)}
                              className={cn(
                                "focus-visible:ring-ring h-5 rounded-[4px] transition-transform hover:scale-110 focus-visible:ring-2 focus-visible:outline-none",
                                HEATMAP_LEVEL_CLASSES[level],
                              )}
                            />
                          );
                        })}
                      </div>
                    ))}
                  </div>
                </div>
                <div className="text-muted-foreground flex items-center justify-end gap-1.5 text-[0.6875rem]">
                  <span>{t("web.admin.insights.activity.heatmap.less")}</span>
                  {HEATMAP_LEVEL_CLASSES.map((cls, idx) => (
                    <span key={idx} className={cn("h-3 w-3 rounded-[3px]", cls)} />
                  ))}
                  <span>{t("web.admin.insights.activity.heatmap.more")}</span>
                </div>
              </div>
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
    </div>
  );
}
