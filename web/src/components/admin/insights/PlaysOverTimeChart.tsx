import { useMemo } from "react";

import {
  ChartEmptyState,
  StackedColumnChart,
  type StackedColumnBucket,
} from "@/components/admin/dashboard/charts";
import { formatWatchTime } from "@/components/admin/dashboard/format";
import type { AdminInsightsActivity } from "@/hooks/queries/admin/insights";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";
import {
  buildActivityBucketGrid,
  formatActivityBucketLabel,
} from "@/pages/admin-insights/insightsHelpers";

/** What the columns show: plays split by play method or media type, or watch time. */
export type PlaysOverTimeView = "method" | "media" | "watch_time";

/**
 * Plays per day, week or month of an Activity read. Quiet buckets are filled
 * across [from, to) from the server's local-calendar labels, anchored at UTC
 * midnight, so columns stay evenly spaced and never shift a day in another
 * browser timezone.
 */
export function PlaysOverTimeChart({
  data,
  view,
  height,
  fill,
}: {
  data: AdminInsightsActivity;
  view: PlaysOverTimeView;
  height?: number;
  fill?: boolean;
}) {
  const t = useT();
  const buckets = useMemo<StackedColumnBucket[]>(() => {
    // Buckets carry sessions too, so a window can have buckets but no plays.
    const hasMetric =
      view === "watch_time"
        ? data.buckets.some((b) => b.watch_seconds > 0)
        : data.buckets.some((b) => b.plays > 0);
    if (!hasMetric) return [];
    const byDate = new Map(data.buckets.map((b) => [b.label_date, b]));
    const grid = buildActivityBucketGrid(data.from, data.to, data.tz, data.bucket_unit);
    const dates = grid.length > 0 ? grid : data.buckets.map((b) => b.label_date);
    return dates.map((labelDate) => {
      const ts = Date.parse(`${labelDate}T00:00:00Z`);
      const bucket = byDate.get(labelDate);
      if (view === "watch_time") {
        return { t: ts, segments: [bucket?.watch_seconds ?? 0] };
      }
      if (view === "media") {
        return {
          t: ts,
          segments: [bucket?.movies ?? 0, bucket?.episodes ?? 0, bucket?.other_media ?? 0],
        };
      }
      return { t: ts, segments: [bucket?.direct ?? 0, bucket?.remux ?? 0, bucket?.transcode ?? 0] };
    });
  }, [data, view]);

  const seriesLabels = useMemo<string[]>(() => {
    if (view === "watch_time") {
      return [t("web.admin.insights.activity.volume.metricWatchTime")];
    }
    if (view === "media") {
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
  }, [t, view]);

  if (buckets.length === 0) {
    return <ChartEmptyState fill={fill} message={t("web.admin.insights.activity.volume.empty")} />;
  }
  return (
    <StackedColumnChart
      fill={fill}
      height={height}
      buckets={buckets}
      seriesLabels={seriesLabels}
      formatBucket={(timeMs) => formatActivityBucketLabel(timeMs, data.bucket_unit)}
      formatValue={
        view === "watch_time"
          ? (seconds) => formatWatchTime(seconds)
          : (v) => v.toLocaleString(formatLocale())
      }
      totalLabel={
        view === "watch_time" ? undefined : t("web.admin.insights.activity.volume.metricPlays")
      }
      ariaLabel={t("web.admin.insights.activity.volume.chartLabel")}
    />
  );
}
