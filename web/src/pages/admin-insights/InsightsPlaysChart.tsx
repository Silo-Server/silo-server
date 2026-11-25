import { useMemo } from "react";

import {
  ChartEmptyState,
  StackedColumnChart,
  type StackedColumnBucket,
} from "@/components/admin/dashboard/charts";
import { useT } from "@/i18n";
import { formatLocale } from "@/lib/appLocale";

import { buildActivityBucketGrid, formatActivityBucketLabel } from "./insightsHelpers";

interface PlaysBucket {
  label_date: string;
  plays: number;
}

/**
 * Plays per day, week or month across a detail panel's window, with quiet
 * buckets filled in so columns stay evenly spaced.
 */
export function InsightsPlaysChart({
  buckets,
  from,
  to,
  tz,
  unit,
  emptyMessage,
}: {
  buckets: readonly PlaysBucket[];
  from: string;
  to: string;
  tz: string;
  unit: "day" | "week" | "month";
  emptyMessage: string;
}) {
  const t = useT();
  const columns = useMemo<StackedColumnBucket[]>(() => {
    if (!buckets.some((b) => b.plays > 0)) return [];
    const byDate = new Map(buckets.map((b) => [b.label_date, b.plays]));
    const grid = buildActivityBucketGrid(from, to, tz, unit);
    const dates = grid.length > 0 ? grid : buckets.map((b) => b.label_date);
    return dates.map((labelDate) => ({
      t: Date.parse(`${labelDate}T00:00:00Z`),
      segments: [byDate.get(labelDate) ?? 0],
    }));
  }, [buckets, from, to, tz, unit]);

  if (columns.length === 0) {
    return <ChartEmptyState message={emptyMessage} />;
  }
  return (
    <StackedColumnChart
      height={150}
      buckets={columns}
      seriesLabels={[t("web.admin.insights.activity.volume.metricPlays")]}
      formatBucket={(timeMs) => formatActivityBucketLabel(timeMs, unit)}
      formatValue={(v) => v.toLocaleString(formatLocale())}
      ariaLabel={t("web.admin.insights.detail.playsOverTime")}
    />
  );
}
