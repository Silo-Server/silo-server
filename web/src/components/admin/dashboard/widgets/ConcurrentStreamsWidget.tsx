import { useMemo } from "react";

import { Card, CardContent } from "@/components/ui/card";
import { useAdminTimeseries } from "@/hooks/queries/admin/dashboardInsights";
import {
  formatRangeTimestamp,
  rangeEdgeLabels,
  rangeHours,
  rangePhrase,
  rangeTitle,
} from "../range";
import { useWidgetRange } from "../widgetChrome";
import { WidgetHeader } from "../WidgetHeader";
import { WidgetRangePicker } from "../WidgetRangePicker";
import { TimeseriesChartBody } from "./timeseriesChart";
import { buildTimeseriesPoints } from "./timeseriesSeries";
import { formatLocale } from "@/lib/appLocale";
import { useT } from "@/i18n";

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
  const t = useT();
  const { range } = useWidgetRange();
  const query = useAdminTimeseries(rangeHours(range));
  const points = useMemo(
    () => buildTimeseriesPoints(query.data, (point) => point.streams),
    [query.data],
  );

  const peak = points.reduce((max, point) => Math.max(max, point.value ?? 0), 0);

  return (
    <Card className="h-full">
      <WidgetHeader
        title={rangeTitle(t("web.admin.dashboard.widgets.concurrentStreams.title"), range)}
      >
        <span className="text-muted-foreground text-[11px] tabular-nums">
          {t("web.admin.dashboard.widgets.concurrentStreams.peak", {
            value: peak.toLocaleString(formatLocale()),
          })}
        </span>
        <WidgetRangePicker />
      </WidgetHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col">
        <TimeseriesChartBody
          query={query}
          points={points}
          seriesLabel={t("web.admin.dashboard.widgets.concurrentStreams.seriesLabel")}
          ariaLabel={t("web.admin.dashboard.widgets.concurrentStreams.chartLabel", {
            range: rangePhrase(range),
          })}
          errorMessage={t("web.admin.dashboard.widgets.concurrentStreams.loadFailed")}
          emptyMessage={t("web.admin.dashboard.widgets.concurrentStreams.empty")}
          formatTimestamp={(time) => formatRangeTimestamp(range, time, { withTime: true })}
          edgeLabels={rangeEdgeLabels(range)}
          minTickStep={1}
          fill
        />
      </CardContent>
    </Card>
  );
}
