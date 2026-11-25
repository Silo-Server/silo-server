import { useMemo } from "react";

import {
  ChartEmptyState,
  LineChart,
  type LineChartPoint,
} from "@/components/admin/dashboard/charts";
import type { AdminInsightsActivity } from "@/hooks/queries/admin/insights";
import { useT } from "@/i18n";
import {
  buildActivityBucketGrid,
  formatActivityBucketLabel,
} from "@/pages/admin-insights/insightsHelpers";

/**
 * Distinct profiles (the line) and accounts (the overlay) with a session that
 * started in each day, week or month of an Activity read. A quiet bucket is a
 * real zero, not a gap: nobody watched.
 */
export function ActivePeopleChart({
  data,
  height,
  fill,
}: {
  data: AdminInsightsActivity;
  height?: number;
  fill?: boolean;
}) {
  const t = useT();
  const series = useMemo(() => {
    if (!data.buckets.some((b) => b.active_profiles > 0)) {
      return null;
    }
    const byDate = new Map(data.buckets.map((b) => [b.label_date, b]));
    const grid = buildActivityBucketGrid(data.from, data.to, data.tz, data.bucket_unit);
    const dates = grid.length > 0 ? grid : data.buckets.map((b) => b.label_date);
    const point = (date: string, pick: "active_profiles" | "active_accounts"): LineChartPoint => ({
      t: Date.parse(`${date}T00:00:00Z`),
      value: byDate.get(date)?.[pick] ?? 0,
    });
    const first = dates[0] ?? "";
    const last = dates[dates.length - 1] ?? "";
    return {
      profiles: dates.map((date) => point(date, "active_profiles")),
      accounts: dates.map((date) => point(date, "active_accounts")),
      edgeLabels: {
        start: formatActivityBucketLabel(Date.parse(`${first}T00:00:00Z`), data.bucket_unit),
        end: formatActivityBucketLabel(Date.parse(`${last}T00:00:00Z`), data.bucket_unit),
      },
    };
  }, [data]);

  if (!series) {
    return <ChartEmptyState fill={fill} message={t("web.admin.insights.activity.volume.empty")} />;
  }
  return (
    <LineChart
      fill={fill}
      height={height}
      points={series.profiles}
      seriesLabel={t("web.admin.insights.people.kpi.activeProfiles")}
      seriesIndex={0}
      overlays={[
        {
          label: t("web.admin.insights.people.kpi.activeAccounts"),
          seriesIndex: 1,
          points: series.accounts,
        },
      ]}
      edgeLabels={series.edgeLabels}
      formatTimestamp={(ms) => formatActivityBucketLabel(ms, data.bucket_unit)}
      minTickStep={1}
      ariaLabel={t("web.admin.insights.activity.activePeople.title")}
    />
  );
}
