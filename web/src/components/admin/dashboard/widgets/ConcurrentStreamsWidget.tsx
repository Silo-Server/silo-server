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
import { WidgetViewToggle } from "../WidgetViewToggle";
import {
  buildMethodBands,
  buildMethodLines,
  hasMethodSplit,
  NEUTRAL_SERIES_COLOR,
} from "./concurrentStreamsSeries";
import { TimeseriesChartBody } from "./timeseriesChart";
import { buildTimeseriesPoints } from "./timeseriesSeries";
import { formatLocale } from "@/lib/appLocale";
import { useAppLanguage, useT } from "@/i18n";

/**
 * Concurrent playback sessions over the chosen window, one point per bucket the
 * server returned. Buckets the sampler missed break the line instead of
 * dropping to zero — "the server was down" and "nobody was watching" are
 * different facts.
 *
 * A wide window is bucketed server-side and each bucket reports its peak
 * minute, so the summit of a spike survives being zoomed out.
 *
 * Three views, stored with the placement:
 * - Lines (default): the total in a neutral color, with direct play, direct
 *   stream and transcode as their own lines in the Playback activity colors.
 * - Stacked: the same methods as bands that add up to the total.
 * - Total: the total alone.
 */
export function ConcurrentStreamsWidget() {
  const t = useT();
  // The method labels come from the catalog, so the series follow the language.
  const language = useAppLanguage();
  const { range, view, setView } = useWidgetRange();
  const query = useAdminTimeseries(rangeHours(range));
  // Offered until a response shows the server cannot split the total, so the
  // switch does not lose a button while the first read is in flight.
  const canStack = !query.data || hasMethodSplit(query.data);
  const mode = view === "total" || (view === "stacked" && canStack) ? view : "lines";

  const points = useMemo(
    () => buildTimeseriesPoints(query.data, (point) => point.streams),
    [query.data],
  );
  const lines = useMemo(
    () => (mode === "lines" ? buildMethodLines(query.data) : undefined),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- language re-runs the labels
    [query.data, mode, language],
  );
  const bands = useMemo(
    () => (mode === "stacked" && query.data ? buildMethodBands(query.data) : undefined),
    // eslint-disable-next-line react-hooks/exhaustive-deps -- language re-runs the labels
    [query.data, mode, language],
  );

  const peak = points.reduce((max, point) => Math.max(max, point.value ?? 0), 0);

  return (
    <Card className="h-full">
      <WidgetHeader
        title={rangeTitle(t("web.admin.dashboard.widgets.concurrentStreams.title"), range)}
      >
        <span className="text-muted-foreground text-[11px] whitespace-nowrap tabular-nums">
          {t("web.admin.dashboard.widgets.concurrentStreams.peak", {
            value: peak.toLocaleString(formatLocale()),
          })}
        </span>
        <WidgetViewToggle
          value={mode}
          options={[
            {
              value: "lines",
              label: t("web.admin.dashboard.widgets.concurrentStreams.viewLines"),
              ariaLabel: t("web.admin.dashboard.widgets.concurrentStreams.viewLinesLabel"),
            },
            ...(canStack
              ? [
                  {
                    value: "stacked" as const,
                    label: t("web.admin.dashboard.widgets.concurrentStreams.viewStacked"),
                    ariaLabel: t("web.admin.dashboard.widgets.concurrentStreams.viewStackedLabel"),
                  },
                ]
              : []),
            {
              value: "total",
              label: t("web.admin.dashboard.widgets.concurrentStreams.viewTotal"),
              ariaLabel: t("web.admin.dashboard.widgets.concurrentStreams.viewTotalLabel"),
            },
          ]}
          onChange={setView}
        />
        <WidgetRangePicker />
      </WidgetHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col">
        <TimeseriesChartBody
          query={query}
          points={points}
          seriesLabel={
            mode === "lines"
              ? t("web.admin.dashboard.chart.total")
              : t("web.admin.dashboard.widgets.concurrentStreams.seriesLabel")
          }
          color={mode === "lines" ? NEUTRAL_SERIES_COLOR : undefined}
          overlays={lines}
          stacks={bands}
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
