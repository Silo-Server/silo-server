import { useMemo, useState } from "react";

import {
  ChartEmptyState,
  LineChart,
  chartSeriesColor,
  type LineChartOverlay,
  type LineChartPoint,
  type LineChartStack,
} from "@/components/admin/dashboard/charts";
import { formatDataTotal, formatMbps, formatMbpsValue } from "@/components/admin/dashboard/format";
import { WidgetViewToggle } from "@/components/admin/dashboard/WidgetViewToggle";
import { NEUTRAL_SERIES_COLOR } from "@/components/admin/dashboard/widgets/concurrentStreamsSeries";
import {
  MAX_NAMED_BANDS,
  OTHER_COLOR,
  assignEgressSourceColors,
  assignEgressSourceSlots,
  dedupeEgressSourceLabels,
} from "@/components/admin/dashboard/widgets/egressBands";
import { Badge } from "@/components/ui/badge";
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { Skeleton } from "@/components/ui/skeleton";
import {
  Table,
  TableBody,
  TableCell,
  TableHead,
  TableHeader,
  TableRow,
} from "@/components/ui/table";
import { useAdminInsightsBandwidth } from "@/hooks/queries/admin/insights";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

import {
  buildDailyUtcGrid,
  buildInsightsWindowQueryParams,
  formatRatePercentNumber,
  formatShortCalendarDate,
  formatShortTimestamp,
  type InsightsFilterState,
} from "./insightsHelpers";
import { InsightKpiCard, InsightNoticeBanner, InsightSectionError } from "./insightsShared";

type BandwidthMetric = "volume" | "peak";
type BandwidthScope = "total" | "nodes";
const EM_DASH = "—";

export function BandwidthTab({ filters }: { filters: InsightsFilterState }) {
  const t = useT();
  const [metric, setMetric] = useState<BandwidthMetric>("volume");
  const [scopeView, setScopeView] = useState<BandwidthScope>("total");

  const queryParams = useMemo(
    () => buildInsightsWindowQueryParams(filters, { omitLibrary: true, omitUser: true }),
    [filters],
  );
  const query = useAdminInsightsBandwidth(queryParams);
  const data = query.data;

  const canSplitNodes = Boolean(data?.has_remote_nodes && (data?.sources.length ?? 0) > 1);
  const effectiveScope: BandwidthScope = scopeView === "nodes" && canSplitNodes ? "nodes" : "total";

  const chartSeries = useMemo(() => {
    const raw = data?.points ?? [];
    if (raw.length === 0 || !data) {
      return {
        points: [] as LineChartPoint[],
        color: undefined as string | undefined,
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

    const edgeLabels = {
      start: formatShortCalendarDate(first),
      end: formatShortCalendarDate(last),
    };

    const points = makePoints((pt) =>
      metric === "volume" ? pt.egress_bytes : pt.peak_egress_kbps / 1_000,
    );

    if (effectiveScope === "nodes") {
      const topSources = [...data.sources]
        .sort((a, b) => b.egress_bytes - a.egress_bytes || a.key.localeCompare(b.key))
        .slice(0, MAX_NAMED_BANDS);
      const keptKeys = new Set(topSources.map((s) => s.key));
      const apiLabel = t("web.admin.insights.bandwidth.chart.apiServer");
      const labels = dedupeEgressSourceLabels(topSources, apiLabel);
      const colors = assignEgressSourceColors(topSources);
      const slots = assignEgressSourceSlots(topSources);

      // Per-node daily peak rates come from independent minutes, so stacking
      // them could overshoot the cluster total peak. Draw them as separate
      // overlay lines under the neutral total line; reserve stacked bands for
      // additive daily byte volumes.
      if (metric === "peak") {
        const overlays: LineChartOverlay[] = topSources.map((src) => ({
          label: labels.get(src.key) ?? src.name,
          seriesIndex: slots.get(src.key) ?? 0,
          points: makePoints((pt) => (pt.peak_kbps_by_source[src.key] ?? 0) / 1_000),
        }));
        return { points, color: NEUTRAL_SERIES_COLOR, overlays, stacks: undefined, edgeLabels };
      }

      const namedStacks: LineChartStack[] = topSources.map((src) => ({
        key: src.key,
        label: labels.get(src.key) ?? src.name,
        color: colors.get(src.key) ?? chartSeriesColor(0),
        points: makePoints((pt) => pt.bytes_by_source[src.key] ?? 0),
      }));

      const otherPoints = makePoints((pt) => {
        const keptSum = Object.entries(pt.bytes_by_source)
          .filter(([k]) => keptKeys.has(k))
          .reduce((sum, [, val]) => sum + val, 0);
        return Math.max(pt.egress_bytes - keptSum, 0);
      });
      const hasOther = otherPoints.some((p) => (p.value ?? 0) > 0);
      const stacks = hasOther
        ? [
            {
              key: "other",
              label: t("web.admin.dashboard.widgets.egress.otherBand"),
              color: OTHER_COLOR,
              points: otherPoints,
            },
            ...namedStacks,
          ]
        : namedStacks;
      return { points, color: undefined, overlays: undefined, stacks, edgeLabels };
    }

    const hasDownloads = raw.some((pt) =>
      metric === "volume" ? pt.download_egress_bytes > 0 : pt.peak_download_kbps > 0,
    );
    const overlays: LineChartOverlay[] | undefined = hasDownloads
      ? [
          {
            label: t("web.admin.dashboard.widgets.egress.downloadsSeries"),
            seriesIndex: 1,
            points: makePoints((pt) =>
              metric === "volume" ? pt.download_egress_bytes : pt.peak_download_kbps / 1_000,
            ),
          },
        ]
      : undefined;

    return { points, color: undefined, overlays, stacks: undefined, edgeLabels };
  }, [data, effectiveScope, metric, t]);

  if (query.isLoading) {
    return (
      <div className="space-y-5">
        <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
          {Array.from({ length: 4 }).map((_, i) => (
            <Skeleton key={i} className="h-24 rounded-2xl" />
          ))}
        </div>
        <Skeleton className="h-72 rounded-2xl" />
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
  const showMetricsStartBanner =
    Boolean(data.oldest_metric_at) &&
    Date.parse(data.from) < Date.parse(data.oldest_metric_at ?? "");
  const knownLocationSessions = summary.remote_sessions + summary.local_sessions;

  return (
    <div className="space-y-5">
      {showMetricsStartBanner ? (
        <InsightNoticeBanner
          message={t("web.admin.insights.coverage.metricsStart", {
            since: formatShortCalendarDate(data.oldest_metric_at),
          })}
        />
      ) : null}

      <div className="grid grid-cols-1 gap-3 sm:grid-cols-2 xl:grid-cols-4">
        <InsightKpiCard
          label={t("web.admin.insights.bandwidth.kpi.totalEgress")}
          value={formatDataTotal(summary.total_egress_bytes)}
          subtext={
            data.has_remote_nodes
              ? t("web.admin.insights.bandwidth.kpi.nodeSplit", {
                  api: formatDataTotal(summary.api_egress_bytes),
                  nodes: formatDataTotal(summary.node_egress_bytes),
                })
              : t("web.admin.insights.bandwidth.kpi.egressSplit", {
                  playback: formatDataTotal(summary.playback_egress_bytes),
                  downloads: formatDataTotal(summary.download_egress_bytes),
                })
          }
        />
        <InsightKpiCard
          label={t("web.admin.insights.bandwidth.kpi.peakEgress")}
          value={formatMbps(summary.peak_egress_kbps / 1_000)}
          subtext={
            summary.peak_egress_at
              ? t("web.admin.insights.bandwidth.kpi.peakEgressAt", {
                  date: formatShortTimestamp(summary.peak_egress_at),
                })
              : EM_DASH
          }
          accent="info"
        />
        <InsightKpiCard
          label={t("web.admin.insights.bandwidth.kpi.dailyAverage")}
          value={formatDataTotal(summary.avg_daily_bytes)}
          subtext={t("web.admin.insights.bandwidth.kpi.dailyAverageSub", {
            count: summary.active_days,
          })}
        />
        <InsightKpiCard
          label={t("web.admin.insights.bandwidth.kpi.remoteShare")}
          value={
            knownLocationSessions > 0
              ? t("web.admin.insights.percent", {
                  value: formatRatePercentNumber(summary.remote_sessions / knownLocationSessions),
                })
              : EM_DASH
          }
          subtext={
            knownLocationSessions > 0
              ? t("web.admin.insights.bandwidth.kpi.remoteShareDetail", {
                  remote: summary.remote_sessions.toLocaleString(formatLocale()),
                  local: summary.local_sessions.toLocaleString(formatLocale()),
                })
              : t("web.admin.insights.bandwidth.kpi.remoteShareUnknown")
          }
        />
      </div>

      {/* Egress over time */}
      <Card className="surface-panel rounded-2xl border-0">
        <CardHeader className="flex flex-row flex-wrap items-start justify-between gap-2 space-y-0">
          <div className="space-y-0.5">
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.bandwidth.chart.title")}
            </CardTitle>
            <p className="text-muted-foreground text-xs">
              {t("web.admin.insights.bandwidth.chart.subtitle")}
            </p>
          </div>
          <div className="flex flex-wrap items-center gap-2">
            <WidgetViewToggle
              value={metric}
              options={[
                {
                  value: "volume",
                  label: t("web.admin.insights.bandwidth.chart.metricVolume"),
                  ariaLabel: t("web.admin.insights.bandwidth.chart.metricVolume"),
                },
                {
                  value: "peak",
                  label: t("web.admin.insights.bandwidth.chart.metricPeak"),
                  ariaLabel: t("web.admin.insights.bandwidth.chart.metricPeak"),
                },
              ]}
              onChange={setMetric}
            />
            {canSplitNodes ? (
              <WidgetViewToggle
                value={effectiveScope}
                options={[
                  {
                    value: "total",
                    label: t("web.admin.insights.bandwidth.chart.viewTotal"),
                    ariaLabel: t("web.admin.insights.bandwidth.chart.viewTotal"),
                  },
                  {
                    value: "nodes",
                    label: t("web.admin.insights.bandwidth.chart.viewNodes"),
                    ariaLabel: t("web.admin.insights.bandwidth.chart.viewNodes"),
                  },
                ]}
                onChange={setScopeView}
              />
            ) : null}
          </div>
        </CardHeader>
        <CardContent>
          {chartSeries.points.length === 0 ? (
            <ChartEmptyState message={t("web.admin.insights.bandwidth.chart.empty")} />
          ) : (
            <LineChart
              height={220}
              points={chartSeries.points}
              color={chartSeries.color}
              seriesLabel={
                effectiveScope === "nodes" && metric === "peak"
                  ? t("web.admin.dashboard.chart.total")
                  : t(
                      metric === "volume"
                        ? "web.admin.insights.bandwidth.chart.metricVolume"
                        : "web.admin.insights.bandwidth.chart.metricPeak",
                    )
              }
              overlays={chartSeries.overlays}
              stacks={chartSeries.stacks}
              edgeLabels={chartSeries.edgeLabels}
              formatValue={metric === "volume" ? formatDataTotal : formatMbps}
              formatTick={metric === "volume" ? formatDataTotal : formatMbpsValue}
              formatTimestamp={(ms) =>
                formatShortCalendarDate(new Date(ms).toISOString().slice(0, 10))
              }
              ariaLabel={t(
                metric === "volume"
                  ? "web.admin.insights.bandwidth.chart.volumeLabel"
                  : "web.admin.insights.bandwidth.chart.peakLabel",
              )}
            />
          )}
        </CardContent>
      </Card>

      {/* Per-Node Breakdown (hidden on single-server installs with no remote nodes) */}
      {data.has_remote_nodes ? (
        <Card className="surface-panel rounded-2xl border-0">
          <CardHeader>
            <CardTitle className="text-sm font-bold">
              {t("web.admin.insights.bandwidth.nodes.title")}
            </CardTitle>
          </CardHeader>
          <CardContent>
            {data.sources.length === 0 ? (
              <div className="text-muted-foreground py-6 text-center text-sm">
                {t("web.admin.insights.bandwidth.nodes.empty")}
              </div>
            ) : (
              <div className="overflow-x-auto">
                <Table>
                  <TableHeader>
                    <TableRow>
                      <TableHead>{t("web.admin.insights.bandwidth.nodes.column.node")}</TableHead>
                      <TableHead className="text-end">
                        {t("web.admin.insights.bandwidth.nodes.column.total")}
                      </TableHead>
                      <TableHead className="text-end">
                        {t("web.admin.insights.bandwidth.nodes.column.share")}
                      </TableHead>
                      <TableHead className="text-end">
                        {t("web.admin.insights.bandwidth.nodes.column.peak")}
                      </TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {data.sources.map((src) => {
                      const displayName =
                        src.kind === "api"
                          ? t("web.admin.insights.bandwidth.chart.apiServer")
                          : src.name;
                      const sharePct = Math.max(0, Math.min(100, src.share_of_total * 100));
                      return (
                        <TableRow key={src.key}>
                          <TableCell className="font-medium">
                            <div className="flex items-center gap-2">
                              <span>{displayName}</span>
                              {src.node_type ? (
                                <Badge variant="outline" className="text-[0.625rem]">
                                  {src.node_type === "proxy"
                                    ? t("web.admin.activity.route.proxy")
                                    : src.node_type === "transcode"
                                      ? t("web.admin.activity.route.transcode")
                                      : src.node_type}
                                </Badge>
                              ) : null}
                            </div>
                          </TableCell>
                          <TableCell className="text-end tabular-nums">
                            {formatDataTotal(src.egress_bytes)}
                          </TableCell>
                          <TableCell className="text-end tabular-nums">
                            <div className="inline-flex items-center justify-end gap-2">
                              <div className="bg-muted h-1.5 w-16 overflow-hidden rounded-full forced-colors:bg-transparent forced-colors:outline forced-colors:outline-[CanvasText]">
                                <div
                                  className="bg-primary h-full rounded-full forced-colors:bg-[Highlight]"
                                  style={{ width: `${sharePct}%` }}
                                />
                              </div>
                              <span className="w-10 text-end">
                                {t("web.admin.insights.percent", {
                                  value: formatRatePercentNumber(src.share_of_total),
                                })}
                              </span>
                            </div>
                          </TableCell>
                          <TableCell className="text-end tabular-nums">
                            {formatMbps(src.peak_egress_kbps / 1_000)}
                          </TableCell>
                        </TableRow>
                      );
                    })}
                  </TableBody>
                </Table>
              </div>
            )}
          </CardContent>
        </Card>
      ) : null}
    </div>
  );
}
