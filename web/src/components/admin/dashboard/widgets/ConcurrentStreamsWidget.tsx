import { useMemo } from "react";

import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card";
import { useAdminTimeseries } from "@/hooks/queries/admin/dashboardInsights";
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
 * Concurrent playback sessions over the chosen window, one point per bucket the
 * server returned. Buckets the sampler missed break the line instead of
 * dropping to zero — "the server was down" and "nobody was watching" are
 * different facts.
 *
 * A wide window is bucketed server-side and each bucket reports its peak
 * minute, so the summit of a spike survives being zoomed out.
 */
export function ConcurrentStreamsWidget() {
  const { range } = useWidgetRange();
  const query = useAdminTimeseries(rangeHours(range));
  const points = useMemo(
    () => buildTimeseriesPoints(query.data, (point) => point.streams),
    [query.data],
  );

  const peak = points.reduce((max, point) => Math.max(max, point.value ?? 0), 0);

  return (
    <Card className="h-full">
      <CardHeader className="flex shrink-0 flex-row items-center justify-between gap-2 space-y-0 pb-3">
        <CardTitle className="text-sm font-bold">
          {rangeTitle("Concurrent streams", range)}
        </CardTitle>
        <div className="flex min-w-0 items-center gap-2">
          <span className="text-muted-foreground text-[11px] tabular-nums">
            Peak {peak.toLocaleString()}
          </span>
          <WidgetRangePicker />
        </div>
      </CardHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col">
        <TimeseriesChartBody
          query={query}
          points={points}
          seriesLabel="Streams"
          ariaLabel={`Concurrent streams over ${rangePhrase(range)}`}
          errorMessage="Failed to load stream history."
          emptyMessage="No stream samples yet"
          formatTimestamp={(t) => formatRangeTimestamp(range, t, { withTime: true })}
          edgeLabels={rangeEdgeLabels(range)}
          minTickStep={1}
          fill
        />
      </CardContent>
    </Card>
  );
}
