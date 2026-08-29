import { useMemo } from "react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAdminTimeseries } from "@/hooks/queries/admin/dashboardInsights";
import { formatMbps, formatMbpsValue } from "../format";
import {
  formatRangeTimestamp,
  rangeEdgeLabels,
  rangeHours,
  rangePhrase,
  rangeTitle,
} from "../range";
import { useWidgetRange } from "../widgetChrome";
import { WidgetRangePicker } from "../WidgetRangePicker";
import { TimeseriesChartBody } from "./timeseriesChart";
import { buildTimeseriesPoints } from "./timeseriesSeries";

/**
 * Egress the deployment served over the chosen window, in Mbps.
 *
 * The sampler mixes two sources into one number: the rolling average each
 * stream node reports, and the exact bytes each API process served itself. A
 * node-less single-server install charts the second alone. Wide windows are
 * bucketed server-side to the peak minute in each bucket, so "Peak" means the
 * same thing at every range.
 *
 * The main line is always the total. When the window contains measured
 * download traffic, the download subset is drawn as a second line under it —
 * never as a derived "playback" line: past the two-hour resolution the
 * server's total and download maxima are peak-preserved independently, so
 * they can come from different minutes and their difference is not any
 * minute's playback rate.
 */
export function EgressWidget() {
  const { range } = useWidgetRange();
  const query = useAdminTimeseries(rangeHours(range));
  const hasDownloadTraffic = useMemo(
    () => (query.data?.points ?? []).some((point) => (point.download_egress_kbps ?? 0) > 0),
    [query.data],
  );
  const points = useMemo(
    () => buildTimeseriesPoints(query.data, (point) => point.egress_kbps / 1_000),
    [query.data],
  );
  const downloadPoints = useMemo(
    () =>
      hasDownloadTraffic
        ? buildTimeseriesPoints(query.data, (point) => (point.download_egress_kbps ?? 0) / 1_000)
        : [],
    [query.data, hasDownloadTraffic],
  );

  const peak = points.reduce((max, point) => Math.max(max, point.value ?? 0), 0);

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between gap-2 space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">{rangeTitle("Egress", range)}</CardTitle>
        <div className="flex min-w-0 items-center gap-2">
          <span className="text-muted-foreground text-[11px] tabular-nums">
            Peak {formatMbps(peak)}
          </span>
          <WidgetRangePicker />
        </div>
      </CardHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col">
        <TimeseriesChartBody
          query={query}
          points={points}
          seriesLabel="Egress"
          overlays={
            hasDownloadTraffic
              ? [{ label: "Downloads", points: downloadPoints, seriesIndex: 1 }]
              : undefined
          }
          ariaLabel={`Egress over ${rangePhrase(range)}, in megabits per second`}
          errorMessage="Failed to load egress history."
          emptyMessage="No egress samples yet"
          formatValue={formatMbps}
          formatTick={formatMbpsValue}
          formatTimestamp={(t) => formatRangeTimestamp(range, t, { withTime: true })}
          edgeLabels={rangeEdgeLabels(range)}
          fill
        />
      </CardContent>
    </Card>
  );
}
