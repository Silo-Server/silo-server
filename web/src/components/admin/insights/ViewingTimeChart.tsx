import { useMemo } from "react";

import {
  ChartEmptyState,
  StackedColumnChart,
  type StackedColumnBucket,
} from "@/components/admin/dashboard/charts";
import { formatWatchTime } from "@/components/admin/dashboard/format";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

import {
  hourTotals,
  useHourLabels,
  useWeekdayLabels,
  weekdayTotals,
  type ViewingTimeCell,
  type ViewingTimeMetric,
} from "./viewingTime";

/**
 * Plays (or watch time) by day of the week or by hour of the day: the
 * heatmap's rows or columns added up. Columns are indexed rather than timed,
 * so `t` is the weekday or hour and the axis labels come from the locale.
 */
export function ViewingTimeChart({
  cells,
  axis,
  metric,
  height,
  fill,
}: {
  cells: readonly ViewingTimeCell[];
  axis: "weekday" | "hour";
  metric: ViewingTimeMetric;
  height?: number;
  fill?: boolean;
}) {
  const t = useT();
  const weekdayLabels = useWeekdayLabels();
  const hourLabels = useHourLabels();
  const labels = axis === "weekday" ? weekdayLabels : hourLabels;
  const buckets = useMemo<StackedColumnBucket[]>(() => {
    const totals = axis === "weekday" ? weekdayTotals(cells, metric) : hourTotals(cells, metric);
    return totals.map((value, index) => ({ t: index, segments: [value] }));
  }, [axis, cells, metric]);
  const empty = buckets.every((bucket) => (bucket.segments[0] ?? 0) === 0);

  if (empty) {
    return <ChartEmptyState fill={fill} message={t("web.admin.insights.activity.heatmap.empty")} />;
  }
  const metricLabel =
    metric === "watch_time"
      ? t("web.admin.insights.activity.volume.metricWatchTime")
      : t("web.admin.insights.activity.volume.metricPlays");
  return (
    <StackedColumnChart
      fill={fill}
      height={height}
      buckets={buckets}
      seriesLabels={[metricLabel]}
      formatBucket={(index) => labels[index] ?? ""}
      formatValue={
        metric === "watch_time"
          ? (seconds) => formatWatchTime(seconds)
          : (value) => value.toLocaleString(formatLocale())
      }
      ariaLabel={
        axis === "weekday"
          ? t("web.admin.insights.activity.weekday.title")
          : t("web.admin.insights.activity.hour.title")
      }
    />
  );
}
