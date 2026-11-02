import { useMemo, useState } from "react";

import { Card, CardContent } from "@/components/ui/card";
import { cn } from "@/lib/utils";
import { useAdminTimeseries } from "@/hooks/queries/admin/dashboardInsights";
import { formatDataTotal, formatMbps, formatMbpsValue } from "../format";
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
import { buildEgressBands } from "./egressBands";
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
 *
 * Once a stream node has served egress in the window, a Total/Nodes switch
 * splits the same total into stacked bands per node (see buildEgressBands).
 * Installs without proxy nodes never see the switch.
 */
export function EgressWidget() {
  const { range } = useWidgetRange();
  const query = useAdminTimeseries(rangeHours(range));
  const [byNode, setByNode] = useState(false);
  const bands = useMemo(() => buildEgressBands(query.data), [query.data]);
  const showBands = byNode && bands !== null;
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
  // Integrated server-side from every sampled minute; summing `points` here
  // would be wrong, since wide windows carry each bucket's peak minute.
  const totalBytes = query.data?.egress_bytes;

  return (
    <Card className="h-full">
      <WidgetHeader title={rangeTitle("Egress", range)}>
        {totalBytes != null ? (
          // Secondary to the peak: when the header is too narrow for both
          // beside the range picker, the total is dropped rather than
          // truncated. CardHeader is the `card-header` container.
          <span className="text-muted-foreground hidden text-[11px] whitespace-nowrap tabular-nums @md/card-header:inline">
            Total {formatDataTotal(totalBytes)}
          </span>
        ) : null}
        <span className="text-muted-foreground text-[11px] whitespace-nowrap tabular-nums">
          Peak {formatMbps(peak)}
        </span>
        {bands ? <EgressViewToggle byNode={byNode} onChange={setByNode} /> : null}
        <WidgetRangePicker />
      </WidgetHeader>
      <CardContent className="flex min-h-0 flex-1 flex-col">
        <TimeseriesChartBody
          query={query}
          points={points}
          seriesLabel="Egress"
          overlays={
            hasDownloadTraffic && !showBands
              ? [{ label: "Downloads", points: downloadPoints, seriesIndex: 1 }]
              : undefined
          }
          stacks={showBands ? bands : undefined}
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

/**
 * Total/Nodes switch, styled like the range picker beside it. Two toggle
 * buttons rather than one: each says which view it shows, and the pressed
 * state tells a screen reader which one is on.
 */
function EgressViewToggle({
  byNode,
  onChange,
}: {
  byNode: boolean;
  onChange: (byNode: boolean) => void;
}) {
  const options = [
    { byNode: false, label: "Total", ariaLabel: "Show total egress" },
    { byNode: true, label: "Nodes", ariaLabel: "Show egress by node" },
  ];
  return (
    <div className="border-border/60 bg-muted/20 flex shrink-0 items-center gap-0.5 rounded-full border p-0.5">
      {options.map((option) => {
        const active = option.byNode === byNode;
        return (
          <button
            key={option.label}
            type="button"
            aria-pressed={active}
            aria-label={option.ariaLabel}
            className={cn(
              "cursor-pointer rounded-full px-1.5 py-0.5 text-[10px] leading-none font-semibold transition-colors",
              "focus-visible:ring-ring focus-visible:ring-2 focus-visible:outline-none",
              active
                ? "border-primary/40 bg-primary/15 text-foreground"
                : "text-muted-foreground hover:text-foreground hover:bg-muted/60",
            )}
            onClick={() => onChange(option.byNode)}
          >
            {option.label}
          </button>
        );
      })}
    </div>
  );
}
